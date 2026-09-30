package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

type wantMedia struct {
	mime string
	data string
}

// A tool result that carries binary must not be reduced to a string: Gemini
// only lets the model look at bytes that arrive as an inlineData part in the
// same user turn as the functionResponse. Before the fix the tool branch read
// the result with extractContentString, which concatenates text blocks and
// discards every image, so a screenshot tool returned a blind model.
func TestOpenAIToGemini_ToolResultMediaParts(t *testing.T) {
	tests := []struct {
		name         string
		toolContent  string
		wantResult   string
		wantInline   []wantMedia
		wantFileURI  string
		wantWire     string
		wantNoInline bool
	}{
		{
			name:         "text-only result stays a lone functionResponse",
			toolContent:  `"captured"`,
			wantResult:   `{"output":"captured"}`,
			wantNoInline: true,
		},
		{
			name:         "text block array alone stays a lone functionResponse",
			toolContent:  `[{"type":"text","text":"captured"}]`,
			wantResult:   `{"output":"captured"}`,
			wantNoInline: true,
		},
		{
			name:        "image-only result keeps the picture",
			toolContent: `[{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]`,
			wantResult:  `{"output":""}`,
			wantInline:  []wantMedia{{"image/png", "QUJD"}},
			wantWire:    `"inlineData":{"mimeType":"image/png","data":"QUJD"}`,
		},
		{
			name:        "mixed text and image result keeps both",
			toolContent: `[{"type":"text","text":"captured"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]`,
			wantResult:  `{"output":"captured"}`,
			wantInline:  []wantMedia{{"image/png", "QUJD"}},
			wantWire:    `"inlineData":{"mimeType":"image/png","data":"QUJD"}`,
		},
		{
			name:        "pdf tool output is inlined",
			toolContent: `[{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBER"}}]`,
			wantResult:  `{"output":""}`,
			wantInline:  []wantMedia{{"application/pdf", "JVBER"}},
			wantWire:    `"inlineData":{"mimeType":"application/pdf","data":"JVBER"}`,
		},
		{
			name:        "remote image travels as fileData rather than inlined",
			toolContent: `[{"type":"text","text":"shot"},{"type":"image_url","image_url":{"url":"https://example.com/shot.png"}}]`,
			wantResult:  `{"output":"shot"}`,
			wantFileURI: "https://example.com/shot.png",
			wantWire:    `"fileUri":"https://example.com/shot.png"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{
				"model": "gemini-2.5-flash",
				"messages": [
					{"role": "user", "content": "screenshot the page"},
					{"role": "assistant", "tool_calls": [
						{"id":"call_1","type":"function","function":{"name":"browser_screenshot","arguments":"{}"}}
					]},
					{"role": "tool", "tool_call_id": "call_1", "content": ` + tt.toolContent + `}
				]
			}`)

			out, err := TranslateOpenAIToGemini(body)
			if err != nil {
				t.Fatalf("TranslateOpenAIToGemini: %v", err)
			}

			var req GeminiRequest
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatalf("unmarshal gemini request: %v", err)
			}
			if len(req.Contents) == 0 {
				t.Fatal("no contents in gemini request")
			}

			// The tool result is the trailing user turn; find it rather than
			// pinning an index that normalization is free to move.
			var parts []GeminiPart
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil {
						parts = c.Parts
					}
				}
			}
			if parts == nil {
				t.Fatalf("no functionResponse part found in %s", out)
			}

			resp := parts[0].FunctionResponse
			if resp.Name != "browser_screenshot" {
				t.Errorf("functionResponse name = %q, want browser_screenshot", resp.Name)
			}
			if resp.Response == nil {
				t.Fatalf("functionResponse carries no response payload: %s", out)
			}
			result, err := json.Marshal(resp.Response.Result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			if got := string(result); got != tt.wantResult {
				t.Errorf("functionResponse.result = %s, want %s", got, tt.wantResult)
			}

			wantCount := 1 + len(tt.wantInline)
			if tt.wantFileURI != "" {
				wantCount++
			}
			if len(parts) != wantCount {
				t.Fatalf("tool turn has %d parts, want %d: %s", len(parts), wantCount, out)
			}

			for i, want := range tt.wantInline {
				part := parts[1+i]
				if part.InlineData == nil {
					t.Fatalf("part %d is not inlineData: %+v", 1+i, part)
				}
				if part.InlineData.MimeType != want.mime {
					t.Errorf("part %d mimeType = %q, want %q", 1+i, part.InlineData.MimeType, want.mime)
				}
				if part.InlineData.Data != want.data {
					t.Errorf("part %d data = %q, want %q", 1+i, part.InlineData.Data, want.data)
				}
			}

			if tt.wantFileURI != "" {
				last := parts[len(parts)-1]
				if last.FileData == nil {
					t.Fatalf("last part is not fileData: %+v", last)
				}
				if last.FileData.FileUri != tt.wantFileURI {
					t.Errorf("fileUri = %q, want %q", last.FileData.FileUri, tt.wantFileURI)
				}
			}

			// The bytes Gemini actually receives must carry the picture, not
			// merely an intermediate struct field that marshalling drops.
			if tt.wantWire != "" && !strings.Contains(string(out), tt.wantWire) {
				t.Errorf("outbound gemini payload missing %s: %s", tt.wantWire, out)
			}
			if tt.wantNoInline && strings.Contains(string(out), `"inlineData"`) {
				t.Errorf("text-only tool result grew an inlineData part: %s", out)
			}
		})
	}
}
