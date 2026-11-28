package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

func ForwardAzure(w http.ResponseWriter, req *Request) error {
	fittedBody, toolNameMap := applyToolNameAlias(req)
	if len(toolNameMap) > 0 {
		req.Body = fittedBody
		req.ToolNameMap = mergeToolNameMaps(req.ToolNameMap, toolNameMap)
	}
	var oreq struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		log.Warn("executor", "azure unmarshal body", "error", err)
	}
	modelName := oreq.Model
	if modelName == "" {
		modelName = "gpt-4"
	}

	endpoint := os.Getenv("AZURE_ENDPOINT")
	apiVersion := os.Getenv("AZURE_API_VERSION")
	if apiVersion == "" {
		apiVersion = "2024-10-01-preview"
	}
	deployment := os.Getenv("AZURE_DEPLOYMENT")
	if deployment == "" {
		deployment = modelName
	}
	if endpoint == "" {
		endpoint = "https://api.openai.com"
	}

	baseURL := strings.TrimRight(endpoint, "/")
	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		baseURL, deployment, apiVersion)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	r, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("api-key", req.APIKey)
	if req.IsStream {
		r.Header.Set("Accept", "text/event-stream")
	}
	if req.IsStream && r.Header.Get("Accept-Encoding") == "" {
		r.Header.Set("Accept-Encoding", "identity")
	}

	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	proxy.UnwrapIdentityGzip(r, resp)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if readErr != nil {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: []byte("failed to read error body")}
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}
