package providers

import "strings"

// CommandCode serves every model from a single /alpha/generate endpoint, so the
// model families it resells (deepseek-v4, kimi, glm, qwen, step, …) must not
// inherit their own wire format here. This is the Go port of the commandcode
// short-circuit at the top of getCapabilitiesForModel
// (open-sse/providers/capabilities.js:570).

// commandCodeContextWindow and commandCodeMaxOutput are the limits CommandCode
// advertises for its whole catalog, independent of the underlying family.
// Upstream caps cmc/* at 1M/384k, but the live /alpha/generate endpoint
// validates params.max_tokens <= 200000 and rejects larger budgets with a 400
// (upstream decolua/9router#5268). The advertised cap is the enforced one,
// so clients that trust /v1/models stay inside it.
const (
	commandCodeContextWindow = 1000000
	commandCodeMaxOutput     = 200000
)

// commandCodeTextOnly mirrors the CommandCode CLI isKnownTextOnlyModel
// denylist: no image input. Anything not listed is assumed to take images, so
// new models are vision-capable without a code change.
var commandCodeTextOnly = map[string]bool{
	"deepseek/deepseek-v4-pro":              true,
	"deepseek/deepseek-v4-flash":            true,
	"deepseek/deepseek-v4-flash-fast":       true,
	"zai-org/glm-5.3":                       true,
	"zai-org/glm-5.2":                       true,
	"zai-org/glm-5.2-fast":                  true,
	"zai-org/glm-5.1":                       true,
	"zai-org/glm-5":                         true,
	"minimaxai/minimax-m2.7":                true,
	"minimax/minimax-m2.7-free":             true,
	"minimaxai/minimax-m2.5":                true,
	"xiaomi/mimo-v2.5-pro":                  true,
	"qwen/qwen3.6-max-preview":              true,
	"qwen/qwen3.7-max":                      true,
	"meituan/longcat-2.0:free":              true,
	"stepfun/step-3.5-flash":                true,
	"tencent/hy4-preview":                   true,
	"tencent/hy3":                           true,
	"tencent/hy3-paid":                      true,
	"nvidia/nemotron-3-ultra-550b-a55b":     true,
	"poolside/laguna-s-2.1-free":            true,
	"inclusionai/ling-3.0-flash-free":       true,
	"inclusionai/ling-3.0-flash-sante:free": true,
}

// isCommandCodeProvider reports whether provider is CommandCode under either
// its id or its uiAlias.
func isCommandCodeProvider(provider string) bool {
	return provider == "commandcode" || provider == "cmc"
}

// isCommandCodeTextOnly reports whether CommandCode serves model without image
// input. The model id is matched lowercase, both as written ("deepseek/deepseek-v4-pro")
// and by its bare name, so a catalog entry that carries an extra vendor prefix
// still resolves.
func isCommandCodeTextOnly(model string) bool {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return false
	}
	if commandCodeTextOnly[key] {
		return true
	}
	base := key
	if _, after, ok := strings.CutLast(key, "/"); ok {
		base = after
	}
	for id := range commandCodeTextOnly {
		if !strings.Contains(id, "/") {
			continue
		}
		if _, after, _ := strings.CutLast(id, "/"); after == base {
			return true
		}
	}
	return false
}

// commandCodeCapabilities resolves the fixed CommandCode capability block for
// one model: reasoning on, effort-based thinking, and vision unless the model is
// on the text-only denylist.
func commandCodeCapabilities(model string) Capabilities {
	res := DefaultCapabilities
	res.Reasoning = true
	res.ThinkingFormat = "commandcode"
	res.ThinkingEffortSupported = true
	res.Vision = !isCommandCodeTextOnly(model)
	res.ContextWindow = commandCodeContextWindow
	res.MaxOutput = commandCodeMaxOutput
	return res
}
