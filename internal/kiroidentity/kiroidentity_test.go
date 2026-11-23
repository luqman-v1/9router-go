package kiroidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// sha256Hex is deliberately independent of MachineID: if the helper's hashing
// or seed handling drifts, these expectations still catch it.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Exact upstream strings for both client kinds (open-sse/shared/kiroIdentity.js).
func TestKiroIdentityHeadersStreamingAndRuntimeUserAgent(t *testing.T) {
	const refreshToken = "social-refresh-token-for-ua-test"
	machineID := sha256Hex(refreshToken)
	id := Identity{RefreshToken: refreshToken}

	tests := []struct {
		name      string
		kind      ClientKind
		wantUA    string
		wantAmzUA string
	}{
		{
			name: "streaming",
			kind: ClientKindStreaming,
			wantUA: "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 " +
				"api/codewhispererstreaming#1.0.0 m/E KiroIDE-1.4.154-" + machineID,
			wantAmzUA: "aws-sdk-js/1.0.0 KiroIDE-1.4.154-" + machineID,
		},
		{
			name: "runtime",
			kind: ClientKindRuntime,
			wantUA: "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 " +
				"api/codewhispererruntime#1.0.0 m/N,E KiroIDE-1.4.154-" + machineID,
			wantAmzUA: "aws-sdk-js/1.0.0 KiroIDE-1.4.154-" + machineID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ua, amzUA := Headers(tt.kind, id)
			if ua != tt.wantUA {
				t.Errorf("Headers(%q).User-Agent = %q, want %q", tt.kind, ua, tt.wantUA)
			}
			if amzUA != tt.wantAmzUA {
				t.Errorf("Headers(%q).X-Amz-User-Agent = %q, want %q", tt.kind, amzUA, tt.wantAmzUA)
			}
		})
	}
}

// Seed priority is clientId > refreshToken > profileArn > apiKey > accessToken.
func TestKiroIdentityMachineIDSeedPriority(t *testing.T) {
	tests := []struct {
		name     string
		id       Identity
		wantSeed string
	}{
		{
			name:     "client id outranks every other seed",
			id:       Identity{ClientID: "cid", RefreshToken: "rt", ProfileARN: "arn", APIKey: "ak", AccessToken: "at"},
			wantSeed: "cid",
		},
		{
			name:     "refresh token outranks profile arn and tokens",
			id:       Identity{RefreshToken: "rt", ProfileARN: "arn", APIKey: "ak", AccessToken: "at"},
			wantSeed: "rt",
		},
		{
			name:     "profile arn outranks api key and access token",
			id:       Identity{ProfileARN: "arn", APIKey: "ak", AccessToken: "at"},
			wantSeed: "arn",
		},
		{
			name:     "api key outranks access token",
			id:       Identity{APIKey: "ak", AccessToken: "at"},
			wantSeed: "ak",
		},
		{
			name:     "access token used when nothing else is set",
			id:       Identity{AccessToken: "at"},
			wantSeed: "at",
		},
		{
			name:     "blank values are skipped",
			id:       Identity{ClientID: "  ", RefreshToken: "", ProfileARN: " arn "},
			wantSeed: "arn",
		},
		{
			name:     "all empty falls back to anonymous",
			id:       Identity{},
			wantSeed: "kiro-anonymous",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MachineID(tt.id)
			want := sha256Hex(tt.wantSeed)
			if got != want {
				t.Errorf("MachineID(%+v) = %q, want sha256(%q) = %q", tt.id, got, tt.wantSeed, want)
			}
			if len(got) != 64 {
				t.Errorf("MachineID(%+v) length = %d, want 64 hex chars", tt.id, len(got))
			}
		})
	}
}

// A rotating access token must not change the machine id once a stable seed
// (clientId or refreshToken) is present — that is the whole point of the id.
func TestKiroIdentityMachineIDStableAcrossTokenRefresh(t *testing.T) {
	withClientID := Identity{ClientID: "stable-client", RefreshToken: "rt1", AccessToken: "at1"}
	withRefreshToken := Identity{RefreshToken: "stable-refresh", AccessToken: "at1"}

	tests := []struct {
		name   string
		base   Identity
		mutate func(*Identity)
	}{
		{
			name:   "client id seed survives access token rotation",
			base:   withClientID,
			mutate: func(id *Identity) { id.AccessToken = "rotated-at"; id.RefreshToken = "rotated-rt" },
		},
		{
			name:   "refresh token seed survives access token rotation",
			base:   withRefreshToken,
			mutate: func(id *Identity) { id.AccessToken = "rotated-at" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := MachineID(tt.base)
			rotated := tt.base
			tt.mutate(&rotated)
			after := MachineID(rotated)
			if before != after {
				t.Errorf("machine id changed across token refresh: %q != %q", before, after)
			}
		})
	}
}
