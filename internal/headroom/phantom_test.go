package headroom

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/log"
)

// TestCompress_ByteFloorCases tests the integrating function HeadroomClient.Compress
// against the 5% byte floor:
// (a) body grows from 7055 to 7455 bytes, expect original body back and compressed false
// (b) body shrinks from 21064 to 21054 bytes (0.047%), expect original body back and compressed false
// (c) body shrinks from 21064 to 19000 bytes, expect compressed true
func TestCompress_ByteFloorCases(t *testing.T) {
	tests := []struct {
		name           string
		bodyTargetLen  int
		expectedOutLen int
		origMsgLen     int
		compMsgLen     int
		numFloats      int
		tokensSaved    int
		wantCompressed bool
	}{
		{
			name:           "case a: body grows from 7055 to 7455 bytes",
			bodyTargetLen:  7055,
			expectedOutLen: 7455,
			origMsgLen:     100,
			compMsgLen:     80,
			numFloats:      420,
			tokensSaved:    50,
			wantCompressed: false,
		},
		{
			name:           "case b: body shrinks from 21064 to 21054 bytes (0.047%)",
			bodyTargetLen:  21064,
			expectedOutLen: 21054,
			origMsgLen:     100,
			compMsgLen:     90,
			numFloats:      0,
			tokensSaved:    50,
			wantCompressed: false,
		},
		{
			name:           "case c: body shrinks from 21064 to 19000 bytes",
			bodyTargetLen:  21064,
			expectedOutLen: 19000,
			origMsgLen:     2500,
			compMsgLen:     436,
			numFloats:      0,
			tokensSaved:    500,
			wantCompressed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origMsg := strings.Repeat("m", tt.origMsgLen)
			compMsg := strings.Repeat("m", tt.compMsgLen)

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				resp := map[string]any{
					"messages": []any{
						map[string]any{"role": "user", "content": compMsg},
					},
					"tokens_saved": tt.tokensSaved,
				}
				data, _ := json.Marshal(resp)
				_, _ = w.Write(data)
			}))
			defer ts.Close()

			var numsPart string
			if tt.numFloats > 0 {
				nums := make([]string, tt.numFloats)
				for i := range nums {
					nums[i] = "1e21"
				}
				numsPart = `,"nums":[` + strings.Join(nums, ",") + `]`
			}

			buildBody := func(padLen int) []byte {
				pad := strings.Repeat("p", padLen)
				return []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"` + origMsg + `"}]` + numsPart + `,"pad":"` + pad + `"}`)
			}

			// Calibrate padding to match tt.bodyTargetLen exactly
			rawBase := buildBody(0)
			neededPad := tt.bodyTargetLen - len(rawBase)
			if neededPad < 0 {
				t.Fatalf("targetLen %d too small for base %d", tt.bodyTargetLen, len(rawBase))
			}
			body := buildBody(neededPad)
			if len(body) != tt.bodyTargetLen {
				t.Fatalf("constructed body len = %d, want %d", len(body), tt.bodyTargetLen)
			}

			// Verify candidate re-marshal length matches tt.expectedOutLen
			var reqMap map[string]any
			if err := json.Unmarshal(body, &reqMap); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			reqMap["messages"] = []any{
				map[string]any{"role": "user", "content": compMsg},
			}
			newBody, err := json.Marshal(reqMap)
			if err != nil {
				t.Fatalf("marshal newBody: %v", err)
			}
			if len(newBody) != tt.expectedOutLen {
				t.Fatalf("marshaled newBody len = %d, want %d", len(newBody), tt.expectedOutLen)
			}

			client := NewClient(ts.URL, "", nil)
			resBody, compressed, err := client.Compress(context.Background(), body, "gpt-4o", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if compressed != tt.wantCompressed {
				t.Errorf("Compress() compressed = %v (outLen=%d), want %v", compressed, len(resBody), tt.wantCompressed)
			}
			if !tt.wantCompressed && string(resBody) != string(body) {
				t.Errorf("Compress() did not return original body on rejection")
			}
		})
	}
}

// TestCompress_RejectsBarelySmallerMessages verifies that when messages and body
// both shrink by less than the 5% threshold, phantom savings is rejected and a warning is logged.
func TestCompress_RejectsBarelySmallerMessages(t *testing.T) {
	log.ClearConsoleLogs()
	origMessage := strings.Repeat("x", 1000)
	nearlySameMessage := strings.Repeat("x", 995) // only 0.5% shrink

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": nearlySameMessage},
			},
			"tokens_before": 1000,
			"tokens_after":  995,
			"tokens_saved":  500, // reporting positive token savings
		}
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	bodyMap := map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": origMessage},
		},
	}
	body, _ := json.Marshal(bodyMap)

	client := NewClient(ts.URL, "", nil)
	_, compressed, err := client.Compress(context.Background(), body, "gpt-4o", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if compressed {
		t.Fatalf("expected compression to be rejected when both messages and body shrink <5%%")
	}

	var foundWarning bool
	for _, entry := range log.ConsoleEntries() {
		if strings.Contains(entry.Line, HeadroomPhantomWarningText) {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("expected phantom savings warning to be logged with %q", HeadroomPhantomWarningText)
	}
}

// TestIsHeadroomPhantomSavings_Table mirrors upstream tests/unit/headroom.test.js:
func TestIsHeadroomPhantomSavings_Table(t *testing.T) {
	tests := []struct {
		name        string
		beforeMsg   int
		afterMsg    int
		wantPhantom bool
	}{
		{"healthy message compression with large tools", 1000, 500, false},
		{"barely smaller messages", 1000, 990, true},
		{"unchanged messages", 1000, 1000, true},
		{"larger messages", 1000, 1100, true},
		{"missing message sizes", 0, 0, false},
		{"empty original messages", 0, 10, false},
		{"missing compressed size", 1000, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := &HeadroomStats{TokensSaved: 500}
			diag := &HeadroomDiagnostics{
				Before: HeadroomSizes{BodyBytes: 100000, MessageBytes: tt.beforeMsg},
				After:  HeadroomSizes{BodyBytes: 99500, MessageBytes: tt.afterMsg},
			}
			got := IsHeadroomPhantomSavings(stats, diag)
			if got != tt.wantPhantom {
				t.Errorf("IsHeadroomPhantomSavings() = %v, want %v", got, tt.wantPhantom)
			}
		})
	}
}

func TestIsHeadroomPhantomSavings_NoPositiveTokensSaved(t *testing.T) {
	diag := &HeadroomDiagnostics{
		Before: HeadroomSizes{BodyBytes: 100000, MessageBytes: 1000},
		After:  HeadroomSizes{BodyBytes: 100000, MessageBytes: 1000},
	}

	for _, stats := range []*HeadroomStats{nil, {}, {TokensSaved: 0}, {TokensSaved: -1}} {
		if got := IsHeadroomPhantomSavings(stats, diag); got {
			t.Errorf("IsHeadroomPhantomSavings(%v) = true, want false", stats)
		}
	}
}

func TestIsHeadroomPhantomSavings_MinShrinkRatioThreshold(t *testing.T) {
	diag := &HeadroomDiagnostics{
		Before: HeadroomSizes{BodyBytes: 100000, MessageBytes: 1000},
		After:  HeadroomSizes{BodyBytes: 99900, MessageBytes: 900}, // 10% shrink
	}
	stats := &HeadroomStats{TokensSaved: 100}

	// Default 5%: 10% >= 5%, so not phantom
	if got := IsHeadroomPhantomSavings(stats, diag); got {
		t.Errorf("expected false with default 0.05 threshold, got %v", got)
	}

	// Custom 20%: 10% < 20%, so phantom
	if got := IsHeadroomPhantomSavings(stats, diag, 0.20); !got {
		t.Errorf("expected true with 0.20 threshold, got %v", got)
	}
}

func TestIsHeadroomPhantomSavings_ClaudeSystemPromptSavings(t *testing.T) {
	// Top-level system prompt compression for Claude:
	// bodyBytes shrinks by 50%, while messageBytes does not shrink.
	stats := &HeadroomStats{TokensSaved: 500}
	diag := &HeadroomDiagnostics{
		Before: HeadroomSizes{BodyBytes: 100000, MessageBytes: 1000},
		After:  HeadroomSizes{BodyBytes: 50000, MessageBytes: 1000},
	}
	if got := IsHeadroomPhantomSavings(stats, diag); got {
		t.Errorf("expected Claude system prompt savings not to be flagged as phantom, got %v", got)
	}
}

func TestHeadroomPhantomWarning_Text(t *testing.T) {
	diag := &HeadroomDiagnostics{
		Before: HeadroomSizes{BodyBytes: 100000, MessageBytes: 1000},
		After:  HeadroomSizes{BodyBytes: 99500, MessageBytes: 990},
	}
	warning := FormatHeadroomPhantomWarning(diag)
	if !strings.Contains(warning, "reported token delta, but messages and outbound JSON shrank <5%") {
		t.Errorf("expected updated warning text in %q", warning)
	}
	if strings.Contains(warning, "reported token delta, but outbound JSON shrank <5%;") {
		t.Errorf("warning still contains obsolete text: %q", warning)
	}
}
