package executor

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

func testSAJSON(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	sa := map[string]any{
		"type":         "service_account",
		"project_id":   "proj-abc",
		"client_email": "svc@proj-abc.iam.gserviceaccount.com",
		"private_key":  pemText,
	}
	raw, _ := json.Marshal(sa)
	return string(raw)
}

// A service-account key arrives as JSON in the apiKey field, and the lane has to
// recognise it: a raw key goes in the query string while an SA key mints a
// Bearer token, and getting that wrong sends a signed JWT as an API key.
func TestParseVertexServiceAccount(t *testing.T) {
	sa := parseVertexServiceAccount(testSAJSON(t))
	if sa == nil {
		t.Fatal("a valid service-account key was not recognised")
	}
	if sa.ProjectID != "proj-abc" {
		t.Errorf("project = %q, want proj-abc", sa.ProjectID)
	}

	// A plain API key is not JSON at all.
	if got := parseVertexServiceAccount("AIzaSyPlainKey"); got != nil {
		t.Error("a raw API key was taken for a service account")
	}
	// JSON that is not a service account.
	if got := parseVertexServiceAccount(`{"type":"authorized_user"}`); got != nil {
		t.Error("an ADC credential was taken for a service account")
	}
	// An SA key missing a required field is not usable.
	if got := parseVertexServiceAccount(`{"type":"service_account","client_email":"x@y"}`); got != nil {
		t.Error("an incomplete service account was accepted")
	}
}

func TestParseVertexADC(t *testing.T) {
	adc := parseVertexADC(`{"type":"authorized_user","client_id":"cid","client_secret":"cs","refresh_token":"rt","quota_project_id":"p1"}`)
	if adc == nil {
		t.Fatal("an ADC credential was not recognised")
	}
	if adc.QuotaProject != "p1" {
		t.Errorf("quota project = %q, want p1", adc.QuotaProject)
	}
	if got := parseVertexADC(`{"type":"service_account"}`); got != nil {
		t.Error("a service account was taken for an ADC credential")
	}
	if got := parseVertexADC(`{"type":"authorized_user","refresh_token":"rt"}`); got != nil {
		t.Error("an ADC credential without a client_id was accepted")
	}
}

// The assertion is a real RS256 JWT: three dot-separated segments, the claims
// Google requires, and a header naming RS256.
func TestVertexJWTAssertionShape(t *testing.T) {
	sa := parseVertexServiceAccount(testSAJSON(t))
	assertion, err := vertexJWTAssertion(sa)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d segments, want 3: %s", len(parts), assertion)
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if !strings.Contains(string(header), `"RS256"`) {
		t.Errorf("header does not name RS256: %s", header)
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(claims, &parsed); err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	if parsed["iss"] != sa.ClientEmail {
		t.Errorf("iss = %v, want %q", parsed["iss"], sa.ClientEmail)
	}
	if parsed["aud"] != vertexGoogleTokenURL {
		t.Errorf("aud = %v, want %q", parsed["aud"], vertexGoogleTokenURL)
	}
	if parsed["scope"] != vertexCloudScope {
		t.Errorf("scope = %v, want %q", parsed["scope"], vertexCloudScope)
	}
	if _, ok := parsed["exp"]; !ok {
		t.Error("claims carry no exp")
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		t.Errorf("signature segment is not valid base64url: %v", err)
	}
}

// A key that arrives with literal backslash-n (a JSON-in-JSON paste) still
// parses: refusing it would fail the one shape most operators paste.
func TestVertexJWTAssertionAcceptsEscapedNewlines(t *testing.T) {
	sa := parseVertexServiceAccount(testSAJSON(t))
	sa.PrivateKey = strings.ReplaceAll(sa.PrivateKey, "\n", `\n`)
	if _, err := vertexJWTAssertion(sa); err != nil {
		t.Errorf("escaped newlines were rejected: %v", err)
	}
}

func TestVertexJWTAssertionRejectsGarbage(t *testing.T) {
	if _, err := vertexJWTAssertion(&vertexServiceAccount{ClientEmail: "x", PrivateKey: "not pem"}); err == nil {
		t.Error("expected an error for a non-PEM key")
	}
}

func TestVertexModelStripsPrefixAndSuffix(t *testing.T) {
	cases := map[string]string{
		"vertex/gemini-4-argon":     "gemini-4-argon",
		"vx/gemini-3.1-pro-preview": "gemini-3.1-pro-preview",
		"gemini-4-argon-thinking":   "gemini-4-argon",
		"gemini-4-argon(high)":      "gemini-4-argon",
	}
	for in, want := range cases {
		if got := vertexModel(in); got != want {
			t.Errorf("vertexModel(%q) = %q, want %q", in, got, want)
		}
	}
}
