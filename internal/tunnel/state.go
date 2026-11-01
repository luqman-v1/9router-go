package tunnel

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"9router/proxy/internal/config"
)

// tunnelDir is where cloudflared's PID file and the quick-tunnel state live.
// It is deliberately the same directory the upstream Node implementation uses
// (`<DATA_DIR>/tunnel`), because both binaries resolve the same data dir: an
// operator who ran upstream 9router and then switched to this binary must not
// end up with two tunnel states, or a tunnel that one of them thinks is dead
// while the other still holds the URL.
//
// It is a function rather than a package variable because the data dir is
// resolved from the environment and from .env, both of which can change after
// this package is initialised. A variable captured at init would keep pointing
// at whatever DATA_DIR happened to be when the process started, and every
// tunnel read would silently use a stale path.
func tunnelDir() string {
	return filepath.Join(config.ResolveDataDir(), "tunnel")
}

func stateFilePath() string { return filepath.Join(tunnelDir(), "state.json") }

// State is the persisted quick-tunnel identity. shortId is what makes the
// public URL stable across restarts, so it is written once and reused.
type State struct {
	ShortID   string `json:"shortId"`
	TunnelURL string `json:"tunnelUrl"`
	// PreviousURL holds the address the tunnel served before the current one.
	// A quick tunnel's hostname rotates on every reconnect, so without this the
	// dashboard cannot tell a fresh tunnel from one that simply re-registered
	// the same address — and the operator has no way to learn that the URL they
	// handed out stopped working.
	PreviousURL string `json:"previousUrl,omitempty"`
	// RotatedAt is when TunnelURL last changed, so the dashboard can say "the
	// URL changed 4 minutes ago" rather than only "the URL is different".
	RotatedAt time.Time `json:"rotatedAt,omitempty"`
	// Mode records which kind of tunnel produced this state, so a restart
	// knows whether to respawn a quick tunnel or re-run a named one.
	Mode string `json:"mode,omitempty"`
}

// shortIDChars omits the characters that read ambiguously in a URL someone
// has to retype by hand: 0/O, 1/l/I. The same set upstream uses.
const shortIDChars = "abcdefghijklmnpqrstuvwxyz23456789"

const (
	shortIDMinLen = 3
	shortIDMaxLen = 20
)

// ValidateShortID reports whether an operator-supplied shortId is usable as a
// DNS label. It becomes both the tunnel name and a hostname segment, so the
// rules are the intersection of what Cloudflare accepts for a tunnel name and
// what a URL can carry: lowercase alphanumerics and interior hyphens, no
// leading or trailing hyphen, and a length that keeps the generated hostname
// well inside the 63-character DNS label limit.
func ValidateShortID(id string) error {
	if len(id) < shortIDMinLen || len(id) > shortIDMaxLen {
		return fmt.Errorf("tunnel.ValidateShortID: length must be %d-%d characters, got %d", shortIDMinLen, shortIDMaxLen, len(id))
	}
	if strings.HasPrefix(id, "-") || strings.HasSuffix(id, "-") {
		return errors.New("tunnel.ValidateShortID: must not start or end with a hyphen")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("tunnel.ValidateShortID: invalid character %q (lowercase letters, digits and hyphens only)", r)
		}
	}
	return nil
}

// GenerateShortID mints a random identifier. Used when the operator leaves the
// field empty, and as the fallback when a stored id fails validation.
//
// math/rand/v2 has no global seed, so the generator is created per call and
// seeded from the OS. The value is a DNS label, not a secret, so a
// non-cryptographic source is the right tool here.
func GenerateShortID() string {
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0)) //nolint:gosec // identifier, not a secret
	b := make([]byte, 6)
	for i := range b {
		b[i] = shortIDChars[rng.IntN(len(shortIDChars))]
	}
	return string(b)
}

// ResolveShortID normalises an operator-supplied id into the bare form the
// worker expects, falling back to a fresh random one when it does not validate.
// An invalid custom id must never abort the enable — the operator asked for a
// tunnel, not for a validation lecture.
//
// The `r` is drawn inside the field in the dashboard, so an operator who types
// one anyway — or a client calling the API directly, which has no dashboard to
// show it — gets `rajaja` stored, which serves
// `https://rrajaja.abc-tunnel.us`: an address the worker does not route.
//
// Only a *single* leading `r` is removed, and only when what follows is long
// enough to be a real id. `router` and `redfox` are legitimate shortIds that
// happen to start with `r`, and trimming them would silently rename them to
// `outer` and `edfox` — different tunnels, different public URLs, no error
// anywhere to explain it. The length guard is what separates the two cases:
// a typed prefix is followed by the id the operator actually chose, which is
// at least shortIDMinLen long on its own.
func ResolveShortID(custom string) string {
	custom = strings.ToLower(strings.TrimSpace(custom))
	// Trim the typed prefix, then keep trimming while the result still looks
	// like a prefixed value: an operator who pasted `rrajaja` has typed the
	// prefix twice, and one pass would leave `rajaja` — still wrong.
	for len(custom) > shortIDMinLen && strings.HasPrefix(custom, "r") {
		custom = custom[1:]
	}
	if custom != "" && ValidateShortID(custom) == nil {
		return custom
	}
	return GenerateShortID()
}

func ensureTunnelDir() error {
	return os.MkdirAll(tunnelDir(), 0o755)
}

// LoadState reads the persisted identity. A missing or corrupt file is not an
// error: the tunnel simply has no history, which is the normal first run.
func LoadState() *State {
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return nil
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	return &st
}

// previousURL and rotatedAt read the rotation fields off a state that may be
// absent. A gateway that has never run a tunnel has no state file, and the
// status handler still has to answer — a nil check at each call site is the
// kind of thing that gets forgotten on the next field added.
func (s *State) previousURL() string {
	if s == nil {
		return ""
	}
	return s.PreviousURL
}

func (s *State) rotatedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.RotatedAt
}

// RecordTunnelURL stores the address a running tunnel reported, keeping the
// previous one when it changed.
//
// The distinction it preserves is between "the tunnel re-registered the same
// address" and "the address rotated". Only the second case leaves an operator
// with a dead URL in their notes, so only the second one is worth surfacing —
// and it cannot be told apart after the fact, because the old value is gone
// from the state file by then.
//
// This file is the only store of the live Cloudflare hostname. The settings row
// used to carry a copy, written once at enable and never again, and the login
// gate in internal/auth read it — so after a rotation the gate kept deciding
// access for a hostname the tunnel no longer served while the live one went
// undecided. That gate now reads this file, and the settings key is gone.
func RecordTunnelURL(url string) error {
	st := LoadState()
	if st == nil {
		st = &State{}
	}
	// A first record is not a rotation: there is no previous address to
	// supersede, so nothing changed for the consumers of this file.
	rotated := st.TunnelURL != "" && st.TunnelURL != url
	if rotated {
		st.PreviousURL = st.TunnelURL
		st.RotatedAt = time.Now()
	}
	st.TunnelURL = url
	return SaveState(st)
}

// SaveState writes the identity atomically enough for this use: a truncated
// state.json would only cost a re-register, never a broken tunnel.
func SaveState(st *State) error {
	if err := ensureTunnelDir(); err != nil {
		return fmt.Errorf("tunnel.SaveState: create dir: %w", err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("tunnel.SaveState: marshal: %w", err)
	}
	if err := os.WriteFile(stateFilePath(), data, 0o600); err != nil {
		return fmt.Errorf("tunnel.SaveState: write: %w", err)
	}
	return nil
}

// ClearTunnelURL keeps the shortId but drops the URL, which is what upstream's
// disable does: the identity survives so a re-enable reuses the same public
// hostname instead of rotating it.
func ClearTunnelURL() error {
	st := LoadState()
	if st == nil {
		return nil
	}
	st.TunnelURL = ""
	return SaveState(st)
}
