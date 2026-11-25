package kiroidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestIDEVersion(t *testing.T) {
	if IDEVersion != "1.4.154" {
		t.Errorf("IDEVersion = %q, want 1.4.154", IDEVersion)
	}
}

func TestHeadersExact(t *testing.T) {
	const seed = "test-seed"
	sum := sha256.Sum256([]byte(seed))
	id := hex.EncodeToString(sum[:])

	tests := []struct {
		name      string
		client    string
		wantUA    string
		wantAmzUA string
	}{
		{
			name:   "streaming",
			client: "streaming",
			wantUA: "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 " +
				"api/codewhispererstreaming#1.0.0 m/E KiroIDE-1.4.154-" + id,
			wantAmzUA: "aws-sdk-js/1.0.0 KiroIDE-1.4.154-" + id,
		},
		{
			name:   "runtime",
			client: "runtime",
			wantUA: "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 " +
				"api/codewhispererruntime#1.0.0 m/N,E KiroIDE-1.4.154-" + id,
			wantAmzUA: "aws-sdk-js/1.0.0 KiroIDE-1.4.154-" + id,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ua, amzUA := Headers(seed, tt.client)
			if ua != tt.wantUA {
				t.Errorf("Headers UA = %q, want %q", ua, tt.wantUA)
			}
			if amzUA != tt.wantAmzUA {
				t.Errorf("Headers amzUA = %q, want %q", amzUA, tt.wantAmzUA)
			}
		})
	}
}

func TestMachineIDStableAndDistinct(t *testing.T) {
	id1 := MachineID("same-seed")
	id2 := MachineID("same-seed")
	if id1 != id2 {
		t.Errorf("same seed produced different IDs: %q != %q", id1, id2)
	}
	if len(id1) != 64 {
		t.Errorf("machine id length = %d, want 64", len(id1))
	}
	if id3 := MachineID("other-seed"); id3 == id1 {
		t.Errorf("different seeds produced same ID: %q", id3)
	}
}

func TestSeedPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		psd          map[string]any
		refreshToken string
		accessToken  string
		want         string
	}{
		{name: "clientId wins", psd: map[string]any{"clientId": "cid", "profileArn": "arn", "apiKey": "key"}, refreshToken: "rt", accessToken: "at", want: "cid"},
		{name: "refreshToken next", psd: map[string]any{"profileArn": "arn", "apiKey": "key"}, refreshToken: "rt", accessToken: "at", want: "rt"},
		{name: "profileArn next", psd: map[string]any{"profileArn": "arn", "apiKey": "key"}, accessToken: "at", want: "arn"},
		{name: "apiKey next", psd: map[string]any{"apiKey": "key"}, accessToken: "at", want: "key"},
		{name: "accessToken next", refreshToken: "", accessToken: "at", want: "at"},
		{name: "anonymous", want: "kiro-anonymous"},
		{name: "empty clientId skipped", psd: map[string]any{"clientId": "  "}, refreshToken: "rt", want: "rt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Seed(tt.psd, tt.refreshToken, tt.accessToken); got != tt.want {
				t.Errorf("Seed = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHeadersCarryVersionForAnyClient(t *testing.T) {
	ua, amzUA := Headers("seed", "unknown-client")
	if !strings.Contains(ua, "KiroIDE-1.4.154-") {
		t.Errorf("streaming UA missing version: %q", ua)
	}
	if !strings.Contains(amzUA, "KiroIDE-1.4.154-") {
		t.Errorf("amz UA missing version: %q", amzUA)
	}
}
