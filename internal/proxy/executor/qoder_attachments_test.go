package executor

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
)

func TestQoderBuildMultipartFile(t *testing.T) {
	data := []byte("fake-png-bytes")
	boundary, body := qoderBuildMultipartFile(data, "photo.png", "image/png")

	if !strings.HasPrefix(boundary, "----9routerQoder") {
		t.Errorf("unexpected boundary: %s", boundary)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `Content-Disposition: form-data; name="file"; filename="photo.png"`) {
		t.Errorf("missing content-disposition header in body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Content-Type: image/png") {
		t.Errorf("missing content-type in body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "fake-png-bytes") {
		t.Errorf("missing data in body: %s", bodyStr)
	}
	if !strings.HasSuffix(bodyStr, "--"+boundary+"--\r\n") {
		t.Errorf("missing closing boundary in body: %s", bodyStr)
	}
}

func TestQoderExtractURLFromUploadResponse(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "imageUrls array in result",
			response: `{"result":{"imageUrls":["https://oss.qoder.sh/img1.png"]}}`,
			want:     "https://oss.qoder.sh/img1.png",
		},
		{
			name:     "image_url string in result",
			response: `{"result":{"image_url":"https://oss.qoder.sh/img2.jpg"}}`,
			want:     "https://oss.qoder.sh/img2.jpg",
		},
		{
			name:     "top-level imageUrl",
			response: `{"imageUrl":"https://oss.qoder.sh/img3.webp"}`,
			want:     "https://oss.qoder.sh/img3.webp",
		},
		{
			name:     "nested escaped body string",
			response: `{"body":"{\"result\":{\"url\":\"https://oss.qoder.sh/img4.png\"}}"}`,
			want:     "https://oss.qoder.sh/img4.png",
		},
		{
			name:     "empty response",
			response: `{}`,
			want:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := qoderExtractURLFromUploadResponse([]byte(tc.response))
			if got != tc.want {
				t.Errorf("qoderExtractURLFromUploadResponse() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteQoderMessageAttachments_DataURIToOSS(t *testing.T) {
	rawImg := []byte("pretend-png-data")
	b64 := base64.StdEncoding.EncodeToString(rawImg)
	dataURI := "data:image/png;base64," + b64

	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Check this image:",
				},
				map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": dataURI,
					},
				},
			},
		},
	}

	uploadCalls := 0
	mockUpload := func(ctx context.Context, buffer []byte, mediaType string) (string, error) {
		uploadCalls++
		if mediaType != "image/png" {
			t.Errorf("mediaType = %q, want image/png", mediaType)
		}
		if string(buffer) != string(rawImg) {
			t.Errorf("buffer mismatch: got %q, want %q", string(buffer), string(rawImg))
		}
		return "https://oss.qoder.sh/uploaded-pic.png", nil
	}

	creds := QoderCosyCreds{UserID: "u1", AuthToken: "dt-1"}
	stats, err := RewriteQoderMessageAttachments(context.Background(), messages, creds, QoderRegionIntl, nil, mockUpload)
	if err != nil {
		t.Fatalf("RewriteQoderMessageAttachments: %v", err)
	}

	if uploadCalls != 1 {
		t.Errorf("uploadCalls = %d, want 1", uploadCalls)
	}
	if stats.Uploaded != 1 {
		t.Errorf("stats.Uploaded = %d, want 1", stats.Uploaded)
	}

	// Verify the data URI was replaced with the OSS URL
	userMsg := messages[0].(map[string]any)
	blocks := userMsg["content"].([]any)
	imgBlock := blocks[1].(map[string]any)
	imgURLMap := imgBlock["image_url"].(map[string]any)
	if imgURLMap["url"] != "https://oss.qoder.sh/uploaded-pic.png" {
		t.Errorf("url = %q, want https://oss.qoder.sh/uploaded-pic.png", imgURLMap["url"])
	}
}

func TestRewriteQoderMessageAttachments_ClaudeImageBlock(t *testing.T) {
	rawImg := []byte("claude-image-bytes")
	b64 := base64.StdEncoding.EncodeToString(rawImg)

	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/jpeg",
						"data":       b64,
					},
				},
			},
		},
	}

	mockUpload := func(ctx context.Context, buffer []byte, mediaType string) (string, error) {
		return "https://oss.qoder.sh/claude-uploaded.jpg", nil
	}

	creds := QoderCosyCreds{UserID: "u1", AuthToken: "dt-1"}
	stats, err := RewriteQoderMessageAttachments(context.Background(), messages, creds, QoderRegionIntl, nil, mockUpload)
	if err != nil {
		t.Fatalf("RewriteQoderMessageAttachments: %v", err)
	}
	if stats.Uploaded != 1 {
		t.Errorf("stats.Uploaded = %d, want 1", stats.Uploaded)
	}

	userMsg := messages[0].(map[string]any)
	blocks := userMsg["content"].([]any)
	imgBlock := blocks[0].(map[string]any)
	if imgBlock["type"] != "image_url" {
		t.Errorf("block type = %v, want image_url", imgBlock["type"])
	}
	imgURLMap := imgBlock["image_url"].(map[string]any)
	if imgURLMap["url"] != "https://oss.qoder.sh/claude-uploaded.jpg" {
		t.Errorf("url = %q", imgURLMap["url"])
	}
}

func TestRewriteQoderMessageAttachments_OllamaImagesSidecar(t *testing.T) {
	messages := []any{
		map[string]any{
			"role":    "user",
			"content": "Look at these",
			"images":  []any{"https://example.com/one.png", "https://example.com/two.png"},
		},
	}

	creds := QoderCosyCreds{UserID: "u1", AuthToken: "dt-1"}
	stats, err := RewriteQoderMessageAttachments(context.Background(), messages, creds, QoderRegionIntl, nil, nil)
	if err != nil {
		t.Fatalf("RewriteQoderMessageAttachments: %v", err)
	}

	// Two image URLs collected
	if stats.Uploaded != 2 {
		t.Errorf("stats.Uploaded = %d, want 2", stats.Uploaded)
	}

	userMsg := messages[0].(map[string]any)
	if _, hasImages := userMsg["images"]; hasImages {
		t.Error("images sidecar was not deleted from message")
	}

	blocks, ok := userMsg["content"].([]any)
	if !ok || len(blocks) != 3 {
		t.Fatalf("content should have been expanded to 3 blocks, got: %v", userMsg["content"])
	}
}

func TestRewriteQoderMessageAttachments_UploadFailureFallsBackToInlineSmall(t *testing.T) {
	rawImg := []byte("tiny-image")
	b64 := base64.StdEncoding.EncodeToString(rawImg)
	dataURI := "data:image/png;base64," + b64

	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": dataURI,
					},
				},
			},
		},
	}

	mockUpload := func(ctx context.Context, buffer []byte, mediaType string) (string, error) {
		return "", fmt.Errorf("upload server unreachable")
	}

	creds := QoderCosyCreds{UserID: "u1", AuthToken: "dt-1"}
	_, err := RewriteQoderMessageAttachments(context.Background(), messages, creds, QoderRegionIntl, nil, mockUpload)
	if err != nil {
		t.Fatalf("RewriteQoderMessageAttachments: %v", err)
	}

	// Tiny image should be kept inline when upload fails
	userMsg := messages[0].(map[string]any)
	blocks := userMsg["content"].([]any)
	imgBlock := blocks[0].(map[string]any)
	imgURLMap := imgBlock["image_url"].(map[string]any)
	if imgURLMap["url"] != dataURI {
		t.Errorf("tiny image should stay inline on failure, got %v", imgURLMap["url"])
	}
}

func TestRewriteQoderMessageAttachments_UploadFailureStubsLarge(t *testing.T) {
	// Create an image larger than 512KB (qoderInlineFallbackMaxBytes)
	largeData := make([]byte, 600*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}
	b64 := base64.StdEncoding.EncodeToString(largeData)
	dataURI := "data:image/png;base64," + b64

	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": dataURI,
					},
				},
			},
		},
	}

	mockUpload := func(ctx context.Context, buffer []byte, mediaType string) (string, error) {
		return "", fmt.Errorf("network down")
	}

	creds := QoderCosyCreds{UserID: "u1", AuthToken: "dt-1"}
	stats, err := RewriteQoderMessageAttachments(context.Background(), messages, creds, QoderRegionIntl, nil, mockUpload)
	if err != nil {
		t.Fatalf("RewriteQoderMessageAttachments: %v", err)
	}
	if stats.Stubbed != 1 {
		t.Errorf("stats.Stubbed = %d, want 1", stats.Stubbed)
	}

	userMsg := messages[0].(map[string]any)
	blocks := userMsg["content"].([]any)
	textBlock := blocks[0].(map[string]any)
	if textBlock["type"] != "text" {
		t.Errorf("type = %v, want text", textBlock["type"])
	}
	text := textBlock["text"].(string)
	if !strings.Contains(text, "upload failed; not inlined") {
		t.Errorf("stub text mismatch: %s", text)
	}
}

func TestRewriteQoderMessageAttachments_NonImageFileStubbed(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "file",
					"file": map[string]any{
						"filename":  "manual.pdf",
						"file_data": "data:application/pdf;base64,cGRmYnl0ZXM=",
					},
				},
			},
		},
	}

	creds := QoderCosyCreds{UserID: "u1", AuthToken: "dt-1"}
	stats, err := RewriteQoderMessageAttachments(context.Background(), messages, creds, QoderRegionIntl, nil, nil)
	if err != nil {
		t.Fatalf("RewriteQoderMessageAttachments: %v", err)
	}
	if stats.Stubbed != 1 {
		t.Errorf("stats.Stubbed = %d, want 1", stats.Stubbed)
	}

	userMsg := messages[0].(map[string]any)
	blocks := userMsg["content"].([]any)
	textBlock := blocks[0].(map[string]any)
	text := textBlock["text"].(string)
	if !strings.Contains(text, "Qoder reads documents via its file API") {
		t.Errorf("expected file stub text, got: %s", text)
	}
}

func TestRewriteQoderMessageAttachments_PayloadCapStripsRemainingDataURIs(t *testing.T) {
	// Build a messages array that exceeds 6MB
	hugeData := make([]byte, 2*1024*1024)
	b64 := base64.StdEncoding.EncodeToString(hugeData)
	dataURI := "data:image/png;base64," + b64

	// 4 messages of ~2.6MB JSON each => ~10.4MB > 6MB cap
	messages := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURI}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURI}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURI}}}},
	}

	// No upload function => will try fallback to inline, but payload cap kicks in and strips them
	creds := QoderCosyCreds{UserID: "u1", AuthToken: "dt-1"}
	_, err := RewriteQoderMessageAttachments(context.Background(), messages, creds, QoderRegionIntl, nil, nil)
	if err != nil {
		t.Fatalf("RewriteQoderMessageAttachments: %v", err)
	}

	// Check that payloadBytes is now well under 6MB because data URIs were stripped
	raw, _ := json.Marshal(messages)
	if len(raw) > qoderMaxPayloadBytes {
		t.Errorf("payload length %d exceeds max %d", len(raw), qoderMaxPayloadBytes)
	}
}
