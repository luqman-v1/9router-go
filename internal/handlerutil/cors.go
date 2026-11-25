package handlerutil

import "net/http"

// WriteCORSHeaders answers a simple cross-origin read: GET plus the OPTIONS
// preflight that must precede it. Every public endpoint whose browser callers
// live on another origin (health probes from the tunnel cards, an Ollama client
// on a different port) sends the same three headers, so they live here rather
// than being copied per handler — three copies is how one of them drifts the
// next time a header is added.
//
// `*` for Allow-Origin is deliberate and safe only because none of these
// responses carry credentials: the middleware is not involved, no cookie is
// read, and the body is public catalogue data.
func WriteCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

// WriteCORSPreflight answers an OPTIONS preflight with 204 and the CORS headers.
// A preflight carries no body and no credentials, so 204 is the answer the
// browser expects.
func WriteCORSPreflight(w http.ResponseWriter) {
	WriteCORSHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}
