package chat

import (
	"errors"
	"fmt"
	"net/http"

	"9router/proxy/internal/customjson"
	"9router/proxy/internal/proxy/executor"
)

// isCustomJSONConn reports whether a connection targets a custom JSON node
// (upstream decolua/9router#4929). Its endpoint is a business API whose call
// may have side effects, so one attempt is final: no account rotation, no
// combo fallback, and no zero-content probe that would replay an empty answer
// elsewhere.
func isCustomJSONConn(connData *ConnectionData) bool {
	return connData != nil && customjson.IsConnection(connData.ProviderSpecificData)
}

// customJSONKeyOptional reports whether the node sends no credential at all,
// so a connection without an API key is still usable. The auth type is read
// from the configuration the server copied from the node, never from a value
// a client could have supplied on its own.
func customJSONKeyOptional(connData *ConnectionData) bool {
	if !isCustomJSONConn(connData) {
		return false
	}
	cfg, _, err := customjson.FromConnection(connData.ProviderSpecificData)
	return err == nil && cfg.AuthType == customjson.AuthNone
}

// executorFor picks the executor a connection dispatches through. A custom
// JSON node keeps its compatible-node provider id, so the choice is made on
// the connection's stored apiType rather than on the registry.
func executorFor(provider string, connData *ConnectionData) executor.Executor {
	if isCustomJSONConn(connData) {
		return executor.ForwardCustomJSON
	}
	return executor.Get(provider)
}

// customJSONTerminalError is the error a combo reports after a custom JSON
// attempt, which ends the turn whatever its outcome.
func customJSONTerminalError(err error) *upstreamError {
	var ue *upstreamError
	if errors.As(err, &ue) && ue != nil {
		return ue
	}
	return &upstreamError{StatusCode: http.StatusBadGateway, Body: upstreamErrorBody(fmt.Sprintf("upstream error: %v", err))}
}
