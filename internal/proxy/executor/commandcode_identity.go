package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const commandcodeMaxRetries = 2

// commandcodeWorkingDir is the gateway's own working directory, reported as
// config.workingDir and (slugified) as x-project-slug, as upstream does.
var commandcodeWorkingDir = func() string {
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "/"
}()

// commandcodeProjectSlug falls back to "9router-go" when the working dir
// slugifies to nothing (cwd "/" in a container): upstream rejects an empty
// x-project-slug with 400.
var commandcodeProjectSlug = func() string {
	if s := commandcodeSlugify(commandcodeWorkingDir); s != "" {
		return s
	}
	return "9router-go"
}()

var (
	slugNonAlnum = regexp.MustCompile(`[^a-z\d]+`)
	slugCamelA   = regexp.MustCompile(`([A-Z]{2,})(\d+)`)
	slugCamelB   = regexp.MustCompile(`([a-z\d]+)([A-Z]{2,})`)
	slugCamelC   = regexp.MustCompile(`([a-z\d])([A-Z])`)
	slugCamelD   = regexp.MustCompile(`([A-Z]+)([A-Z][a-rt-z\d]+)`)
	slugReplacer = strings.NewReplacer("&", " and ", "ß", "ss", "ẞ", "Ss", "ä", "ae", "Ä", "Ae", "ö", "oe", "Ö", "Oe", "ü", "ue", "Ü", "Ue", "Æ", "AE", "æ", "ae", "Ø", "O", "ø", "o", "Œ", "OE", "œ", "oe", "Ð", "D", "ð", "d", "Đ", "D", "đ", "d", "Þ", "TH", "þ", "th", "Ł", "L", "ł", "l", "ı", "i")
)

// commandcodeSlugify mirrors @sindresorhus/slugify defaults, as used by the
// official CLI for x-project-slug: "/home/u/My Project" → "home-u-my-project".
func commandcodeSlugify(v string) string {
	v = norm.NFD.String(slugReplacer.Replace(v))
	v = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, v)
	v = slugCamelA.ReplaceAllString(v, "$1 $2")
	v = slugCamelB.ReplaceAllString(v, "$1 $2")
	v = slugCamelC.ReplaceAllString(v, "$1 $2")
	v = slugCamelD.ReplaceAllString(v, "$1 $2")
	v = slugNonAlnum.ReplaceAllString(strings.ToLower(v), "-")
	return strings.Trim(v, "-")
}

// commandcodeSessionKey resolves a conversation-stable key for the commandcode
// threadId/x-session-id pair. The upstream prompt cache is keyed on that
// identity: the official CLI mints ONE uuid per conversation and reuses it for
// every turn, and a fresh id per request kept cache reads at 0 across ~27k
// requests on the upstream side (decolua/9router#5006). So the chain below
// always ends in something conversation-stable — never a per-request random:
//
//  1. the client's session headers (req.SessionID: x-claude-code-session-id,
//     x-session-id, …)
//  2. session fields in the body — Claude Code carries its session in
//     metadata.user_id, OpenAI clients in prompt_cache_key/session_id/
//     conversation_id — which headers miss
//  3. the conversation prefix (system text + first user turn): constant while
//     later turns append
//  4. the connection id, so one connection keeps one warm cache
func commandcodeSessionKey(body []byte, headerSession, connectionID string) string {
	if headerSession != "" {
		return headerSession
	}
	if s := commandcodeBodySession(body); s != "" {
		return s
	}
	if p := commandcodePrefixKey(body); p != "" {
		return "prefix\000" + p
	}
	if connectionID != "" {
		return "conn\000" + connectionID
	}
	return ""
}

var commandcodeClaudeSessionRe = regexp.MustCompile(`(?i)_session_([0-9a-f][0-9a-f-]*)$`)

// commandcodeBodySession reads the client's own session declaration out of the
// request body (the body is OpenAI-shaped here; headers are handled by the
// caller). Claude Code embeds its session in metadata.user_id, either as
// "..._session_<uuid>" or as a JSON string; OpenAI-style clients may set
// prompt_cache_key/session_id/conversation_id directly.
func commandcodeBodySession(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		PromptCacheKey string `json:"prompt_cache_key"`
		SessionID      string `json:"session_id"`
		ConversationID string `json:"conversation_id"`
		Metadata       struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	if uid := strings.TrimSpace(probe.Metadata.UserID); uid != "" {
		if m := commandcodeClaudeSessionRe.FindStringSubmatch(uid); m != nil {
			return "claude\000" + strings.ToLower(m[1])
		}
		if strings.HasPrefix(uid, "{") {
			var parsed struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal([]byte(uid), &parsed); err == nil && parsed.SessionID != "" {
				return "claude\000" + strings.TrimSpace(parsed.SessionID)
			}
		}
		return "claude\000" + uid
	}
	for _, v := range []string{probe.PromptCacheKey, probe.SessionID, probe.ConversationID} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// commandcodePrefixKey keys the conversation on its stable prefix — system
// text plus the first user turn, joined the same way upstream's
// commandCodeSession.js does ("\n\n" between system parts, "\0" before the
// user turn). Later turns only append, so the prefix never changes mid-
// conversation; a different first turn hashes differently.
func commandcodePrefixKey(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	system := commandcodeFlattenContent(probe.System)
	firstUser := ""
	for _, msg := range probe.Messages {
		if msg.Role == "user" {
			firstUser = commandcodeFlattenContent(msg.Content)
			break
		}
	}
	if system == "" && firstUser == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(system + "\n\n\000" + firstUser))
	return hex.EncodeToString(sum[:])
}

// commandcodeFlattenContent renders string content or a text/image block array
// as plain text (Claude Messages and OpenAI chat shapes share enough of it).
func commandcodeFlattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var out []string
	for _, block := range blocks {
		var obj struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &obj); err == nil && obj.Text != "" {
			out = append(out, obj.Text)
		}
	}
	return strings.Join(out, "\n")
}

// commandcodeSessionUUID derives a deterministic UUID-shaped id from the
// client session so one conversation keeps one threadId (prefix cache
// friendly). The session key comes from commandcodeSessionKey; an empty one
// yields a random UUID as a last resort.
func commandcodeSessionUUID(sessionID string) string {
	if sessionID == "" {
		return uuid.New().String()
	}
	sum := sha256.Sum256([]byte(sessionID))
	h := hex.EncodeToString(sum[:])
	n := hexNibble(h[16])
	v := "0123456789abcdef"[(n&0x3)|0x8]
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-" + string(v) + h[17:20] + "-" + h[20:32]
}

func hexNibble(c byte) int {
	if c >= 'a' {
		return int(c-'a') + 10
	}
	return int(c - '0')
}
