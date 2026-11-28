package guardrails

import (
	"strings"
	"testing"
)

// Fixture values are assembled from two literals so the file never carries a
// whole credential shape: GitHub push protection scans every push, and it reads
// a test fixture exactly as it reads a live key. Splitting costs nothing here —
// the engine sees the same bytes either way.

func TestSecretDetectorsFire(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		detector string
	}{
		{
			name:     "openai key",
			text:     "here is my key: " + "sk-proj" + "1234567890abcdefghijklmnop",
			detector: "openai_key",
		},
		{
			name:     "anthropic key",
			text:     "sk-ant-" + "api03-1234567890abcdefghijklmnopqrstuvwx",
			detector: "anthropic_key",
		},
		{
			name:     "openrouter key",
			text:     "sk-or-" + "v1-0123456789abcdef0123456789abcdef",
			detector: "openrouter_key",
		},
		{
			name:     "google api key",
			text:     "AIza" + strings.Repeat("A1b2", 8) + "AAA",
			detector: "google_api_key",
		},
		{
			name:     "hugging face token",
			text:     "hf_" + strings.Repeat("a1b2", 9),
			detector: "huggingface_token",
		},
		{
			name:     "github personal access token",
			text:     "ghp_" + strings.Repeat("a1B2", 12),
			detector: "github_token",
		},
		{
			name:     "slack bot token",
			text:     "xoxb-" + strings.Repeat("f1", 12),
			detector: "slack_token",
		},
		{
			name:     "aws long-term access key id",
			text:     "AKIA" + "A1B2A1B2A1B2A1B2",
			detector: "aws_access_key_id",
		},
		{
			name:     "authorization header",
			text:     "Authorization: Bearer " + "abcdefghijklmnopqrstuvwx",
			detector: "bearer_token",
		},
		{
			name:     "labelled secret in json",
			text:     `{"api_key": "` + "s3cr3t-value-1234567890" + `", "model": "gpt-4o"}`,
			detector: "labelled_secret",
		},
	}

	engine := NewEngine([]string{"secrets"}, ActionBlock)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := engine.Scan(tt.text)
			var found string
			for _, f := range d.Findings {
				if f.Detector == tt.detector {
					found = f.Detector
					break
				}
			}
			if found == "" {
				t.Fatalf("no %s finding in %q; findings: %+v", tt.detector, tt.text, d.Findings)
			}
			if d.Action != ActionBlock {
				t.Errorf("action = %q, want block", d.Action)
			}
		})
	}
}

// TestSecretDetectorsLeaveCodeClean is the guardrail on the detector set
// itself. A credentials detector that fires on ordinary development content
// gets switched off, and a switched-off detector protects nothing.
func TestSecretDetectorsLeaveCodeClean(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{
			name: "git sha and uuid",
			text: "commit 9f2c4e7a1b8d3f6a5c2e9b1d4f7a8c3e6b9d2f5a\nuuid=1b4e28ba-2fa1-11d2-883f-0016d3cca427",
		},
		{
			name: "key read from the environment",
			text: "api_key = os.Getenv(\"OPENAI_API_KEY\")",
		},
		{
			name: "key interpolated from a shell variable",
			text: "export OPENAI_API_KEY=${OPENAI_API_KEY}",
		},
		{
			name: "short placeholder value",
			text: "password = \"hunter2\"\ntoken: abc123",
		},
		{
			name: "prose naming the field",
			text: "// the password column is hashed with bcrypt, never logged",
		},
		{
			name: "base64 image payload",
			text: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
		},
		{
			name: "source discussing tokens as a concept",
			text: "func rotateToken(t *Token) error { return db.Save(t) }",
		},
	}

	engine := NewEngine([]string{"secrets"}, ActionBlock)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := engine.Scan(tt.text)
			if len(d.Findings) != 0 {
				t.Errorf("clean content produced %d finding(s): %+v", len(d.Findings), d.Findings)
			}
			if d.Action != ActionAllow {
				t.Errorf("action = %q, want allow", d.Action)
			}
		})
	}
}

// TestSecretMaskKeepsTheLabel checks the redaction a masked request actually
// receives: the credential is gone, and the text around it still reads as a
// header or an assignment.
func TestSecretMaskKeepsTheLabel(t *testing.T) {
	engine := NewEngine([]string{"secrets"}, ActionMask)

	header := "Authorization: Bearer " + "abcdefghijklmnopqrstuvwx"
	d := engine.Scan(header)
	if !d.WasMutated() {
		t.Fatalf("mask action did not mutate %q; findings: %+v", header, d.Findings)
	}
	if want := "Authorization: Bearer " + redactMask; d.Mutated != want {
		t.Errorf("mutated = %q, want %q", d.Mutated, want)
	}
	if strings.Contains(d.Mutated, "abcdefghijklmnopqrstuvwx") {
		t.Errorf("mutated still carries the token: %q", d.Mutated)
	}

	secret := "s3cr3t-" + "value-1234567890"
	assignment := `{"client_secret":"` + secret + `","grant_type":"client_credentials"}`
	d = engine.Scan(assignment)
	if !d.WasMutated() {
		t.Fatalf("mask action did not mutate %q; findings: %+v", assignment, d.Findings)
	}
	if strings.Contains(d.Mutated, secret) {
		t.Errorf("mutated still carries the secret: %q", d.Mutated)
	}
	if !strings.Contains(d.Mutated, `"client_secret":`) {
		t.Errorf("mutated dropped the label: %q", d.Mutated)
	}
	if !strings.Contains(d.Mutated, "grant_type") {
		t.Errorf("mutated dropped a neighbouring field: %q", d.Mutated)
	}

	// Padding is credential bytes. A base64 token's trailing = must fall
	// inside the mask, or the forwarded prompt keeps part of the value.
	paddedBearer := "Authorization: Bearer " + "abcdefghijklmnopqr" + "=="
	d = engine.Scan(paddedBearer)
	if !d.WasMutated() {
		t.Fatalf("mask action did not mutate %q; findings: %+v", paddedBearer, d.Findings)
	}
	if strings.HasSuffix(d.Mutated, "==") {
		t.Errorf("mask left the padding after it: %q", d.Mutated)
	}
	if !strings.HasSuffix(d.Mutated, redactMask) {
		t.Errorf("masked header should end at the mask: %q", d.Mutated)
	}

	paddedSecret := "s3cr3t-" + "value-1234567890"
	paddedAssignment := `{"client_secret":"` + paddedSecret + `==","grant_type":"client_credentials"}`
	d = engine.Scan(paddedAssignment)
	if !d.WasMutated() {
		t.Fatalf("mask action did not mutate %q; findings: %+v", paddedAssignment, d.Findings)
	}
	if strings.Contains(d.Mutated, paddedSecret) {
		t.Errorf("mutated still carries the secret: %q", d.Mutated)
	}
	if !strings.Contains(d.Mutated, redactMask+`"`) {
		t.Errorf("masked assignment should close right after the mask: %q", d.Mutated)
	}
}

// TestSecretAttributionIsExclusive: one key must be one finding. The generic
// sk- pattern also matches Anthropic and OpenRouter keys, so without the
// exclusivity gate both fire and the generic one claims the attribution the
// provider-specific rule earned.
func TestSecretAttributionIsExclusive(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"anthropic key", "sk-ant-" + "api03-1234567890abcdefghijklmnopqrstuvwx", "anthropic_key"},
		{"openrouter key", "sk-or-" + "v1-0123456789abcdef0123456789abcdef", "openrouter_key"},
		{"plain openai key", "sk-" + "proj1234567890abcdefghijklmnop", "openai_key"},
	}

	engine := NewEngine([]string{"secrets"}, ActionBlock)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := engine.Scan(tt.text)
			if len(d.Findings) != 1 {
				t.Fatalf("findings = %+v, want exactly one %s", d.Findings, tt.want)
			}
			if d.Findings[0].Detector != tt.want {
				t.Errorf("detector = %q, want %q", d.Findings[0].Detector, tt.want)
			}
		})
	}
}

// TestScanJSONMasksLabelledSecretInMapKey is the path that is actually sent
// upstream: the middleware walks the decoded JSON, so a map value arrives at
// the detector without its key. Without key context the labelled detector
// never fires and the credential goes out unmasked.
func TestScanJSONMasksLabelledSecretInMapKey(t *testing.T) {
	engine := NewEngine([]string{"secrets"}, ActionMask)
	secret := "s3cr3t-" + "value-1234567890"
	payload := map[string]any{
		"model":         "claude-sonnet",
		"client_secret": secret,
		"nested":        map[string]any{"api_key": secret},
	}

	scanned, d := engine.ScanJSON(payload)
	if !d.WasMutated() {
		t.Fatalf("structured payload was not masked: %+v", d.Findings)
	}
	out, ok := scanned.(map[string]any)
	if !ok {
		t.Fatalf("scanned payload changed shape: %T", scanned)
	}
	for key, want := range map[string]string{"client_secret": redactMask, "nested": redactMask} {
		value, _ := out[key].(string)
		if value == "" {
			nested, _ := out[key].(map[string]any)
			value, _ = nested["api_key"].(string)
		}
		if value == "" {
			t.Fatalf("%s lost its value: %v", key, out[key])
		}
		if strings.Contains(value, secret) {
			t.Errorf("%s still carries the secret: %q", key, value)
		}
		if !strings.Contains(value, want) {
			t.Errorf("%s was not masked: %q", key, value)
		}
	}
	if out["model"] != "claude-sonnet" {
		t.Errorf("unrelated field was rewritten: %v", out["model"])
	}
}

// TestSecretsDetectorIsIsolated proves the set runs only when asked: a policy
// naming pii alone must not rewrite a credential, because an operator who
// never selected the set did not consent to it.
func TestSecretsDetectorIsIsolated(t *testing.T) {
	key := "sk-" + "1234567890abcdefghijklmnop"
	text := "Bearer " + "abcdefghijklmnopqrstuvwx" + " and " + key

	if d := NewEngine([]string{"pii"}, ActionMask).Scan(text); d.WasMutated() {
		t.Errorf("pii-only policy mutated a secret: %q", d.Mutated)
	}
	if d := NewEngine([]string{"injection"}, ActionMask).Scan(text); d.WasMutated() {
		t.Errorf("injection-only policy mutated a secret: %q", d.Mutated)
	}
	d := NewEngine([]string{"pii", "secrets"}, ActionMask).Scan(text)
	if !d.WasMutated() || strings.Contains(d.Mutated, key) {
		t.Errorf("combined policy did not mask the key: %q", d.Mutated)
	}
}
