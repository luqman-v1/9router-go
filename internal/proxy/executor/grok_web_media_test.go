package executor

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"
)

func TestGrokFindVideoURL(t *testing.T) {
	// Direct nested hit.
	if got := grokFindVideoURL(`{"result":{"response":{"cardAttachment":{"jsonData":"{\"videoUrl\":\"https://assets.grok.com/v/abc.mp4\"}"}}}}`); got != "https://assets.grok.com/v/abc.mp4" {
		t.Errorf("nested stringified hit = %q", got)
	}
	// The last hit wins.
	two := strings.Join([]string{
		`{"a":{"u":"https://x/one.mp4"}}`,
		`{"b":{"u":"https://x/two.mp4"}}`,
	}, "\n")
	if got := grokFindVideoURL(two); got != "https://x/two.mp4" {
		t.Errorf("last hit = %q", got)
	}
	if got := grokFindVideoURL(`{"nothing":"here"}`); got != "" {
		t.Errorf("no-hit = %q", got)
	}
}

func TestGenerateGrokMediaImage(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0}
	card := `{"image_chunk":{"progress":100,"imageUrl":"/img/final.jpg","mimeType":"image/jpeg"}}`
	ndjson := strings.Join([]string{
		`{"result":{"response":{"cardAttachment":{"jsonData":"{\"image_chunk\":{\"progress\":42,\"imageUrl\":\"/img/partial.jpg\"}"}}}}}`,
		`{"result":{"response":{"cardAttachment":{"jsonData":"` + strings.ReplaceAll(card, `"`, `\"`) + `"}}}}`,
	}, "\n")

	var seenPayload map[string]any
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/img/") {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpeg)
			return
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		_ = json.Unmarshal(body, &seenPayload)
		_, _ = w.Write([]byte(ndjson))
	}))
	defer site.Close()
	prevURL, prevAssets, prevFactory := grokWebChatURL, grokAssetsBase, grokTransportFactory
	grokWebChatURL, grokAssetsBase = site.URL, site.URL
	grokTransportFactory = func(*http.Client, string) (grokDoer, error) { return &grokPlainDoer{}, nil }
	t.Cleanup(func() { grokWebChatURL, grokAssetsBase, grokTransportFactory = prevURL, prevAssets, prevFactory })

	results, err := GenerateGrokMedia(context.Background(), nil, "sso=abc", "a cat", "image", "1:1", 0, 1)
	if err != nil {
		t.Fatalf("GenerateGrokMedia: %v", err)
	}
	if len(results) != 1 || results[0].Mime != "image/jpeg" {
		t.Fatalf("results = %+v", results)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(results[0].B64); string(decoded) != string(jpeg) {
		t.Error("asset bytes mismatch")
	}
	if seenPayload["modelName"] != "grok-4-1-thinking-1129" {
		t.Errorf("image payload modelName = %v", seenPayload["modelName"])
	}
	if enable, _ := seenPayload["enableImageGeneration"].(bool); !enable {
		t.Error("image turn must enable image generation")
	}
	if msg, _ := seenPayload["message"].(string); msg != "Generate an image (1:1): a cat" {
		t.Errorf("image prompt = %q", msg)
	}
}

func TestGenerateGrokMediaVideo(t *testing.T) {
	mp4 := append([]byte("aaaaftyp"), make([]byte, 8)...)
	cardData, _ := json.Marshal(map[string]any{"render": map[string]any{"url": "/v/final.mp4"}})
	quoted, _ := json.Marshal(string(cardData))
	ndjson := `{"result":{"response":{"cardAttachment":{"jsonData":` + string(quoted) + `}}}}`
	var seenPayload map[string]any
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v/") {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4)
			return
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		_ = json.Unmarshal(body, &seenPayload)
		_, _ = w.Write([]byte(ndjson))
	}))
	defer site.Close()
	prevURL, prevAssets, prevFactory := grokWebChatURL, grokAssetsBase, grokTransportFactory
	grokWebChatURL, grokAssetsBase = site.URL, site.URL
	grokTransportFactory = func(*http.Client, string) (grokDoer, error) { return &grokPlainDoer{}, nil }
	t.Cleanup(func() { grokWebChatURL, grokAssetsBase, grokTransportFactory = prevURL, prevAssets, prevFactory })

	results, err := GenerateGrokMedia(context.Background(), nil, "sso=abc", "a dance", "video", "9:16", 8, 3)
	if err != nil {
		t.Fatalf("GenerateGrokMedia: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("video must never loop, results = %d", len(results))
	}
	if results[0].Mime != "video/mp4" {
		t.Errorf("mime = %q", results[0].Mime)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(results[0].B64); string(decoded) != string(mp4) {
		t.Error("asset bytes mismatch")
	}
	if seenPayload["modelName"] != "imagine-video-gen" || seenPayload["kind"] != "CONVERSATION_KIND_IMAGINE" {
		t.Errorf("video payload = %v", seenPayload)
	}
	mediaGen, _ := seenPayload["mediaGenInput"].(map[string]any)
	t2v, _ := mediaGen["textToVideo"].(map[string]any)
	if t2v["aspectRatio"] != "9:16" || t2v["duration"] != float64(8) || t2v["resolutionName"] != "480p" {
		t.Errorf("textToVideo = %v", t2v)
	}
	if msg, _ := seenPayload["message"].(string); msg != "a dance --mode=custom" {
		t.Errorf("video message = %q", msg)
	}
}

func TestGenerateGrokMediaGuards(t *testing.T) {
	t.Run("no cookie", func(t *testing.T) {
		if _, err := GenerateGrokMedia(context.Background(), nil, "", "x", "image", "", 0, 1); err == nil {
			t.Fatal("cookie is required")
		}
	})
	t.Run("no prompt", func(t *testing.T) {
		if _, err := GenerateGrokMedia(context.Background(), nil, "sso=abc", "  ", "image", "", 0, 1); err == nil {
			t.Fatal("prompt is required")
		}
	})
	t.Run("unsupported kind", func(t *testing.T) {
		if _, err := GenerateGrokMedia(context.Background(), nil, "sso=abc", "x", "audio", "", 0, 1); err == nil {
			t.Fatal("audio is refused")
		}
	})
	t.Run("moderated image turn", func(t *testing.T) {
		site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"result":{"response":{"token":"cannot do that"}}}`))
		}))
		defer site.Close()
		prevURL, prevFactory := grokWebChatURL, grokTransportFactory
		grokWebChatURL = site.URL
		grokTransportFactory = func(*http.Client, string) (grokDoer, error) { return &grokPlainDoer{}, nil }
		t.Cleanup(func() { grokWebChatURL, grokTransportFactory = prevURL, prevFactory })
		if _, err := GenerateGrokMedia(context.Background(), nil, "sso=abc", "x", "image", "", 0, 1); err == nil ||
			!strings.Contains(err.Error(), "no completed image chunk") {
			t.Fatalf("err = %v, want no-completed-image-chunk", err)
		}
	})
}
