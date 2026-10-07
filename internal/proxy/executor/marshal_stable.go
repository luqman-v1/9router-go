package executor

import (
	json "encoding/json/v2"
)

// marshalStable serializes with json.Deterministic(true): map members are
// emitted in sorted key order, so the same logical request always produces
// byte-identical output. DeepSeek (and other prefix-caching upstreams) key
// their prompt cache on the longest byte-identical request prefix; the
// default json/v2 marshaling randomizes map member order per call, which
// reshuffles the whole body on every rewrite and turns every request after
// the first into a full cache miss. Use marshalStable (or the inline
// json.Deterministic(true) option in the translator package, which cannot
// import this one) for every unmarshal->mutate->remarshal cycle on request
// bodies headed to prefix-caching upstreams — deepseek lanes: zen, opencode,
// opencode-go, and native deepseek tool dedupe.
func marshalStable(v any) ([]byte, error) {
	return json.Marshal(v, json.Deterministic(true))
}
