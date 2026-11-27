package media

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

func (h *MediaHandler) forwardMediaRequest(w http.ResponseWriter, r *http.Request, body []byte, defaultModel, endpoint string) {
	if handlerutil.GetServiceKind(r.Context()) == "" {
		r = r.WithContext(handlerutil.WithServiceKind(r.Context(), kindFromEndpoint(endpoint)))
	}
	model := defaultModel
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err == nil && reqBody.Model != "" {
		model = reqBody.Model
	} else if isMultipartRequest(r) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && params["boundary"] != "" {
			mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				if p.FormName() == "model" {
					val, _ := io.ReadAll(p)
					if len(val) > 0 {
						model = strings.TrimSpace(string(val))
					}
					break
				}
			}
		}
	}
	if (endpoint == "/v1/search" || endpoint == "/v1/scrape") && (model == "search" || model == "default" || model == "web" || model == "webSearch") {
		model = h.defaultWebSearchModel()
	}
	if endpoint == "/v1/web/fetch" && (model == "fetch" || model == "default" || model == "web" || model == "webFetch") {
		model = h.defaultWebFetchModel()
	}
	// Per-API-key access control. This is the single funnel for images, TTS,
	// STT, video, search, scrape and fetch, so gating here covers all of them
	// with the same decision the chat lane uses. It runs before any connection
	// selection so a denied model never reaches a provider.
	//
	// It also runs before the media caps. A cap answers 413 with the limit and
	// the measured figure; sent to a key that may not call the model at all,
	// that is a policy detail the key has no business learning — the answer it
	// is owed is 403.
	if !h.ChatH.EnforceModelAccess(w, r, model) {
		return
	}
	// Media input caps and request-level usage, at the one funnel every media
	// lane passes through. This still sits before model resolution and before
	// connection selection, which is what matters: an oversized request never
	// reaches an upstream call, because the call is what gets billed.
	if _, refused := guardMediaInput(w, r.Header.Get("Content-Type"), body); refused {
		return
	}
	modelInfo, err := h.ChatH.ResolveModel(model)
	if err != nil {
		log.Warn("media", "resolve model failed", "endpoint", endpoint, "model", model, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Debug("media", "forward request", "endpoint", endpoint, "model", model, "provider", modelInfo.Provider, "resolvedModel", modelInfo.Model)

	// Handle combo fallback if model is a combo
	if len(modelInfo.ComboModels) > 0 {
		var lastErr string
		lastStatus := http.StatusBadGateway
		for _, entry := range modelInfo.ComboModels {
			subInfo, err := h.ChatH.ResolveModel(entry)
			if err != nil {
				continue
			}
			if endpoint == "/v1/search" && subInfo.Provider == "antigravity" {
				if err := h.handleAntigravitySearch(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr, lastStatus = searchErrorParts(err)
					continue
				}
			}
			if endpoint == "/v1/search" && (subInfo.Provider == "xquik" || subInfo.Provider == "xquik-search") {
				if err := h.handleXquikSearch(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr, lastStatus = searchErrorParts(err)
					continue
				}
			}
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (subInfo.Provider == "antigravity" || subInfo.Provider == "ag") {
				if err := h.handleAntigravityImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// Gemini image models answer the native generateContent shape, not
			// the OpenAI images endpoint the generic forward would dial.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (subInfo.Provider == "gemini" || subInfo.Provider == "gm") {
				if err := h.handleGeminiImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// Codex image models are Responses-API models, not an image endpoint.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (subInfo.Provider == "codex" || subInfo.Provider == "cx") {
				if err := h.handleCodexImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// Cloudflare AI image models answer /ai/run/<model>, which the
			// generic forwarder cannot reach — dedicated lane for all of them.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && isCloudflareImageModel(subInfo) {
				if err := h.handleCloudflareImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}

			if (endpoint == "/v1/audio/transcriptions" || endpoint == "/audio/transcriptions") && (subInfo.Provider == "antigravity" || subInfo.Provider == "ag") {
				if err := h.handleAntigravitySTT(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// ElevenLabs Scribe: the vendor rejects the generic OpenAI-compatible
			// multipart body (model_id / language_code / xi-api-key).
			if (endpoint == "/v1/audio/transcriptions" || endpoint == "/audio/transcriptions") && (subInfo.Provider == "elevenlabs" || subInfo.Provider == "el") {
				if err := h.handleElevenLabsSTT(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}

			// Realtime STT: dispatched on the model's transport marker, never on a
			// hardcoded id, so a new streaming provider extends through data.
			if isTranscriptionEndpoint(endpoint) &&
				providers.ResolveModelTransport(subInfo.Provider, subInfo.Model) == providers.TransportGeminiLive {
				if err := h.handleGeminiLiveSTT(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}

			conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), subInfo.Provider, subInfo.ConnectionID, nil, subInfo.Model)
			if err != nil || conn == nil {
				lastErr = fmt.Sprintf("no connection for %s", subInfo.Provider)
				continue
			}
			providerCfg, err := h.ChatH.GetProviderConfig(subInfo.Provider, connData)
			if err != nil {
				lastErr = err.Error()
				continue
			}
			apiKey := chat.ExtractAPIKey(connData)
			if apiKey == "" {
				lastErr = "no API key"
				continue
			}

			finalBody := handlerutil.UpdateModelInBody(body, subInfo.Model)
			if isVideoCreateEndpoint(endpoint) && isXAIVideoProvider(subInfo.Provider) {
				finalBody = formatXAIVideoPayload(finalBody)
			}
			// Cloudflare AI's flux-1-schnell schema rejects width/height — handled
			// by the cloudflare image lane, not here.

			// The endpoint the caller used is a route on this gateway, not a
			// path on the vendor's host: appending it to BaseURL built URLs the
			// vendor never serves (tavily got api.tavily.com/search/v1/search,
			// serper got google.serper.dev/v1/search). Each vendor publishes its
			// own search/fetch URL and verb in the registry.
			targetURL := strings.TrimRight(providerCfg.BaseURL, "/") + endpoint
			method := r.Method
			switch endpoint {
			case "/v1/search":
				targetURL, err = resolveSearchURL(providerCfg, connData.ProviderSpecificData, targetURL)
				if err != nil {
					lastErr = err.Error()
					continue
				}
				method = strings.ToUpper(providerCfg.SearchMethod)
				if method == "" {
					method = http.MethodPost
				}
			case "/v1/web/fetch":
				if providerCfg.FetchURL != "" {
					targetURL = providerCfg.FetchURL
					if providerCfg.FetchMethod != "" {
						method = providerCfg.FetchMethod
					} else {
						method = "POST"
					}
					// Ollama cloud expects {"url": "https://..."} only
					if subInfo.Provider == "ollama" {
						var tmp struct {
							URL string `json:"url"`
						}
						if err := json.Unmarshal(body, &tmp); err == nil && tmp.URL != "" {
							if b, err := json.Marshal(map[string]string{"url": tmp.URL}); err == nil {
								finalBody = b
							}
						}
					}
				}
			}
			req, err := http.NewRequestWithContext(r.Context(), method, targetURL, bytes.NewReader(finalBody))
			if err != nil {
				lastErr = err.Error()
				continue
			}
			for k, v := range r.Header {
				req.Header[k] = v
			}
			handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
			client, clientErr := h.ChatH.GetClientForConnection(connData)
			if clientErr != nil {
				lastErr = clientErr.Error()
				continue
			}
			// The client is the chat handler's shared one; guard its redirects
			// so a combo search that answers 302 to a private address is
			// refused rather than followed.
			client = guardMediaRedirects(client)
			resp, err := client.Do(req)
			if err != nil {
				log.Warn("media", "upstream combo request failed", "endpoint", endpoint, "provider", subInfo.Provider, "model", subInfo.Model, "conn", conn.ID[:min(8, len(conn.ID))], "error", err)
				// A cancelled caller is not a failed model. Trying the next
				// member of the combo would make a fresh upstream connection
				// for a client that has already gone.
				if r.Context().Err() != nil {
					handlerutil.WriteJSONError(w, 499, "client closed request")
					return
				}
				lastErr = err.Error()
				continue
			}
			if resp.StatusCode >= 400 {
				errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024))
				resp.Body.Close()
				errText := strings.TrimSpace(string(errBody))
				if errText == "" {
					errText = resp.Status
				}
				log.Warn("media", "upstream combo error", "endpoint", endpoint, "provider", subInfo.Provider, "model", subInfo.Model, "conn", conn.ID[:min(8, len(conn.ID))], "status", resp.StatusCode, "body", errText)
				// Upstream parity (videoGeneration CREATE_ROTATION_STATUSES):
				// billable video creation must not rotate on 5xx — the job
				// may already exist upstream. Auth/quota errors rotate.
				if isVideoCreateEndpoint(endpoint) && resp.StatusCode >= 500 {
					lastErr = fmt.Sprintf("upstream status %d: %s", resp.StatusCode, errText[:min(200, len(errText))])
					lastStatus = resp.StatusCode
					break
				}
				lastErr = fmt.Sprintf("upstream status %d: %s", resp.StatusCode, errText[:min(200, len(errText))])
				lastStatus = resp.StatusCode
				continue
			}
			defer func() { _ = resp.Body.Close() }()
			copyUpstreamHeaders(w.Header(), resp.Header)
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			h.Repo.UpdateConnectionLastUsed(conn.ID)
			return
		}
		handlerutil.WriteJSONError(w, lastStatus, fmt.Sprintf("all combo models failed: %s", lastErr))
		return
	}

	if endpoint == "/v1/search" && modelInfo.Provider == "antigravity" {
		if err := h.handleAntigravitySearch(w, r, body, modelInfo); err != nil {
			writeSearchError(w, err)
		}
		return
	}

	if endpoint == "/v1/search" && (modelInfo.Provider == "xquik" || modelInfo.Provider == "xquik-search") {
		if err := h.handleXquikSearch(w, r, body, modelInfo); err != nil {
			writeSearchError(w, err)
		}
		return
	}
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "antigravity" || modelInfo.Provider == "ag") {
		if err := h.handleAntigravityImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	// Gemini image models answer the native generateContent shape, not the
	// OpenAI images endpoint the generic forward would dial.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "gemini" || modelInfo.Provider == "gm") {
		if err := h.handleGeminiImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	// Codex image models are Responses-API models, not an image endpoint.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "codex" || modelInfo.Provider == "cx") {
		if err := h.handleCodexImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// Cloudflare AI image models answer /ai/run/<model>, which the generic
	// forwarder cannot reach — dedicated lane for all of them.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && isCloudflareImageModel(modelInfo) {
		if err := h.handleCloudflareImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	// UC web: the persona image API needs a minted Clerk session, not a bearer
	// key, so the generic forward cannot reach it.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "uc-web" || modelInfo.Provider == "uc") {
		if err := h.handleUcWebImage(w, r, body, modelInfo); err != nil {
			writeUcMediaError(w, err)
		}
		return
	}

	// UC web video: generation posts to the persona video API and polls the
	// result URL, so it shares the image lane's session plumbing.
	if (endpoint == "/v1/videos/generations" || endpoint == "/videos/generations") && (modelInfo.Provider == "uc-web" || modelInfo.Provider == "uc") {
		if err := h.handleUcWebVideo(w, r, body, modelInfo); err != nil {
			writeUcMediaError(w, err)
		}
		return
	}

	// veoaifree.com free tier: WordPress AJAX with a scraped CSRF nonce, not
	// an OpenAI-compatible endpoint.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "veoaifree-web" || modelInfo.Provider == "veo-free") {
		if err := h.handleVeoFreeWebImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	if (endpoint == "/v1/videos/generations" || endpoint == "/videos/generations") && (modelInfo.Provider == "veoaifree-web" || modelInfo.Provider == "veo-free") {
		if err := h.handleVeoFreeWebVideo(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// grok.com Imagine: the Cloudflare-gated conversations/new endpoint and
	// the assets.grok.com download live in the chat executor's transport, so
	// the lane delegates there (upstream imageProvider useExecutor: true).
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "grok-web" || modelInfo.Provider == "gw") {
		if err := h.handleGrokWebImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	if (endpoint == "/v1/videos/generations" || endpoint == "/videos/generations") && (modelInfo.Provider == "grok-web" || modelInfo.Provider == "gw") {
		if err := h.handleGrokWebVideo(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	if isTranscriptionEndpoint(endpoint) && (modelInfo.Provider == "antigravity" || modelInfo.Provider == "ag") {
		if err := h.handleAntigravitySTT(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// ElevenLabs Scribe: the vendor rejects the generic OpenAI-compatible
	// multipart body (model_id / language_code / xi-api-key).
	if isTranscriptionEndpoint(endpoint) && (modelInfo.Provider == "elevenlabs" || modelInfo.Provider == "el") {
		if err := h.handleElevenLabsSTT(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// Codex OAuth transcription: a fixed ChatGPT endpoint addressed with the
	// Codex account's own identity, so no connection URL override applies and
	// only the model "transcribe" is valid here.
	if isTranscriptionEndpoint(endpoint) && (modelInfo.Provider == "codex" || modelInfo.Provider == "cx") {
		if modelInfo.Model != codexSTTModel {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, codexSTTModelHintMsg)
			return
		}
		if err := h.handleCodexSTT(w, r, body, modelInfo); err != nil {
			var stErr *codexSTTError
			if errors.As(err, &stErr) {
				handlerutil.WriteJSONError(w, stErr.status, stErr.message)
			} else {
				handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
			}
		}
		return
	}

	// Realtime STT: dispatched on the model's transport marker, never on a
	// hardcoded id, so a new streaming provider extends through data.
	if isTranscriptionEndpoint(endpoint) &&
		providers.ResolveModelTransport(modelInfo.Provider, modelInfo.Model) == providers.TransportGeminiLive {
		if err := h.handleGeminiLiveSTT(w, r, body, modelInfo); err != nil {
			writeLiveSTTError(w, err)
		}
		return
	}

	preferredConnID := preferredMediaConnID(r, modelInfo)

	baseFinalBody := body
	if !isMultipartRequest(r) {
		baseFinalBody = handlerutil.UpdateModelInBody(body, modelInfo.Model)
	} else if model != "" && model != modelInfo.Model {
		// Upstream parity (videoGeneration byte-exact multipart): never
		// rewrite inside the binary payload — a byte.Replace could corrupt
		// file bytes that happen to match. Rebuild only the model part.
		if rebuilt, err := rewriteMultipartModelPart(body, r.Header.Get("Content-Type"), model, modelInfo.Model); err == nil {
			baseFinalBody = rebuilt
		} else {
			log.Warn("media", "multipart model rewrite skipped", "endpoint", endpoint, "error", err)
		}
	}
	if isVideoCreateEndpoint(endpoint) && isXAIVideoProvider(modelInfo.Provider) && !isMultipartRequest(r) {
		baseFinalBody = formatXAIVideoPayload(baseFinalBody)
	}

	usePinned := preferredConnID != ""
	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr *mediaForwardAttemptErr

	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadGateway, r.Context().Err().Error())
			return
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, preferredConnID, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if len(excludeIDs) == 0 {
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					apiKey := cfg.DefaultAPIKey
					if apiKey == "" {
						apiKey = "public"
					}
					conn = nil
					connData = &chat.ConnectionData{
						APIKey:      apiKey,
						ProxyPoolID: h.ChatH.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				}
			}
			if connData == nil {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				log.Warn("media", "no connection", "endpoint", endpoint, "provider", modelInfo.Provider, "model", modelInfo.Model, "error", err)
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
				return
			}
		}
		if conn != nil {
			if _, seen := tried[conn.ID]; seen {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(nil, modelInfo.Provider))
				return
			}
			tried[conn.ID] = struct{}{}
		}

		attemptErr := h.tryMediaForwardConn(w, r, body, baseFinalBody, modelInfo, endpoint, conn, connData)
		if attemptErr == nil {
			return
		}
		lastErr = attemptErr
		if conn == nil || usePinned || !attemptErr.retryable || r.Context().Err() != nil {
			lastErr.write(w)
			return
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}
