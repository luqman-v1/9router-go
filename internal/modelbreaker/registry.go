package modelbreaker

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/errclass"
)

// The registry is keyed on provider plus model, with both lowercased: the same
// pair reaches this layer from a registered provider's connection, a custom
// provider node, and the dashboard, and those three paths do not always agree
// on case. A single mutex guards the map and each breaker locks itself, which
// is the same shape internal/resilience/open-sse uses so the two layers stay
// readable together.

var (
	registryMu sync.Mutex
	registry   = map[string]*Breaker{}
)

// Key builds the registry key for one provider/model pair.
func Key(provider, model string) string {
	return strings.ToLower(provider) + "/" + strings.ToLower(model)
}

// CountsTowardBreaker reports whether an upstream error is the model's own
// fault, which is the only case this layer may charge.
//
// The rule is errclass's Scope plus its Kind: only an availability failure
// (KindModelUnavailable) counts. A context-window overflow is ScopeModel too,
// but it is produced by the caller's own prompt, so charging it lets any
// caller open the breaker for a provider/model pair and 503 every other
// tenant after five oversized requests (#1450). ScopeProvider (5xx,
// transport) is the provider breaker's job — charging it here would put
// every model of a briefly-down provider into cooldown and then disable all
// of them after three cycles. ScopeAccount (429, quota, auth) is the
// connection-rotation layer's job for the same reason. ScopeRequest cools
// nothing.
func CountsTowardBreaker(class errclass.Classification) bool {
	return class.EffectiveScope() == errclass.ScopeModel &&
		class.Kind == errclass.KindModelUnavailable
}

// Classify turns an upstream status plus body into the classification this
// layer needs, for the call site that has not already classified the error.
//
// The body is read, not searched: providers carry their verdict in a JSON
// envelope ("error":{"type":…,"message":…,"code":…}), and errclass's precedence
// is type first, then that message, then the numeric status. Handing the raw
// bytes to ClassifyDetail as a message would skip the type and feed the JSON
// prose to the phrase table, so a typed model verdict would fall through to the
// status map and a 404 would be the only thing left to decide the scope.
func Classify(status int, body []byte) errclass.Classification {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var env struct {
			Type  string          `json:"type"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(trimmed), &env) == nil {
			errRaw := bytes.TrimSpace(env.Error)
			if len(errRaw) > 0 && errRaw[0] == '{' {
				var detail struct {
					Type    string          `json:"type"`
					Message string          `json:"message"`
					Code    json.RawMessage `json:"code"`
					Status  string          `json:"status"`
				}
				if json.Unmarshal(errRaw, &detail) == nil {
					code := 0
					if codeFromRaw(detail.Code, &code) {
						return errclass.ClassifyDetail(detail.Type, code, detail.Status, detail.Message)
					}
					return errclass.ClassifyDetail(detail.Type, 0, detail.Status, detail.Message)
				}
			}
			// A bare {"error":"…"} envelope: the string is the message.
			if len(errRaw) > 0 && errRaw[0] == '"' {
				var msg string
				if json.Unmarshal(errRaw, &msg) == nil && msg != "" {
					return errclass.ClassifyDetail("", status, "", msg)
				}
			}
		}
	}
	return errclass.ClassifyDetail("", status, "", trimmed)
}

// codeFromRaw reads a numeric or numeric-string JSON code into out, reporting
// whether it found one. Non-numeric code values (a provider's own code enum)
// carry no status, so they leave out untouched and the status the caller passed
// decides.
func codeFromRaw(raw json.RawMessage, out *int) bool {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return false
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		*out = n
		return true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			*out = v
			return true
		}
	}
	return false
}

// Bound on the registry. AGENTS.md §4.G requires bounding everything that
// grows, and this map is keyed on a model id that originates in the request
// body, so an unauthenticated client can create a permanent entry per distinct
// model string. Two independent bounds, because either alone has a hole:
//
//   - maxEntries caps the count, so a flood of distinct ids cannot grow the map
//     without limit;
//   - evictIdleFor removes entries that have not failed for a whole retention
//     window, so an entry whose owner stopped calling the model leaves on its
//     own and the cap only ever bites during an actual flood.
//
// Eviction is lossy on purpose: a breaker holds no durable state, and the next
// BreakerFor recreates the pair from its first cooldown cycle. The worst case
// is one model waiting 60 seconds longer than it otherwise would — never a
// wrong admission decision, because Allow() reads the entry that exists now.
//
// The cap check lives in BreakerFor rather than in a background sweeper: a
// package-level goroutine would need a lifecycle the request path does not have,
// and this map only grows on the request path, so bounding it there means the
// size is checked exactly when it could have grown.
const (
	maxEntries   = 4096
	evictIdleFor = 30 * time.Minute
)

// evictLocked brings the registry back within bounds. The caller must hold
// registryMu.
//
// Order matters and both rules run on every call: idle entries go first, then
// the oldest of what is left until the cap is met. Dropping only the oldest
// would leave an idle entry parked until a flood finally reached it, and
// dropping only the idle ones would let a flood of *recently-failing* distinct
// ids grow the map past the cap forever. A cap that cannot shed an entry that
func evictLocked() {
	// The idle pass runs on every call, independent of the cap: a pair nobody
	// has failed for a whole retention window must leave even when the registry
	// is nowhere near full, otherwise the cap becomes the only thing that ever
	// removes anything and a mostly-idle registry holds its entries forever.
	// O(n) over a map that is normally a handful of entries, and this runs only
	// on the create path, past the "does this pair exist" lookup that already
	// cost a map read.
	idleCutoff := time.Now().Add(-evictIdleFor)
	for k, b := range registry {
		if last := b.lastFailureTime(); last.Before(idleCutoff) {
			delete(registry, k)
		}
	}

	// Then the hard cap, which has to shed even recently-failing entries or a
	// flood of distinct ids grows the map past it. Strictly less than the cap,
	// because BreakerFor adds one entry immediately after this returns: an
	// equality check would leave the map one entry over, permanently.
	if len(registry) < maxEntries {
		return
	}
	type entry struct {
		key  string
		last time.Time
	}
	entries := make([]entry, 0, len(registry))
	for k, b := range registry {
		last := b.lastFailureTime()
		if last.IsZero() {
			// Never failed: only its creation makes it exist. A zero stamp is
			// trivially the oldest, and evicting it removes no state at all.
			last = idleCutoff
		}
		entries = append(entries, entry{key: k, last: last})
	}
	// Oldest first: the head of this order is what gets dropped. Dropping a
	// recently-failing pair to satisfy a cap is the one case where this layer
	// takes a live model out of rotation for a boundary condition instead of
	// for its own behaviour — which is why the cap is a high-water mark and not
	// the routine removal path.
	slices.SortFunc(entries, func(a, b entry) int {
		return a.last.Compare(b.last)
	})
	for _, e := range entries {
		if len(registry) < maxEntries {
			return
		}
		delete(registry, e.key)
	}
}

// AllStatuses returns a snapshot of every tracked model, for the dashboard.
func AllStatuses() []Status {
	registryMu.Lock()
	breakers := make([]*Breaker, 0, len(registry))
	for _, b := range registry {
		breakers = append(breakers, b)
	}
	registryMu.Unlock()
	out := make([]Status, 0, len(breakers))
	for _, b := range breakers {
		out = append(out, b.Status())
	}
	return out
}

// ResetAll closes every breaker. Used by the dashboard's reset control and by
// tests.
func ResetAll() {
	registryMu.Lock()
	breakers := make([]*Breaker, 0, len(registry))
	for _, b := range registry {
		breakers = append(breakers, b)
	}
	registryMu.Unlock()
	for _, b := range breakers {
		b.Reset()
	}
}
