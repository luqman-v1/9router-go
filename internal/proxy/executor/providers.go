package executor

import (
	"net/http"
	"time"

	"9router/proxy/internal/proxy"
)

// ForwardGrokCLI forwards to grok-cli using Responses API format.
func ForwardGrokCLI(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardGrokCLI(req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return codexStream(w, resp.Body)
}

// ForwardIflow forwards to iflow with HMAC signing (headers built in handler).
func ForwardIflow(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardIflow(req.Client, req.Config, req.APIKey, req.Body, req.IsStream, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
}

// ForwardKimchi forwards to kimchi (body already cleaned by handler).
func ForwardKimchi(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardKimchi(req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
}

// ForwardKiro forwards to kiro.
func ForwardKiro(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardKiro(req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
}

// ForwardAzure forwards to Azure OpenAI.
func ForwardAzure(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardAzure(req.Client, req.Config, req.APIKey, req.Body, req.IsStream, req.Endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
}

// ForwardCommandcode forwards to CommandCode.
func ForwardCommandcode(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardCommandcode(req.Client, req.Config, req.APIKey, req.Body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return jsonResponse(w, resp.Body, false)
}
