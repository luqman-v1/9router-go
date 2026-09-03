package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
)

// inlineImageMime maps Kiro image format to MIME type.
var inlineImageMime = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"jpg":  "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
}

// HandleKiro intercepts Kiro AWS EventStream requests and forwards to 9router.
// It preserves inline images in OpenAI MITM and removes redundant systemPrompt.
func HandleKiro(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		SendError(w, http.StatusBadRequest, "invalid Kiro request body")
		return
	}

	// Fix(kiro): remove redundant top-level systemPrompt field (decolua/9router #12)
	delete(reqBody, "systemPrompt")

	// Fix(kiro): preserve inline images in OpenAI MITM (userInputMessage.images -> image_url parts)
	if uim, ok := reqBody["userInputMessage"].(map[string]any); ok {
		if imagesRaw, ok := uim["images"].([]any); ok && len(imagesRaw) > 0 {
			var imageParts []any
			for _, imgRaw := range imagesRaw {
				img, ok := imgRaw.(map[string]any)
				if !ok {
					continue
				}
				format, _ := img["format"].(string)
				mime, ok := inlineImageMime[strings.ToLower(format)]
				if !ok {
					continue
				}
				source, ok := img["source"].(map[string]any)
				if !ok {
					continue
				}
				b, _ := source["bytes"].(string)
				if b == "" {
					continue
				}
				imageParts = append(imageParts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + mime + ";base64," + b,
					},
				})
			}
			if len(imageParts) > 0 {
				// Convert to OpenAI content array: text + images
				text, _ := uim["content"].(string)
				text = strings.TrimSpace(text)
				var content []any
				if text != "" {
					content = append(content, map[string]any{"type": "text", "text": text})
				}
				content = append(content, imageParts...)
				// If the request already has messages, merge image content into messages for OpenAI forwarding;
				// otherwise create a synthetic messages field so the image reaches the translator.
				if _, hasMessages := reqBody["messages"]; !hasMessages {
					// Kiro CodeWhisperer style payload — synthesize OpenAI messages for the router
					reqBody["messages"] = []any{
						map[string]any{"role": "user", "content": content},
					}
				} else if msgs, ok := reqBody["messages"].([]any); ok {
					// Append image parts to last user message if possible
					if len(msgs) > 0 {
						if last, ok := msgs[len(msgs)-1].(map[string]any); ok {
							if role, _ := last["role"].(string); role == "user" {
								// Merge into existing content
								var existing []any
								switch c := last["content"].(type) {
								case string:
									if strings.TrimSpace(c) != "" {
										existing = append(existing, map[string]any{"type": "text", "text": c})
									}
								case []any:
									existing = c
								}
								existing = append(existing, imageParts...)
								last["content"] = existing
								msgs[len(msgs)-1] = last
								reqBody["messages"] = msgs
							} else {
								reqBody["messages"] = append(msgs, map[string]any{"role": "user", "content": content})
							}
						}
					}
				}
				// Clean up original images to avoid duplicate handling downstream
				delete(uim, "images")
				reqBody["userInputMessage"] = uim
			}
		}
	}

	model, _ := reqBody["model"].(string)
	if model != "" && !strings.Contains(model, "/") {
		reqBody["model"] = "kiro/" + model
	}
	forwardBody, err := json.Marshal(reqBody)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "failed to marshal Kiro request")
		return
	}

	upstream, err := FetchRouter(r.Context(), forwardBody, "/v1/chat/completions", r.Header, "")
	if err != nil {
		SendError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer upstream.Body.Close()

	if upstream.Header.Get("Content-Type") == "text/event-stream" {
		PipeSSE(upstream, w)
	} else {
		PipeJSON(upstream, w)
	}
}
