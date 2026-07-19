package proxyhandlers

import (
	"net/http"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// ForwardOpenAI forwards an OpenAI-format request and writes the response.
func ForwardOpenAI(w http.ResponseWriter, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream, translateResponse bool) error {
	resp, err := proxy.ForwardOpenAI(client, cfg, apiKey, body, isStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if isStream {
		return SSEStream(w, resp.Body, translateResponse, time.Now(), nil, nil)
	}
	return JSONResponse(w, resp.Body, translateResponse)
}
