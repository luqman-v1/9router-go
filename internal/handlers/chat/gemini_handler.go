package chat

import (
	"9router/proxy/internal/log"
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/proxy/oauth"
	"9router/proxy/internal/translator"
)

// forwardGeminiNativeRequest handles forwarding for gemini-native providers (antigravity).
func (h *ChatHandler) forwardGeminiNativeRequest(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	cfg *providers.ProviderConfig,
	apiKey string,
	connectionID string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
	// httpClient is the connection's client, proxy pool included.
	httpClient *http.Client,
) error {
	// Extract model
	var reqMeta struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqMeta); err != nil {
		log.Warn("gemini", "parse model failed", "error", err)
	}
	modelName := reqMeta.Model
	if modelName == "" {
		modelName = "gemini-3-flash"
	}

	// OAuth refresh for providers using Gemini-native format (antigravity, etc)
	var projectID string
	refreshedKey, pid, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
	if err != nil {
		log.Warn("gemini", "token refresh error", "conn", connectionID, "error", err)
	} else {
		apiKey = refreshedKey
		projectID = pid
	}

	if (provider == "antigravity" || provider == "gemini-cli") && projectID == "" && !projectProbeCached(connectionID) {
		pid, authFailed, noProject := fetchAntigravityProjectID(ctx, httpClient, apiKey)
		switch {
		case pid != "":
			projectID = pid
			h.storeAntigravityProjectID(connectionID, pid)
		case authFailed:
			// Upstream rejected the token (401/403) — refresh once and retry.
			// A missing/empty project is NOT an auth failure; refreshing there
			// only hammers the onboarding RPCs (the 429 source) without helping.
			log.Info("gemini", "force refresh OAuth token", "conn", connectionID)
			refreshedKey, pid2, err2 := h.forceRefreshOAuthToken(connectionID)
			if err2 == nil && refreshedKey != "" {
				apiKey = refreshedKey
				if pid2 != "" {
					projectID = pid2
					h.storeAntigravityProjectID(connectionID, pid2)
				} else if pid2, _, _ := fetchAntigravityProjectID(ctx, httpClient, apiKey); pid2 != "" {
					projectID = pid2
					h.storeAntigravityProjectID(connectionID, pid2)
				}
			}
		case noProject:
			// Google definitively said this token has no project. Cache it so
			// client retries stop re-probing the onboarding RPCs.
			cacheProjectMissing(connectionID)
		}
	}

	if projectID == "" {
		// antigravity has no OpenAI-compatible endpoint without a project ID — a
		// bare POST to cloudcode-pa.googleapis.com is a guaranteed 404. Bail with
		// an error so the fallback chain moves on instead of burning a request on
		// a dead lane.
		if provider == "antigravity" {
			return fmt.Errorf("antigravity: no project ID — onboard the account in Antigravity (antigravity.google) then re-login")
		}
		// gemini-cli keeps its OpenAI-style fallback.
		log.Info("gemini", "no projectID, fallback to OpenAI", "provider", provider)
		return h.forwardRequest(ctx, w, cfg, apiKey, body, isStream, translateResponse, metrics, httpClient)
	}

	resp, err := proxy.ForwardGemini(ctx, httpClient, cfg, apiKey, string(body), isStream, projectID, modelName)
	if err != nil {
		if uErr, ok := err.(*proxy.UpstreamError); ok {
			if uErr.StatusCode == http.StatusConflict || uErr.StatusCode == http.StatusTooManyRequests {
				if dur, ok := extractResetDuration(uErr.Body); ok {
					BlockAntigravityModelUntil(connectionID, modelName, time.Now().UTC().Add(dur))
				}
				HandleAntigravityQuotaError(AntigravityQuotaError{
					Ctx: ctx, Client: httpClient, ConnectionID: connectionID,
					Status: uErr.StatusCode, Model: modelName, AccessToken: apiKey,
					ProjectID: projectID, ErrorMessage: string(uErr.Body),
				})
			}
		}
		return fmt.Errorf("ForwardGemini (%s/%s): %w", provider, modelName, err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	// Handle response — antigravity wraps non-streaming response in {"response": {...}}
	// For streaming (SSE), the events are NOT wrapped — pipe directly.
	if projectID != "" && !isStream {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
		if err != nil {
			log.Error("gemini", "read response body failed", "error", err)
			return fmt.Errorf("read gemini response body: %w", err)
		}
		unwrapped := translator.UnwrapAntigravityResponse(raw)
		return h.handleGeminiNonStream(ctx, w, bytes.NewReader(unwrapped), translateResponse, metrics)
	}
	if isStream {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			return h.handleGeminiNonStream(ctx, w, resp.Body, translateResponse, metrics)
		}
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, provider)
		bodyCloser = stallReader
		return h.handleGeminiStream(ctx, w, stallReader, translateResponse, metrics)
	}
	return h.handleGeminiNonStream(ctx, w, resp.Body, translateResponse, metrics)
}

// StoreAntigravityProjectID persists a discovered Antigravity project ID on the
// connection so later requests skip the onboarding RPCs entirely.
func (h *ChatHandler) StoreAntigravityProjectID(connectionID, pid string) {
	h.storeAntigravityProjectID(connectionID, pid)
}

func (h *ChatHandler) storeAntigravityProjectID(connectionID, pid string) {
	if connectionID == "" || pid == "" || h.Repo == nil || h.Repo.RawDB() == nil {
		return
	}
	go func() {
		if h.Repo == nil || h.Repo.RawDB() == nil {
			return
		}
		if _, err := h.Repo.RawDB().Exec("UPDATE providerConnections SET data = json_set(data, '$.projectId', ?) WHERE id = ?", pid, connectionID); err != nil {
			log.Warn("gemini", "update projectId failed", "conn", connectionID, "error", err)
		}
	}()
}

type oauthTokenResult struct {
	token     string
	projectID string
	// passthrough marks a result carrying no new token — the connection was
	// healthy or unreadable. A waiter sharing the leader's flight must keep
	// its own token rather than adopting the leader's.
	passthrough bool
}

// RefreshOAuthTokenIfExpired checks and refreshes OAuth token for any provider with refreshToken.
func (h *ChatHandler) RefreshOAuthTokenIfExpired(connectionID, currentToken string) (string, string, error) {
	return h.refreshOAuthTokenIfExpired(connectionID, currentToken)
}

func (h *ChatHandler) refreshOAuthTokenIfExpired(connectionID, currentToken string) (string, string, error) {
	if connectionID == "" {
		return currentToken, "", nil
	}
	if h.Repo == nil || h.Repo.RawDB() == nil {
		return currentToken, "", nil
	}

	res, err, _ := h.oauthRefreshFlight.Do(connectionID, func() (any, error) {
		db := h.Repo.RawDB()
		row := db.QueryRow("SELECT provider, data FROM providerConnections WHERE id = ?", connectionID)
		var provider string
		var rawData string
		if err := row.Scan(&provider, &rawData); err != nil {
			return oauthTokenResult{token: currentToken, passthrough: true}, nil
		}

		var connMap map[string]any
		if err := json.Unmarshal([]byte(rawData), &connMap); err != nil {
			return oauthTokenResult{token: currentToken, passthrough: true}, nil
		}

		oauthData := providers.ParseOAuthConnection(connMap)
		projectID := ""
		if oauthData != nil {
			projectID = oauthData.ProjectID
		}
// The token is usable, so there is nothing to refresh. Hand back exactly
		// the credential the caller passed in, untouched: the stored accessToken
		// is not interchangeable with it, and the request path picks the right
		// one deliberately.
		//
		// for iflow the connection stores an HMAC platform key in apiKey and the
		// OAuth access token alongside it, and the request is signed with apiKey
		// (proxy/executor/providers.go) — substituting here would sign every
		// healthy connection with the wrong secret.
		//
		// Same for Kiro: upstream (open-sse/executors/kiro.js buildHeaders) uses
		// the apiKey for `authMethod: "api_key"` connections even when an
		// accessToken is present, and sending the other one answers 403.
		if oauthData == nil || oauthData.RefreshToken == "" || !oauthData.IsExpired() {
			return oauthTokenResult{token: currentToken, projectID: projectID, passthrough: true}, nil
		}

		// Try per-provider OAuth refresher first
		if refresher := oauth.Get(provider); refresher != nil {
			log.Info("oauth", "token expired, custom refresh", "provider", provider, "project", projectID)
			var connPSD map[string]any
			if raw, ok := connMap["providerSpecificData"].(map[string]any); ok {
				connPSD = raw
			}
			result, err := refresher(context.Background(), &oauth.Params{
				Client:               h.Client,
				Provider:             provider,
				RefreshToken:         oauthData.RefreshToken,
				AccessToken:          currentToken,
				ProviderSpecificData: oauth.StringMap(connPSD),
			})
			if err != nil {
				h.parkRejectedOAuthAccount(connectionID, err)
return oauthTokenResult{token: currentToken, projectID: projectID, passthrough: true},
					fmt.Errorf("OAuth refresh for %s: %w", provider, err)
			}
			if result == nil {
				// A refresher that reports success but hands back nothing is a
				// broken provider, not a reason to crash the gateway. The forced
				// path already guards this shape; without it BuildConnectionUpdate
				// dereferences nil, and singleflight re-panics that on every waiter.
				log.Error("oauth", "custom refresh returned no result", "provider", provider, "connection", connectionID)
				return oauthTokenResult{token: currentToken, projectID: projectID, passthrough: true},
					fmt.Errorf("OAuth refresh for %s: refresher returned no token", provider)
			}
			update := oauth.BuildConnectionUpdate(result)
			var existing map[string]any
			if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
				log.Error("oauth", "unmarshal connection data failed", "conn", connectionID, "error", err)
				existing = make(map[string]any)
			}
			for k, v := range update {
				existing[k] = v
			}
			if result.ProjectID != "" {
				existing["projectId"] = result.ProjectID
			}
			mergedJSON, err := json.Marshal(existing)
			if err != nil {
				log.Error("oauth", "marshal connection data failed", "conn", connectionID, "error", err)
			} else {
				db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
					string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
			}
			newPID := projectID
			if result.ProjectID != "" {
				newPID = result.ProjectID
			}
			log.Info("oauth", "token refreshed", "provider", provider, "project", newPID)
			h.clearOAuthAccountPark(connectionID)
			return oauthTokenResult{token: result.AccessToken, projectID: newPID}, nil
		}

		// Fall back to standard OAuth2
		cfg, ok := providers.KnownOAuthConfigs[provider]
		if !ok {
			return oauthTokenResult{token: currentToken, projectID: projectID, passthrough: true}, nil
		}

		log.Info("oauth", "token expired, standard refresh", "provider", provider, "project", projectID)
		tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
		if err != nil {
			h.parkRejectedOAuthAccount(connectionID, err)
			return oauthTokenResult{token: currentToken, projectID: projectID, passthrough: true}, fmt.Errorf("OAuth refresh for %s: %w", provider, err)
		}

		update := tokenResp.BuildConnectionUpdate()
		var existing map[string]any
		if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
			log.Error("oauth", "unmarshal connection data failed", "conn", connectionID, "error", err)
			existing = make(map[string]any)
		}
		for k, v := range update {
			existing[k] = v
		}
		mergedJSON, err := json.Marshal(existing)
		if err != nil {
			log.Error("oauth", "marshal connection data failed", "conn", connectionID, "error", err)
		} else {
			db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
				string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
		}

		h.clearOAuthAccountPark(connectionID)

		log.Info("oauth", "token refreshed", "provider", provider, "project", projectID)
		return oauthTokenResult{token: tokenResp.AccessToken, projectID: projectID}, nil
	})

// A shared flight must not hand one caller's token to another: when the
	// leader found the connection healthy, it returned its own token as a
	// pass-through, and that value belongs to the leader alone.
	if err != nil {
		r, _ := res.(oauthTokenResult)
		token := currentToken
		if !r.passthrough {
			token = r.token
		}
		return token, r.projectID, err
	}
	r, ok := res.(oauthTokenResult)
	if !ok {
		return currentToken, "", nil
	}
	if r.passthrough {
		return currentToken, r.projectID, nil
	}
	return r.token, r.projectID, nil
}

// forceRefreshOAuthToken unconditionally refreshes the OAuth token for a connection ID.
func (h *ChatHandler) forceRefreshOAuthToken(connectionID string) (string, string, error) {
	if connectionID == "" {
		return "", "", nil
	}
	if h.Repo == nil || h.Repo.RawDB() == nil {
		return "", "", fmt.Errorf("no database repository available")
	}

	// The "\x00" prefix cannot occur in a connection id, so a forced refresh
	// can never collide with the lazy flight of a connection named "force:<id>".
	res, err, _ := h.oauthRefreshFlight.Do("\x00force:"+connectionID, func() (any, error) {
		db := h.Repo.RawDB()
		row := db.QueryRow("SELECT provider, data FROM providerConnections WHERE id = ?", connectionID)
		var provider string
		var rawData string
		if err := row.Scan(&provider, &rawData); err != nil {
			return oauthTokenResult{}, err
		}

		var connMap map[string]any
		if err := json.Unmarshal([]byte(rawData), &connMap); err != nil {
			return oauthTokenResult{}, err
		}

		oauthData := providers.ParseOAuthConnection(connMap)
		if oauthData == nil || oauthData.RefreshToken == "" {
			return oauthTokenResult{}, fmt.Errorf("no refresh token available")
		}

		// Try per-provider OAuth refresher first
		if refresher := oauth.Get(provider); refresher != nil {
			log.Info("oauth", "force refresh", "provider", provider)
			var forcePSD map[string]any
			if raw, ok := connMap["providerSpecificData"].(map[string]any); ok {
				forcePSD = raw
			}
			result, err := refresher(context.Background(), &oauth.Params{
				Client:               h.Client,
				Provider:             provider,
				RefreshToken:         oauthData.RefreshToken,
				ProviderSpecificData: oauth.StringMap(forcePSD),
			})
			if err == nil && result != nil {
				var existing map[string]any
				if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
					log.Error("oauth", "unmarshal conn data failed", "conn", connectionID, "error", err)
					existing = make(map[string]any)
				}
				existing["accessToken"] = result.AccessToken
				if result.RefreshToken != "" {
					existing["refreshToken"] = result.RefreshToken
				}
				if result.ProjectID != "" {
					existing["projectId"] = result.ProjectID
				}
				existing["expiresAt"] = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339)
				mergedJSON, err := json.Marshal(existing)
				if err != nil {
					log.Error("oauth", "marshal conn data failed", "conn", connectionID, "error", err)
				} else {
					db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
						string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
				}
				newPID := oauthData.ProjectID
				if result.ProjectID != "" {
					newPID = result.ProjectID
				}
				h.clearOAuthAccountPark(connectionID)
				return oauthTokenResult{token: result.AccessToken, projectID: newPID}, nil
			}
			if err != nil {
				if h.parkRejectedOAuthAccount(connectionID, err) {
					return oauthTokenResult{}, fmt.Errorf("OAuth refresh for %s: %w", provider, err)
				}
				log.Warn("oauth", "custom refresh failed, trying standard", "provider", provider, "conn", connectionID, "error", err)
			}
		}

		// Fall back to standard OAuth2
		cfg, ok := providers.KnownOAuthConfigs[provider]
		if !ok {
			return oauthTokenResult{}, fmt.Errorf("no OAuth config for %s", provider)
		}

		log.Info("oauth", "force refresh (standard)", "provider", provider)
		tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
		if err != nil {
			h.parkRejectedOAuthAccount(connectionID, err)
			return oauthTokenResult{}, fmt.Errorf("OAuth refresh for %s: %w", provider, err)
		}

		update := tokenResp.BuildConnectionUpdate()
		var existing map[string]any
		if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
			log.Error("oauth", "unmarshal conn data failed", "conn", connectionID, "error", err)
			existing = make(map[string]any)
		}
		for k, v := range update {
			existing[k] = v
		}
		mergedJSON, err := json.Marshal(existing)
		if err != nil {
			log.Error("oauth", "marshal conn data failed", "conn", connectionID, "error", err)
		} else {
			db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
				string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
		}

		h.clearOAuthAccountPark(connectionID)

		pid := ""
		if v, ok := existing["projectId"].(string); ok {
			pid = v
		}
		log.Info("oauth", "token refreshed", "provider", provider, "project", pid)
		return oauthTokenResult{token: tokenResp.AccessToken, projectID: pid}, nil
	})

	if err != nil {
		return "", "", err
	}
	r, ok := res.(oauthTokenResult)
	if !ok {
		return "", "", nil
	}
	return r.token, r.projectID, nil
}

// handleGeminiStream processes Gemini stream SSE chunks and translates to OpenAI format.
// The stream drops the first SSE line (model metadata), then translates each content block SSE.
func (h *ChatHandler) handleGeminiStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translateResponse bool, metrics *streamMetrics) error {
	hw := proxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)
	geminiState := &translator.GeminiStreamState{}
	start := time.Now()
	// A /v1/responses client is one hop further out: Gemini events become
	// OpenAI chunks here and those chunks become Responses events, so the
	// bridge consumes this branch's output instead of the raw writer.
	var bridge *executor.ResponsesBridge
	if translator.NeedsResponsesBridge(ctx) {
		bridge = newResponsesBridge(ctx, hw, flusher, metrics, start)
	}
	// One session per stream so the OpenAI→Claude translation state cannot
	// collide across concurrent requests; always cleared on exit.
	sessionKey := fmt.Sprintf("gemini-stream-%d", time.Now().UnixNano())
	defer func() {
		if translateResponse {
			if endChunk := translator.EnsureStreamClosed(sessionKey); len(endChunk) > 0 {
				hw.Write(endChunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		translator.ClearStreamState(sessionKey)
	}()

	var totalChunks int
	var totalBytesWritten int
	err := proxy.ScanStream(upstream, func(chunk []byte) {
		chunkStr := strings.TrimSpace(string(chunk))
		if chunkStr == "" || chunkStr == "[DONE]" {
			return
		}

		// Unwrap antigravity envelope if present (SSE chunks may be wrapped as {"response": {...}})
		unwrapped := translator.UnwrapAntigravityResponse(chunk)

		// Translate each Gemini SSE chunk to OpenAI SSE chunk
		openaiChunk, err := translator.TranslateGeminiChunkToOpenAI(unwrapped, geminiState)
		if err != nil {
			log.Error("gemini", "chunk translate error", "error", err)
			return
		}
		if openaiChunk == nil {
			return
		}

		totalChunks++
		if metrics.TTFT == 0 {
			metrics.TTFT = time.Since(start).Milliseconds()
		}
		metrics.ResponseBuf.Write(openaiChunk)

		if bridge != nil {
			bridge.FeedFrames(openaiChunk)
			totalBytesWritten += len(openaiChunk)
			return
		}

		if translateResponse {
			// openaiChunk may have multiple SSE lines -- split and translate each
			for _, sse := range strings.Split(string(openaiChunk), "\n") {
				sse = strings.TrimSpace(sse)
				if !strings.HasPrefix(sse, "data: ") {
					continue
				}
				claudeChunk, tErr := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, []byte(strings.TrimPrefix(sse, "data: ")))
				if tErr != nil {
					log.Error("gemini", "claude translate error", "error", tErr)
					continue
				}
				if claudeChunk == nil {
					continue
				}
				n, _ := hw.Write(claudeChunk)
				totalBytesWritten += n
			}
		} else {
			n, _ := hw.Write(openaiChunk)
			totalBytesWritten += n
		}
		if flusher != nil {
			flusher.Flush()
		}
	})
	if bridge != nil {
		// The upstream is fully drained, so the completion watchdog has nothing
		// left to give up on and must not fire into hw after this returns.
		bridge.StopCompletionWatchdog()
		bridge.Close()
	}
	if !translateResponse && bridge == nil {
		n, _ := hw.Write([]byte("data: [DONE]\n\n"))
		totalBytesWritten += n
		if flusher != nil {
			flusher.Flush()
		}
	}
	log.Info("gemini", "stream ended", "chunks", totalChunks, "bytesWritten", totalBytesWritten, "duration_ms", time.Since(start).Milliseconds(), "err", err)
	// Pull actual accumulated usage (incl. cached tokens) out of the session so
	// the log sees real numbers instead of the fallback estimate.
	if translateResponse {
		if usage := translator.GetStreamUsage(sessionKey); usage != nil {
			translator.SetUsage(ctx, usage)
		}
	} else if geminiState.Usage != nil {
		translator.SetUsage(ctx, geminiState.Usage)
	}
	return err
}

// handleGeminiNonStream translates a Gemini non-stream response to OpenAI format,
// and then to Claude format if translateResponse is true.
func (h *ChatHandler) handleGeminiNonStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translateResp bool, metrics *streamMetrics) error {
	body, err := io.ReadAll(io.LimitReader(upstream, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("read gemini response body: %w", err)
	}

	if metrics != nil {
		metrics.ResponseBuf.Write(body)
	}

	// Use the full translator which handles tool calls, thinking, etc.
	openaiResp, usage, err := translator.TranslateGeminiResponseToOpenAI(body)
	if err == nil && usage != nil {
		translator.SetUsage(ctx, usage)
	}
	if err != nil {
		// Fallback: write raw body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	if translator.NeedsResponsesBridge(ctx) {
		// The body is already Chat Completions at this point, so it goes
		// through the same converter the other non-streaming paths use.
		return h.respondAsResponses(ctx, w, openaiResp)
	}

	if translateResp {
		// OpenAI → Claude (Anthropic) format
		claudeResp, claudeUsage, tErr := translator.TranslateOpenAIToClaude(openaiResp)
		if tErr == nil && claudeUsage != nil {
			translator.SetUsage(ctx, claudeUsage)
		}
		if tErr != nil || claudeResp == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(openaiResp)
			return nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(claudeResp)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openaiResp)
	return nil
}
