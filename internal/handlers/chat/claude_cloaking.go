package chat

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"9router/proxy/internal/proxy/executor"
)

// Claude OAuth cloaking — ports the Next.js dashboard's
// open-sse/utils/claudeCloaking.js. Anthropic expects Claude-Code-shaped
// requests for subscription OAuth tokens (sk-ant-oat): a billing-header
// block as system[0] and a metadata.user_id. Without them the API returns
// 429 rate_limit_error ("Error") even though auth and the body are valid.

const claudeCLIVersion = "2.1.258"

// deriveUuid mirrors the JS deriveUuid: deterministic UUID-v4-shaped string
// from a seed (stable per account).
func deriveUuid(seed string) string {
	h := sha256.Sum256([]byte(seed))
	d := hex.EncodeToString(h[:])
	var v byte
	if d[16] >= '0' && d[16] <= '9' {
		v = d[16] - '0'
	} else {
		v = d[16] - 'a' + 10
	}
	variant := ((v & 0x3) | 0x8)
	return fmt.Sprintf("%s-%s-4%s-%c%s-%s", d[0:8], d[8:12], d[13:16], hexDigit(variant), d[17:20], d[20:32])
}

func hexDigit(b byte) byte {
	const hexDigits = "0123456789abcdef"
	return hexDigits[b&0x0f]
}

// generateFakeUserID mirrors generateFakeUserID: device_id/account_uuid are
// derived from the token (stable per account), session_id is per-conversation.
func generateFakeUserID(sessionID, apiKey string) string {
	deviceSum := sha256.Sum256([]byte("device:" + apiKey))
	deviceID := hex.EncodeToString(deviceSum[:])
	accountUUID := deriveUuid("account:" + apiKey)
	if sessionID == "" {
		sessionID = randomUUID()
	}
	return fmt.Sprintf(`{"device_id":%q,"account_uuid":%q,"session_id":%q}`, deviceID, accountUUID, sessionID)
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// applyClaudeCloaking injects the billing header as system[0] and the fake
// metadata.user_id into a Claude Messages request body.
func applyClaudeCloaking(body []byte, apiKey, sessionID string) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	// cch: first 5 hex chars of sha256 over the pre-injection body.
	cchSum := sha256.Sum256(body)
	cch := hex.EncodeToString(cchSum[:])[:5]
	build := make([]byte, 2)
	if _, err := rand.Read(build); err != nil {
		build = []byte{0, 0}
	}
	billingText := fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=sdk-cli; cch=%s;",
		claudeCLIVersion, hex.EncodeToString(build)[:3], cch)
	billingBlock := map[string]any{"type": "text", "text": billingText}

	switch sys := req["system"].(type) {
	case []any:
		if len(sys) > 0 {
			if first, ok := sys[0].(map[string]any); ok {
				if t, ok := first["text"].(string); ok && strings.HasPrefix(t, "x-anthropic-billing-header:") {
					// already injected
					req["metadata"] = ensureMetadata(req["metadata"], generateFakeUserID(sessionID, apiKey))
					out, err := json.Marshal(req)
					if err != nil {
						return body
					}
					return out
				}
			}
		}
		req["system"] = append([]any{billingBlock}, sys...)
	case string:
		if sys != "" {
			req["system"] = []any{billingBlock, map[string]any{"type": "text", "text": sys}}
		} else {
			req["system"] = []any{billingBlock}
		}
	default:
		req["system"] = []any{billingBlock}
	}

	req["metadata"] = ensureMetadata(req["metadata"], generateFakeUserID(sessionID, apiKey))

	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// ensureMetadata fills in user_id when absent, preserving other metadata keys.
func ensureMetadata(meta any, userID string) map[string]any {
	m, ok := meta.(map[string]any)
	if !ok || m == nil {
		m = map[string]any{}
	}
	if _, exists := m["user_id"]; !exists {
		m["user_id"] = userID
	}
	return m
}

// --- Tool cloaking (anti-ban parity with the dashboard) ---

// claudeToolSuffix is appended to client tool names so the request looks
// like a Claude Code session (which exposes "_ide" tools), and CC_DECOY_TOOLS
// pad the tool list to the Claude Code native set.
const claudeToolSuffix = "_ide"

// cloakClaudeTools mirrors cloakClaudeTools: renames client tools (those
// without a "type" — server-side built-ins keep reserved names) with the
// _ide suffix in tools[] and every tool_use block in messages[], and appends
// the Claude Code decoy tools. Returns the suffixed→original name map
// (nil when nothing was renamed).
func cloakClaudeTools(req map[string]any) map[string]string {
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil
	}

	toolNameMap := map[string]string{}
	clientToolNames := map[string]bool{}
	clientDeclarations := []any{}

	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			clientDeclarations = append(clientDeclarations, t)
			continue
		}
		if typ, hasType := tool["type"]; hasType && typ != nil && typ != "" {
			// server-side built-in (web_search_20250305, ...): reserved name
			clientDeclarations = append(clientDeclarations, tool)
			continue
		}
		name, _ := tool["name"].(string)
		if name == "" {
			clientDeclarations = append(clientDeclarations, tool)
			continue
		}
		suffixed := name + claudeToolSuffix
		toolNameMap[suffixed] = name
		clientToolNames[name] = true
		renamed := make(map[string]any, len(tool))
		for k, v := range tool {
			renamed[k] = v
		}
		renamed["name"] = suffixed
		clientDeclarations = append(clientDeclarations, renamed)
	}

	// No client tool was renamed (client declared only server-side built-ins,
	// or nothing): decoys would go upstream without an active decloaker and
	// raw decoy calls would leak to the client. Skip them entirely.
	if len(toolNameMap) == 0 {
		return nil
	}

	for _, name := range executor.CCDecoyTools {
		clientDeclarations = append(clientDeclarations, map[string]any{
			"name":         name,
			"description":  "This tool is currently unavailable.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
		})
	}
	req["tools"] = clientDeclarations

	// Rename tool_use blocks in history. Only client tools are renamed:
	// server-side built-ins (web_search_20250305, ...) keep their reserved
	// names, and already-suffixed names are not double-suffixed.
	if msgs, ok := req["messages"].([]any); ok {
		for _, m := range msgs {
			msg, ok := m.(map[string]any)
			if !ok {
				continue
			}
			content, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, c := range content {
				block, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if block["type"] != "tool_use" {
					continue
				}
				if name, ok := block["name"].(string); ok && clientToolNames[name] {
					block["name"] = name + claudeToolSuffix
				}
			}
		}
	}

	// Forced tool_choice must point at the suffixed name.
	if tc, ok := req["tool_choice"].(map[string]any); ok {
		if tc["type"] == "tool" {
			if name, ok := tc["name"].(string); ok && clientToolNames[name] {
				tc["name"] = name + claudeToolSuffix
			}
		}
	}

	if len(toolNameMap) == 0 {
		return nil
	}
	return toolNameMap
}
