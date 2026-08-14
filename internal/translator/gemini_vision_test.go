package translator

import (
	"encoding/json"
	"testing"
)

func TestOpenAIToGemini_HttpImageUrl_FileData(t *testing.T) {
	reqJSON := `{
		"model": "gemini-2.5-flash",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "What is in this image?"},
					{"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}}
				]
			}
		]
	}`

	body, err := TranslateOpenAIToGemini([]byte(reqJSON))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	var geminiReq GeminiRequest
	if err := json.Unmarshal(body, &geminiReq); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}

	if len(geminiReq.Contents) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(geminiReq.Contents))
	}

	parts := geminiReq.Contents[0].Parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	if parts[0].Text != "What is in this image?" {
		t.Errorf("expected text part, got %s", parts[0].Text)
	}

	if parts[1].FileData == nil {
		t.Fatal("expected fileData for http image_url, got nil")
	}

	if parts[1].FileData.FileUri != "https://example.com/photo.jpg" {
		t.Errorf("expected fileUri https://example.com/photo.jpg, got %s", parts[1].FileData.FileUri)
	}
	if parts[1].FileData.MimeType != "image/*" {
		t.Errorf("expected mimeType image/*, got %s", parts[1].FileData.MimeType)
	}
}

func TestOpenAIToGemini_InputAudio(t *testing.T) {
	reqJSON := `{
		"model": "gemini-2.5-flash",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "input_audio", "input_audio": {"data": "dGVzdGF1ZGlv", "format": "mp3"}}
				]
			}
		]
	}`

	body, err := TranslateOpenAIToGemini([]byte(reqJSON))
	if err != nil {
		t.Fatalf("OpenAIToGemini failed: %v", err)
	}

	var geminiReq GeminiRequest
	if err := json.Unmarshal(body, &geminiReq); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}

	parts := geminiReq.Contents[0].Parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}

	if parts[0].InlineData == nil {
		t.Fatal("expected inlineData for input_audio, got nil")
	}

	if parts[0].InlineData.MimeType != "audio/mpeg" {
		t.Errorf("expected audio/mpeg, got %s", parts[0].InlineData.MimeType)
	}
	if parts[0].InlineData.Data != "dGVzdGF1ZGlv" {
		t.Errorf("expected dGVzdGF1ZGlv, got %s", parts[0].InlineData.Data)
	}
}
