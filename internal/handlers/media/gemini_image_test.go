package media

import (
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"
)

func TestResolveGeminiImageInput(t *testing.T) {
	dataURI := "data:image/webp;base64,QUJD"
	part := resolveGeminiImageInput(dataURI)
	if part == nil {
		t.Fatal("data URI rejected")
	}
	inline := part["inlineData"].(map[string]string)
	if inline["mimeType"] != "image/webp" || inline["data"] != "QUJD" {
		t.Errorf("inlineData = %v", inline)
	}

	bare := strings.Repeat("QUJDREVG", 20)
	part = resolveGeminiImageInput(bare)
	if part == nil {
		t.Fatal("bare base64 rejected")
	}
	if part["inlineData"].(map[string]string)["mimeType"] != "image/png" {
		t.Errorf("bare base64 mime = %v", part)
	}

	for _, rejected := range []string{"", "https://example.com/x.png", "short"} {
		if resolveGeminiImageInput(rejected) != nil {
			t.Errorf("input %q should not become a part", rejected)
		}
	}
}

func TestGeminiQuotaResetSeconds(t *testing.T) {
	future := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	body := []byte(`{"error":{"code":429,"details":[{"metadata":{"quotaResetTimeStamp":"` + future + `"}}]}}`)
	sec, ok := geminiQuotaResetSeconds(body)
	if !ok || sec < 60 || sec > 95 {
		t.Errorf("quota reset = %d/%v, want ~90s", sec, ok)
	}

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, ok := geminiQuotaResetSeconds([]byte(`{"error":{"details":[{"metadata":{"quotaResetTimeStamp":"` + past + `"}}]}}`)); ok {
		t.Error("past reset accepted")
	}
	if _, ok := geminiQuotaResetSeconds([]byte(`{"error":{}}`)); ok {
		t.Error("missing reset accepted")
	}
}

func TestGeminiImageResponseBody(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[
		{"text":"here you go"},
		{"inlineData":{"mimeType":"image/png","data":"QUJD"}}
	]}}]}`)
	out, err := geminiImageResponseBody(raw, "a cat")
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	data := m["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["b64_json"] != "QUJD" {
		t.Errorf("images = %v", data)
	}

	if _, err := geminiImageResponseBody([]byte(`{"candidates":[{"content":{"parts":[{"text":"no image"}]}}]}`), "p"); err == nil {
		t.Error("image-less response accepted")
	}
}
