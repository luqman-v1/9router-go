package executor

import (
	"regexp"
	"testing"

	"9router/proxy/internal/providers"
)

var (
	uuidRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hex32RE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

func TestCodebuddyConversationHeaders_FamilyPresent(t *testing.T) {
	req := &Request{Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	h := codebuddyConversationHeaders(req, req.Body)
	if !uuidRE.MatchString(h["X-Conversation-ID"]) {
		t.Errorf("X-Conversation-ID = %q, want uuid", h["X-Conversation-ID"])
	}
	for _, k := range []string{"X-Request-ID", "X-Conversation-Request-ID", "X-Conversation-Message-ID"} {
		if !hex32RE.MatchString(h[k]) {
			t.Errorf("%s = %q, want 32 hex chars", k, h[k])
		}
	}
	if h["X-Conversation-Message-ID"] != h["X-Request-ID"] {
		t.Errorf("X-Conversation-Message-ID (%s) must match X-Request-ID (%s)", h["X-Conversation-Message-ID"], h["X-Request-ID"])
	}
}

func TestCodebuddyConversationHeaders_StableAcrossRetries(t *testing.T) {
	req := &Request{Body: []byte(`{"messages":[{"role":"user","content":"same opening"}]}`)}
	a := codebuddyConversationHeaders(req, req.Body)
	b := codebuddyConversationHeaders(req, req.Body)
	if a["X-Conversation-ID"] != b["X-Conversation-ID"] {
		t.Errorf("conversation id not stable: %s != %s", a["X-Conversation-ID"], b["X-Conversation-ID"])
	}
	if a["X-Conversation-Request-ID"] == b["X-Conversation-Request-ID"] {
		t.Error("request id must be fresh per request")
	}
	if a["X-Conversation-Message-ID"] == b["X-Conversation-Message-ID"] {
		t.Error("message id must be fresh per message")
	}
}

func TestCodebuddyConversationHeaders_SessionIDPrecedence(t *testing.T) {
	a := codebuddyConversationHeaders(&Request{SessionID: "sess-123"}, []byte(`{"messages":[]}`))
	b := codebuddyConversationHeaders(&Request{SessionID: "sess-456"}, []byte(`{"messages":[]}`))
	c := codebuddyConversationHeaders(&Request{SessionID: "sess-123"}, []byte(`{"messages":[]}`))
	if a["X-Conversation-ID"] != c["X-Conversation-ID"] {
		t.Error("same session id must map to same conversation id")
	}
	if a["X-Conversation-ID"] == b["X-Conversation-ID"] {
		t.Error("different session ids must map to different conversation ids")
	}
}

func TestCodebuddyConversationHeaders_TypedBlockContent(t *testing.T) {
	req := &Request{Body: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"typed block"}]}]}`)}
	h := codebuddyConversationHeaders(req, req.Body)
	if !uuidRE.MatchString(h["X-Conversation-ID"]) {
		t.Errorf("typed-block derivation produced non-uuid: %q", h["X-Conversation-ID"])
	}
}

func TestCodebuddyConversationHeaders_RandomFallback(t *testing.T) {
	req := &Request{Body: []byte(`{"messages":[]}`)}
	a := codebuddyConversationHeaders(req, req.Body)
	b := codebuddyConversationHeaders(req, req.Body)
	if !uuidRE.MatchString(a["X-Conversation-ID"]) {
		t.Errorf("fallback id not a uuid: %q", a["X-Conversation-ID"])
	}
	if a["X-Conversation-ID"] == b["X-Conversation-ID"] {
		t.Error("with nothing to derive from, ids must be random")
	}
}

func TestFirstUserText_StringAndBlocks(t *testing.T) {
	s := firstUserText([]byte(`{"messages":[{"role":"assistant","content":"skip"},{"role":"user","content":"plain"}]}`))
	if s != "plain" {
		t.Errorf("string content: got %q", s)
	}
	b := firstUserText([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"blk"}]}]}`))
	if b != "blk" {
		t.Errorf("blocks content: got %q", b)
	}
}

func TestCodebuddyHeaders_ChannelVerification(t *testing.T) {
	cnCfg := &providers.ProviderConfig{BaseURL: "https://copilot.tencent.com/v2/chat/completions"}
	intlCfg := &providers.ProviderConfig{BaseURL: "https://www.codebuddy.ai/v2/chat/completions"}

	cnReq := &Request{Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	intlReq := &Request{Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

	cnHeaders := codebuddyHeaders(cnReq, cnCfg, cnReq.Body)
	intlHeaders := codebuddyHeaders(intlReq, intlCfg, intlReq.Body)

	if cnHeaders["User-Agent"] != "CLI/2.108.1 CodeBuddy/2.108.1" {
		t.Errorf("cn User-Agent = %q", cnHeaders["User-Agent"])
	}
	if cnHeaders["X-App"] != "cli" {
		t.Errorf("cn X-App = %q", cnHeaders["X-App"])
	}
	if cnHeaders["X-IDE-Type"] != "CLI" || cnHeaders["X-IDE-Name"] != "CLI" || cnHeaders["X-IDE-Version"] != "2.108.1" {
		t.Errorf("cn IDE headers mismatch: type=%q name=%q ver=%q", cnHeaders["X-IDE-Type"], cnHeaders["X-IDE-Name"], cnHeaders["X-IDE-Version"])
	}
	if cnHeaders["X-Domain"] != "copilot.tencent.com" {
		t.Errorf("cn X-Domain = %q, want copilot.tencent.com", cnHeaders["X-Domain"])
	}
	if cnHeaders["X-Stainless-Lang"] != "js" || cnHeaders["X-Stainless-Runtime"] != "node" {
		t.Errorf("cn Stainless headers mismatch: lang=%q runtime=%q", cnHeaders["X-Stainless-Lang"], cnHeaders["X-Stainless-Runtime"])
	}
	if !hex32RE.MatchString(cnHeaders["X-Request-ID"]) {
		t.Errorf("cn X-Request-ID = %q, want 32 hex chars", cnHeaders["X-Request-ID"])
	}
	if cnHeaders["X-Conversation-ID"] == "" {
		t.Error("cn X-Conversation-ID must be present")
	}
	if cnHeaders["X-Agent-Intent"] != "craft" || cnHeaders["X-Agent-Purpose"] != "conversation" {
		t.Errorf("cn Agent headers mismatch: intent=%q purpose=%q", cnHeaders["X-Agent-Intent"], cnHeaders["X-Agent-Purpose"])
	}

	if intlHeaders["User-Agent"] != "CLI/2.108.1 CodeBuddy/2.108.1" {
		t.Errorf("intl User-Agent = %q", intlHeaders["User-Agent"])
	}
	if intlHeaders["X-Domain"] != "www.codebuddy.ai" {
		t.Errorf("intl X-Domain = %q, want www.codebuddy.ai", intlHeaders["X-Domain"])
	}
	if !hex32RE.MatchString(intlHeaders["X-Request-ID"]) {
		t.Errorf("intl X-Request-ID = %q, want 32 hex chars", intlHeaders["X-Request-ID"])
	}
}
