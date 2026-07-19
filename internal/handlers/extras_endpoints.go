package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// resolveForwardModel resolves a model from a JSON body and looks up the
// best connection + provider config for forwarding.
func (h *ChatHandler) resolveForwardModel(body []byte, fallbackModel string) (*ModelInfo, *modelsProviderCtx, error) {
	model := fallbackModel
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err == nil && reqBody.Model != "" {
		model = reqBody.Model
	}
	if model == "" {
		return nil, nil, fmt.Errorf("missing model")
	}
	modelInfo, err := h.resolveModel(model)
	if err != nil {
		return nil, nil, err
	}
	_, ctx, err := h.buildProviderCtx(modelInfo)
	if err != nil {
		return nil, nil, err
	}
	return modelInfo, ctx, nil
}

// HandleSearch forwards /v1/search requests to an upstream search-capable provider.
func (h *ChatHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	_, ctx, err := h.resolveForwardModel(body, "")
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}
	h.forwardMultimodal(w, r, ctx, "/search", body)
}

// HandleScrape forwards /v1/scrape requests to an upstream fetch-capable provider.
func (h *ChatHandler) HandleScrape(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	_, ctx, err := h.resolveForwardModel(body, "")
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}
	h.forwardMultimodal(w, r, ctx, "/scrape", body)
}

// HandleWebFetch forwards /v1/web/fetch requests to a web content extraction provider.
// Required body fields: url (target URL), model (provider URL for routing).
func (h *ChatHandler) HandleWebFetch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	modelOrProvider := reqBody.Model
	if modelOrProvider == "" {
		modelOrProvider = reqBody.Provider
	}
	if modelOrProvider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model or provider")
		return
	}
	if reqBody.URL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing url")
		return
	}

	modelInfo, err := h.resolveModel(modelOrProvider)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}

	_, ctx, err := h.buildProviderCtx(modelInfo)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}

	cfg := ctx.providerCfg
	fetchURL := cfg.FetchURL
	if fetchURL == "" {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("no fetch endpoint for provider: %s", modelInfo.Provider))
		return
	}

	var upstreamResp *http.Response
	if cfg.FetchMethod == "GET" {
		targetURL := strings.TrimRight(fetchURL, "/") + "/" + reqBody.URL
		req, reqErr := http.NewRequest("GET", targetURL, nil)
		if reqErr != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", reqErr))
			return
		}
		handlerutil.SetAuthHeader(req, ctx.apiKey, cfg.AuthHeader, cfg.AuthScheme)
		upstreamResp, err = h.Client.Do(req)
	} else {
		fetchBody, _ := json.Marshal(map[string]string{"url": reqBody.URL})
		upstreamResp, err = h.Client.Post(fetchURL, "application/json", strings.NewReader(string(fetchBody)))
	}
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer upstreamResp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(upstreamResp.StatusCode)
	io.Copy(w, upstreamResp.Body)

	if upstreamResp.StatusCode == http.StatusOK {
		h.Repo.UpdateConnectionLastUsed(ctx.conn.ID)
	}
}
