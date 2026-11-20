package executor

import (
	"strings"
	"testing"
)

// Anthropic rejects role "developer" (400 Unexpected role); it must fold into
// the top-level system prompt like role "system".
func TestEnsureClaudeMessages_DeveloperRoleBecomesSystem(t *testing.T) {
	in := `{"messages":[{"role":"developer","content":"be terse"},{"role":"user","content":"hi"}]}`
	out := string(EnsureClaudeMessages([]byte(in), "claude-sonnet-5"))
	if strings.Contains(out, `"developer"`) {
		t.Fatalf("developer role leaked to Claude: %s", out)
	}
	if !strings.Contains(out, `"system":"be terse"`) {
		t.Fatalf("developer text not in system: %s", out)
	}
}
