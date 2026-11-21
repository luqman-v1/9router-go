package db

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

type extraBodyKind int

const (
	kindNumber extraBodyKind = iota
	kindInteger
	kindStop
	kindLogitBias
	kindBool
	kindEffort
	kindReasoning
)

// ExtraBodyAllowedKeys is the closed set of top-level request members an
// extraBody may set: sampling and reasoning controls only. Matching is exact
// and case-sensitive; anything else (routing, plugins, gateway-owned members)
// is rejected on save and dropped at merge time. The dashboard mirrors this
// list in web/src/lib/extraBody.ts.
var ExtraBodyAllowedKeys = []string{
	"temperature", "top_p", "top_k", "min_p", "top_a", "repetition_penalty",
	"frequency_penalty", "presence_penalty", "seed", "stop", "logit_bias",
	"logprobs", "top_logprobs", "max_completion_tokens",
	"reasoning", "reasoning_effort",
}

// extraBodyKinds is the value type of each allowed key. Integer keys accept any
// integer-valued number (5 and 5.0); numeric ranges are left to the upstream.
var extraBodyKinds = map[string]extraBodyKind{
	"temperature": kindNumber, "top_p": kindNumber, "top_k": kindInteger,
	"min_p": kindNumber, "top_a": kindNumber, "repetition_penalty": kindNumber,
	"frequency_penalty": kindNumber, "presence_penalty": kindNumber,
	"seed": kindInteger, "stop": kindStop, "logit_bias": kindLogitBias,
	"logprobs": kindBool, "top_logprobs": kindInteger,
	"max_completion_tokens": kindInteger, "reasoning": kindReasoning,
	"reasoning_effort": kindEffort,
}

var (
	reasoningEfforts   = []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}
	reasoningSummaries = []string{"auto", "concise", "detailed"}
	reasoningContexts  = []string{"auto", "all_turns", "current_turn"}
	reasoningModes     = []string{"standard", "pro"}
)

// reasoningSubKeys lists the accepted members of the reasoning object.
var reasoningSubKeys = []string{"effort", "max_tokens", "enabled", "exclude", "summary", "context", "mode"}

// ErrExtraBodyKey marks an extraBody member that is not allowed or has a wrong value.
var ErrExtraBodyKey = errors.New("extraBody key not accepted")

func extraBodyKeyErr(key, format string, args ...any) error {
	return fmt.Errorf("%w: %q %s", ErrExtraBodyKey, key, fmt.Sprintf(format, args...))
}

// validateExtraBodyMembers checks every member against the allowlist and its
// value type. Keys are visited in sorted order so the error is deterministic.
func validateExtraBodyMembers(members map[string]jsontext.Value) error {
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		kind, ok := extraBodyKinds[key]
		if !ok {
			return extraBodyKeyErr(key, "is not allowed; allowed keys: %s", strings.Join(ExtraBodyAllowedKeys, ", "))
		}
		if err := validateExtraBodyValue(key, kind, members[key]); err != nil {
			return err
		}
	}
	return validateReasoningConflict(members)
}

func validateExtraBodyValue(key string, kind extraBodyKind, raw jsontext.Value) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return extraBodyKeyErr(key, "is not valid JSON")
	}
	if v == nil { // null is accepted wherever the spec allows it (all of these)
		return nil
	}
	switch kind {
	case kindNumber:
		if _, ok := v.(float64); !ok {
			return extraBodyKeyErr(key, "must be a number")
		}
	case kindInteger:
		if f, ok := v.(float64); !ok || f != math.Trunc(f) {
			return extraBodyKeyErr(key, "must be an integer")
		}
	case kindBool:
		if _, ok := v.(bool); !ok {
			return extraBodyKeyErr(key, "must be a boolean")
		}
	case kindStop:
		return validateStop(key, v)
	case kindLogitBias:
		return validateLogitBias(key, v)
	case kindEffort:
		return validateEnum(key, v, reasoningEfforts)
	case kindReasoning:
		return validateReasoning(v)
	}
	return nil
}

func validateStop(key string, v any) error {
	switch s := v.(type) {
	case string:
		return nil
	case []any:
		for _, e := range s {
			if _, ok := e.(string); !ok {
				return extraBodyKeyErr(key, "must be a string or an array of strings")
			}
		}
		return nil
	}
	return extraBodyKeyErr(key, "must be a string or an array of strings")
}

func validateLogitBias(key string, v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return extraBodyKeyErr(key, "must be an object of numbers")
	}
	for _, e := range obj {
		if _, ok := e.(float64); !ok {
			return extraBodyKeyErr(key, "must be an object of numbers")
		}
	}
	return nil
}

func validateEnum(key string, v any, allowed []string) error {
	s, ok := v.(string)
	if !ok || !slices.Contains(allowed, s) {
		return extraBodyKeyErr(key, "must be one of: %s", strings.Join(allowed, ", "))
	}
	return nil
}

func validateReasoning(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return extraBodyKeyErr("reasoning", "must be an object")
	}
	for _, sub := range slices.Sorted(mapKeys(obj)) {
		name := "reasoning." + sub
		val := obj[sub]
		if val == nil {
			if !slices.Contains(reasoningSubKeys, sub) {
				return unknownReasoningKey(name)
			}
			continue
		}
		var err error
		switch sub {
		case "effort":
			err = validateEnum(name, val, reasoningEfforts)
		case "summary":
			err = validateEnum(name, val, reasoningSummaries)
		case "context":
			err = validateEnum(name, val, reasoningContexts)
		case "mode":
			err = validateEnum(name, val, reasoningModes)
		case "max_tokens":
			if f, isNum := val.(float64); !isNum || f != math.Trunc(f) {
				err = extraBodyKeyErr(name, "must be an integer")
			}
		case "enabled", "exclude":
			if _, isBool := val.(bool); !isBool {
				err = extraBodyKeyErr(name, "must be a boolean")
			}
		default:
			err = unknownReasoningKey(name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func unknownReasoningKey(name string) error {
	return extraBodyKeyErr(name, "is not allowed; allowed reasoning keys: %s", strings.Join(reasoningSubKeys, ", "))
}

func mapKeys(m map[string]any) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// validateReasoningConflict rejects reasoning.max_tokens combined with an
// effort (reasoning.effort or reasoning_effort): OpenRouter answers 400.
func validateReasoningConflict(members map[string]jsontext.Value) error {
	var r map[string]jsontext.Value
	if raw, ok := members["reasoning"]; !ok || json.Unmarshal(raw, &r) != nil || r == nil {
		return nil
	}
	if isSet(r["max_tokens"]) && (isSet(r["effort"]) || isSet(members["reasoning_effort"])) {
		return extraBodyKeyErr("reasoning.max_tokens", "cannot be combined with reasoning.effort or reasoning_effort")
	}
	return nil
}

func isSet(v jsontext.Value) bool {
	return len(v) > 0 && string(v) != "null"
}

// FilterExtraBody returns the members that are allowed and valid, dropping the
// rest. Merge time uses it so a legacy row never forwards a disallowed key.
func FilterExtraBody(members map[string]jsontext.Value) map[string]jsontext.Value {
	out := make(map[string]jsontext.Value, len(members))
	for k, v := range members {
		kind, ok := extraBodyKinds[k]
		if ok && validateExtraBodyValue(k, kind, v) == nil {
			out[k] = v
		}
	}
	if validateReasoningConflict(out) != nil {
		delete(out, "reasoning")
		delete(out, "reasoning_effort")
	}
	return out
}
