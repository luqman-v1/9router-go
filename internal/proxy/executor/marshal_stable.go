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

// v2ChatKeyOrder is the leading key order the OpenCode v2 free-tier gate
// expects on a Chat Completions body. Remaining keys follow alphabetically.
//
// Why a fixed head at all: alphabetical sorting puts max_completion_tokens and
// messages ahead of model, and the reference client (opencode2api, commit
// 5c5100c) measured that the gate reads this body positionally. The ordering is
// still fully deterministic — a fixed head plus an alphabetical tail — so it
// preserves the property marshalStable exists for: a byte-identical prefix
// across the turns of a session, which DeepSeek's prompt cache keys on.
var v2ChatKeyOrder = []string{
	"model",
	"messages",
	"tools",
	"stream",
	"stream_options",
	"max_completion_tokens",
}

// marshalOpenCodeV2Chat serializes a Chat Completions body with the v2 gate's
// leading key order, then the remaining keys alphabetically.
//
// The leading keys are written one at a time rather than through the map
// marshaler, because json.Deterministic(true) sorts members — handing it a map
// whose insertion order encodes the requirement would silently discard it (the
// first version of this function did exactly that).
//
// A key absent from m is skipped rather than emitted as null: the gate
// distinguishes "no budget" from "budget null", and clampOutputBudget
// deliberately never invents a ceiling the caller did not set.
func marshalOpenCodeV2Chat(m map[string]any) ([]byte, error) {
	buf := make([]byte, 0, 512)
	buf = append(buf, '{')

	written := false
	emit := func(key string, raw []byte) error {
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return err
		}
		if written {
			buf = append(buf, ',')
		}
		written = true
		buf = append(buf, encodedKey...)
		buf = append(buf, ':')
		buf = append(buf, raw...)
		return nil
	}

	for _, key := range v2ChatKeyOrder {
		value, ok := m[key]
		if !ok {
			continue
		}
		// marshalStable, not json.Marshal: a nested value (stream_options,
		// messages) must be deterministic too, or the prefix moves between turns.
		raw, err := marshalStable(value)
		if err != nil {
			return nil, err
		}
		if err := emit(key, raw); err != nil {
			return nil, err
		}
	}

	tail := make(map[string]any, len(m))
	for key, value := range m {
		if containsKey(v2ChatKeyOrder, key) {
			continue
		}
		tail[key] = value
	}
	if len(tail) > 0 {
		// The tail is already sorted by marshalStable; splice its members in.
		tailJSON, err := marshalStable(tail)
		if err != nil {
			return nil, err
		}
		if inner := tailJSON[1 : len(tailJSON)-1]; len(inner) > 0 {
			if written {
				buf = append(buf, ',')
			}
			buf = append(buf, inner...)
		}
	}

	buf = append(buf, '}')
	return buf, nil
}

func containsKey(keys []string, key string) bool {
	for _, candidate := range keys {
		if candidate == key {
			return true
		}
	}
	return false
}

// marshalOpenCodeFreeChatBody serializes a free-lane Chat Completions body.
//
// The reference key order is applied only when the reference fingerprint is
// enabled, so the default lane keeps the exact bytes it has always sent. Both
// orders are deterministic; only the position of the leading block differs.
func marshalOpenCodeFreeChatBody(m map[string]any) ([]byte, error) {
	if opencodeReferenceFingerprint() {
		return marshalOpenCodeV2Chat(m)
	}
	return marshalStable(m)
}
