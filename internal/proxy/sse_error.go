package proxy

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
)

// SSEClientFormat picks the client wire format for a terminal frame emitted
// after HTTP 200 already went out: the status code can no longer change, so
// an abort is reportable only in-band (upstream decolua/9router 93001213).
type SSEClientFormat uint8

const (
	SSEFormatOpenAI SSEClientFormat = iota
	SSEFormatClaude
)

type sseErrorPayload struct {
	Error sseErrorDetail `json:"error"`
}

type sseErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func sseErrorInfo(code int) (string, string) {
	switch code {
	case 499:
		return "invalid_request_error", "request_aborted"
	case 502:
		return "server_error", "upstream_error"
	case 504:
		return "server_error", "gateway_timeout"
	default:
		if code >= 500 {
			return "server_error", "internal_server_error"
		}
		return "invalid_request_error", ""
	}
}

func BuildStreamErrorBytes(code int, message string, format SSEClientFormat) []byte {
	typ, errCode := sseErrorInfo(code)
	payload := sseErrorPayload{Error: sseErrorDetail{Message: message, Type: typ, Code: errCode}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(`{"error":{"message":"upstream stream failed","type":"server_error","code":"internal_server_error"}}`)
	}
	if format == SSEFormatClaude {
		ev, _ := json.Marshal(map[string]any{"type": "error", "error": payload.Error})
		return []byte("event: error\ndata: " + string(ev) + "\n\n")
	}
	return []byte("data: " + string(encoded) + "\n\n" + sseDoneFrame)
}

// ClassifyStreamAbort turns a mid-stream read failure into the status and
// message the client sees. A stall watchdog firing and a socket dying are
// different failures and the client should be able to tell them apart.
func ClassifyStreamAbort(cause error) (int, string) {
	if errors.Is(cause, ErrStreamStall) {
		return http.StatusGatewayTimeout, "stream stall timeout"
	}
	if errors.Is(cause, context.Canceled) {
		return 499, "upstream request aborted"
	}
	return http.StatusBadGateway, "upstream connection lost"
}
