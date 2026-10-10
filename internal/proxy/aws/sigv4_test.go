package aws

import (
	"strings"
	"testing"
	"time"
)

// The two pinned signatures below are AWS's own published SigV4 test vectors (the
// "iam.amazonaws.com" example from the AWS docs and the Bedrock-shaped request the
// upstream port cross-verified byte-for-byte against the `aws4` reference
// implementation). Pinning the values rather than recomputing them from the same code
// under test is the whole point: a refactor that breaks canonicalisation must fail here.
const (
	pinSigningTime = "2015-08-30T12:36:00Z"

	pinAccessKeyID     = "AKIDEXAMPLE"
	pinSecretAccessKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"

	pinIAMAuthz = "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-length;content-type;host;x-amz-date, " +
		"Signature=afcae41d2eaf39c1b479fff3b5ae64d6806f975e7b6efb60038301a42c8dcb14"

	// A real Bedrock model id: the ":0" version suffix is what makes path encoding matter.
	pinBedrockAuthz = "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20150830/us-east-1/bedrock/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=53d28679a943780f541a376e6ae08bb2b10379169a756a881a166d63132c94ab"
)

func pinnedSigningTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, pinSigningTime)
	if err != nil {
		t.Fatalf("parse pinned signing time: %v", err)
	}
	return parsed
}

func pinCredentials() Credentials {
	return Credentials{AccessKeyID: pinAccessKeyID, SecretAccessKey: pinSecretAccessKey}
}

func iamSignOptions() SignOptions {
	body := "Action=ListUsers&Version=2010-05-08"
	return SignOptions{
		Method: "POST",
		URL:    "https://iam.amazonaws.com/",
		Headers: map[string]string{
			"Content-Type":   "application/x-www-form-urlencoded; charset=utf-8",
			"Content-Length": "35",
		},
		Body:        body,
		Region:      "us-east-1",
		Service:     "iam",
		Credentials: pinCredentials(),
	}
}

func bedrockSignOptions() SignOptions {
	return SignOptions{
		Method:  "POST",
		URL:     "https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-sonnet-4-20250514-v1%3A0/invoke-with-response-stream",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    `{"anthropic_version":"bedrock-2023-05-31","messages":[]}`,
		Region:  "us-east-1",
		Service: "bedrock",
		// The URL above is already escaped once, which is exactly what the executor
		// builds. The signer escapes it a second time for the canonical request.
		Credentials: pinCredentials(),
	}
}

func TestSignRequestPinnedSignatures(t *testing.T) {
	tests := []struct {
		name string
		opts SignOptions
		want string
	}{
		{
			name: "aws published vector for a plain signed request",
			opts: iamSignOptions(),
			want: pinIAMAuthz,
		},
		{
			name: "bedrock path holding a versioned model id",
			opts: bedrockSignOptions(),
			want: pinBedrockAuthz,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, err := SignRequestAt(tt.opts, pinnedSigningTime(t))
			if err != nil {
				t.Fatalf("SignRequestAt() error = %v", err)
			}
			if got := headers["Authorization"]; got != tt.want {
				t.Errorf("Authorization =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestSignRequestDateHeader(t *testing.T) {
	headers, err := SignRequestAt(iamSignOptions(), pinnedSigningTime(t))
	if err != nil {
		t.Fatalf("SignRequestAt() error = %v", err)
	}
	if got, want := headers["x-amz-date"], "20150830T123600Z"; got != want {
		t.Errorf("x-amz-date = %q, want %q", got, want)
	}
}

// A temporary credential that is sent but not signed is the classic cause of
// SignatureDoesNotMatch on STS and SSO identities, so pin that the token is both
// transmitted and part of SignedHeaders.
func TestSignRequestSignsSessionToken(t *testing.T) {
	opts := bedrockSignOptions()
	opts.Credentials.SessionToken = "FwoGZXIvYXdzEExample"

	headers, err := SignRequestAt(opts, pinnedSigningTime(t))
	if err != nil {
		t.Fatalf("SignRequestAt() error = %v", err)
	}
	if got, want := headers["x-amz-security-token"], "FwoGZXIvYXdzEExample"; got != want {
		t.Errorf("x-amz-security-token = %q, want %q", got, want)
	}
	if !strings.Contains(headers["Authorization"], "SignedHeaders=content-type;host;x-amz-date;x-amz-security-token") {
		t.Errorf("Authorization does not sign the session token: %q", headers["Authorization"])
	}
}

func TestSignRequestHostIsSigned(t *testing.T) {
	headers, err := SignRequestAt(bedrockSignOptions(), pinnedSigningTime(t))
	if err != nil {
		t.Fatalf("SignRequestAt() error = %v", err)
	}
	if got := headers["host"]; got != "bedrock-runtime.us-east-1.amazonaws.com" {
		t.Errorf("host = %q, want bedrock-runtime.us-east-1.amazonaws.com", got)
	}
}

// Single-encoding is S3-only. Both forms are valid SigV4, so the assertion is that the
// switch is live: if it were dead code, every Bedrock request carrying a versioned model
// id would fail to authenticate and no other test here would notice.
func TestSignRequestPathEncodingSwitchIsLive(t *testing.T) {
	doubled := bedrockSignOptions()
	single := bedrockSignOptions()
	single.SingleEncodePath = true

	doubledHeaders, err := SignRequestAt(doubled, pinnedSigningTime(t))
	if err != nil {
		t.Fatalf("SignRequestAt(doubled) error = %v", err)
	}
	singleHeaders, err := SignRequestAt(single, pinnedSigningTime(t))
	if err != nil {
		t.Fatalf("SignRequestAt(single) error = %v", err)
	}
	if doubledHeaders["Authorization"] == singleHeaders["Authorization"] {
		t.Error("single- and double-encoded path produced identical signatures; the switch is dead code")
	}
}

func TestSignRequestRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*SignOptions)
		wantErr string
	}{
		{
			name: "missing secret access key",
			mutate: func(o *SignOptions) {
				o.Credentials = Credentials{AccessKeyID: "AKIA"}
			},
			wantErr: "AccessKeyID and SecretAccessKey",
		},
		{
			name: "missing access key id",
			mutate: func(o *SignOptions) {
				o.Credentials = Credentials{SecretAccessKey: "secret"}
			},
			wantErr: "AccessKeyID and SecretAccessKey",
		},
		{
			name:    "missing region",
			mutate:  func(o *SignOptions) { o.Region = "" },
			wantErr: "requires a region",
		},
		{
			name:    "missing service",
			mutate:  func(o *SignOptions) { o.Service = "" },
			wantErr: "requires a service name",
		},
		{
			name:    "relative URL",
			mutate:  func(o *SignOptions) { o.URL = "/model/x/invoke" },
			wantErr: "must be absolute",
		},
		{
			// SigV4 wants the query sorted by percent-encoded name with AWS escaping;
			// a half-implementation would emit a subtly invalid canonical request for
			// some future caller. Refusing the input is the honest contract.
			name:    "query string",
			mutate:  func(o *SignOptions) { o.URL = o.URL + "?b=2&a=a%20b" },
			wantErr: "does not support query strings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := iamSignOptions()
			tt.mutate(&opts)
			_, err := SignRequestAt(opts, pinnedSigningTime(t))
			if err == nil {
				t.Fatalf("SignRequestAt() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestEscapeURI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bedrock model id keeps dots and dashes, escapes the colon",
			input: "us.anthropic.claude-sonnet-4-20250514-v1:0",
			want:  "us.anthropic.claude-sonnet-4-20250514-v1%3A0",
		},
		{
			// encodeURIComponent and url.PathEscape both leave these alone; AWS does not.
			name:  "escapes the sub-delimiters AWS escapes",
			input: "a!b'c(d)e*f",
			want:  "a%21b%27c%28d%29e%2Af",
		},
		{
			name:  "leaves the unreserved set alone",
			input: "abcXYZ019-_.~",
			want:  "abcXYZ019-_.~",
		},
		{
			name:  "escapes a space as percent twenty, never plus",
			input: "a b",
			want:  "a%20b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeURI(tt.input); got != tt.want {
				t.Errorf("escapeURI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Header canonicalisation collapses internal whitespace and sorts by name; getting either
// wrong produces a signature AWS rejects with no hint about which part was wrong.
func TestCanonicalHeaders(t *testing.T) {
	canonical, signed := canonicalHeaders(map[string]string{
		"X-Amz-Date":    "  20150830T123600Z  ",
		"Content-Type":  "application/json",
		"HOST":          "bedrock-runtime.us-east-1.amazonaws.com",
		"X-Custom":      "a   b\t\tc",
		"Empty-Content": "",
	})

	wantSigned := "content-type;empty-content;host;x-amz-date;x-custom"
	if signed != wantSigned {
		t.Errorf("SignedHeaders = %q, want %q", signed, wantSigned)
	}
	wantCanonical := "content-type:application/json\n" +
		"empty-content:\n" +
		"host:bedrock-runtime.us-east-1.amazonaws.com\n" +
		"x-amz-date:20150830T123600Z\n" +
		"x-custom:a b c\n"
	if canonical != wantCanonical {
		t.Errorf("canonicalHeaders =\n%q\nwant\n%q", canonical, wantCanonical)
	}
}

func TestRedactSignedHeaders(t *testing.T) {
	opts := bedrockSignOptions()
	opts.Credentials.SessionToken = "FwoGZXIvYXdzEExample"
	signed, err := SignRequestAt(opts, pinnedSigningTime(t))
	if err != nil {
		t.Fatalf("SignRequestAt() error = %v", err)
	}

	redacted := RedactSignedHeaders(signed)

	if strings.Contains(redacted["Authorization"], pinBedrockAuthz) {
		t.Error("Authorization still carries the live signature")
	}
	if !strings.Contains(redacted["Authorization"], "Signature=<redacted>") {
		t.Errorf("Authorization = %q, want the signature replaced", redacted["Authorization"])
	}
	// The credential scope names the key id and the signed-header list; both are needed
	// to debug a SignatureDoesNotMatch and neither replays the request.
	if !strings.Contains(redacted["Authorization"], pinAccessKeyID) {
		t.Error("Authorization lost the access key id, which the log needs for debugging")
	}
	if !strings.Contains(redacted["Authorization"], "SignedHeaders=content-type;host;x-amz-date") {
		t.Error("Authorization lost the signed-header list")
	}
	if redacted["x-amz-security-token"] != "<redacted>" {
		t.Errorf("x-amz-security-token = %q, want <redacted>", redacted["x-amz-security-token"])
	}
	if signed["x-amz-security-token"] != "FwoGZXIvYXdzEExample" {
		t.Error("RedactSignedHeaders mutated its input")
	}
}

// SignRequest's wall-clock path must produce the same shape as the pinned one, or the
// pinned tests would be testing a function production never calls.
func TestSignRequestUsesWallClock(t *testing.T) {
	opts := bedrockSignOptions()
	headers, err := SignRequest(opts)
	if err != nil {
		t.Fatalf("SignRequest() error = %v", err)
	}
	amzDate, ok := headers["x-amz-date"]
	if !ok {
		t.Fatal("SignRequest() omitted x-amz-date")
	}
	parsed, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		t.Fatalf("x-amz-date %q is not in SigV4 compact form: %v", amzDate, err)
	}
	if delta := time.Since(parsed); delta < 0 || delta > time.Minute {
		t.Errorf("x-amz-date %q is %v away from now", amzDate, delta)
	}
	if !strings.HasPrefix(headers["Authorization"], sigV4Algorithm+" Credential="+pinAccessKeyID+"/") {
		t.Errorf("Authorization = %q, want the pinned credential scope prefix", headers["Authorization"])
	}
}
