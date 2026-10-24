package guardrails

import (
	"regexp"
)

// Engine applies a resolved policy to text. It holds no state, so it is safe
// for concurrent use without a lock.
type Engine struct {
	// detectors selects which detector sets run. "pii" and "injection" are the
	// MVP names; an empty slice means the policy enables nothing.
	detectors []string
	// action is what fires when any detector matches.
	action Action
	// customDetectors holds compiled custom regex patterns.
	customDetectors []detector
	// customReplacements maps detector name to a custom replacement string.
	customReplacements map[string]string
}

// NewEngine builds an engine from a policy's detector names and action.
func NewEngine(detectors []string, action Action) *Engine {
	if action == "" {
		action = ActionLogOnly
	}
	return &Engine{detectors: detectors, action: action}
}

// NewEngineWithCustom builds an engine with detector names, action, and custom patterns.
func NewEngineWithCustom(detectors []string, action Action, customPatterns []CustomPattern) *Engine {
	e := NewEngine(detectors, action)
	if len(customPatterns) > 0 {
		e.customDetectors = make([]detector, 0, len(customPatterns))
		e.customReplacements = make(map[string]string)
		for _, cp := range customPatterns {
			if cp.Regex == "" {
				continue
			}
			re, err := regexp.Compile(cp.Regex)
			if err != nil {
				continue
			}
			name := cp.Name
			if name == "" {
				name = "custom"
			}
			e.customDetectors = append(e.customDetectors, detector{
				name:     name,
				re:       re,
				entity:   "CUSTOM_PATTERN",
				severity: SeverityMedium,
			})
			if cp.Replacement != "" {
				e.customReplacements[name] = cp.Replacement
			}
		}
	}
	return e
}

// Enabled reports whether the engine runs anything at all. A disabled engine
// must not rewrite content, so callers check this before touching a body.
func (e *Engine) Enabled() bool {
	return e != nil && (len(e.detectors) > 0 || len(e.customDetectors) > 0) && e.action != ActionAllow
}

func (e *Engine) runs(name string) bool {
	for _, d := range e.detectors {
		if d == name {
			return true
		}
	}
	return false
}

// Scan evaluates text and returns the decision.
//
// Findings from every enabled detector are collected before the action is
// applied, so one decision covers the whole message and the audit log records
// everything that matched rather than only what stopped it.
func (e *Engine) Scan(text string) *Decision {
	if !e.Enabled() || text == "" {
		return &Decision{Action: ActionAllow}
	}

	var findings []Finding
	if e.runs("pii") {
		findings = append(findings, run(piiDetectors, text)...)
	}
	if e.runs("injection") {
		findings = append(findings, run(injectionDetectors, text)...)
	}
	if len(e.customDetectors) > 0 {
		for _, f := range run(e.customDetectors, text) {
			if repl, ok := e.customReplacements[f.Detector]; ok && repl != "" {
				f.Redacted = repl
			}
			findings = append(findings, f)
		}
	}
	if len(findings) == 0 {
		return &Decision{Action: ActionAllow}
	}

	d := &Decision{Action: e.action, Findings: findings}
	if e.action == ActionMask {
		if mutated := maskFindings(text, findings); mutated != text {
			d.Mutated = mutated
		}
	}
	return d
}

// ScanJSON walks a decoded request or response payload and applies the engine
// to every string value it finds, returning the mutated payload.
//
// It is used for the inbound tap: the caller has already parsed the body, so
// rewriting the strings in place avoids re-serialising arbitrary structures
// the gateway does not understand. A non-object, non-array payload is scanned
// as a plain string.
func (e *Engine) ScanJSON(payload any) (any, *Decision) {
	if !e.Enabled() {
		return payload, &Decision{Action: ActionAllow}
	}

	worst := ActionAllow
	var all []Finding
	var mutated bool

	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case string:
			d := e.Scan(t)
			worst = StrictestAction(worst, d.Action)
			all = append(all, d.Findings...)
			if d.WasMutated() {
				mutated = true
				return d.Mutated
			}
			return t
		case map[string]any:
			for k, val := range t {
				t[k] = walk(val)
			}
			return t
		case []any:
			for i, val := range t {
				t[i] = walk(val)
			}
			return t
		default:
			return v
		}
	}

	out := walk(payload)
	if len(all) == 0 {
		return out, &Decision{Action: ActionAllow}
	}
	return out, &Decision{Action: worst, Findings: all, Mutated: boolMutated(mutated)}
}

func boolMutated(b bool) string {
	if b {
		return "1"
	}
	return ""
}
