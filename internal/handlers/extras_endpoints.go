package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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
// The model field selects the provider (e.g. "tavily/search" or "provider/search");
// "/search" is appended to the upstream base URL. JSON in/out.
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

// HandleScrape forwards /v1/scrape (web fetch) requests to an upstream
// fetch-capable provider. The model field selects the provider; "/scrape"
// is appended to the upstream base URL. JSON in/out.
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
