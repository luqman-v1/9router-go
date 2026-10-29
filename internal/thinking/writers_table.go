package thinking

import "9router/proxy/internal/providers"

// writerCtx carries what a per-format writer needs: the body to write into,
// the intent (already clamped to minimal when the model cannot disable), and
// the capability facts the dialect depends on.
type writerCtx struct {
	body       map[string]any
	eff        *Intent
	none       bool
	canDisable bool
	supported  []string
	display    string
	caps       providers.Capabilities
}

// formatWriters maps a thinking format to the writer that emits its dialect.
// Port of the applyFormat switch in open-sse/translator/concerns/thinkingUnified.js.
var formatWriters = map[string]func(*writerCtx){
	"openai":           writeOpenAI,
	"openai-responses": writeOpenAIResponses,
	"claude-adaptive":  writeClaudeAdaptive,
	"claude-budget":    writeClaudeBudget,
	"gemini-level":     writeGeminiLevel,
	"gemini-budget":    writeGeminiBudget,
	"zai":              writeZai,
	"qwen":             writeQwen,
	"deepseek":         writeDeepseek,
	"kimi":             writeKimi,
	"minimax":          writeMinimax,
	"hunyuan":          writeHunyuan,
	"step":             writeStep,
	"tokenrouter":      writeTokenRouter,
	"kiro":             writeKiro,
	"commandcode":      writeCommandcode,
}

func writeOpenAI(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["reasoning_effort"] = "none"
		return
	}
	if lvl := ToLevel(c.eff); lvl != "" {
		c.body["reasoning_effort"] = levelFor(lvl, c.supported)
	}
}

// writeOpenAIResponses emits the Responses dialect: reasoning nests effort
// under {effort,summary}, because a top-level reasoning_effort is rejected by
// strict upstreams. none omits the field.
func writeOpenAIResponses(c *writerCtx) {
	if c.none && c.canDisable {
		delete(c.body, "reasoning")
		return
	}
	if lvl := ToLevel(c.eff); lvl != "" {
		cur, _ := c.body["reasoning"].(map[string]any)
		if cur == nil {
			cur = map[string]any{}
		}
		cur["effort"] = levelFor(lvl, c.supported)
		if _, ok := cur["summary"]; !ok {
			cur["summary"] = "auto"
		}
		c.body["reasoning"] = cur
	}
	delete(c.body, "reasoning_effort")
}

func writeClaudeAdaptive(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	// Permanently adaptive models (e.g. Fable 5.1) take effort directly.
	if c.canDisable {
		c.body["thinking"] = withDisplay(map[string]any{"type": "adaptive"}, c.display)
	} else {
		delete(c.body, "thinking")
	}
	lvl := ToLevel(c.eff)
	effort := "high"
	switch lvl {
	case "minimal":
		effort = "low"
	case "auto":
		// "auto" asks the upstream to decide; the adaptive dialect names it high.
	default:
		effort = levelFor(lvl, c.supported)
	}
	c.body["output_config"] = map[string]any{"effort": effort}
}

func writeClaudeBudget(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	b, ok := ToBudget(c.eff, c.caps.ThinkingRange)
	if b == -1 {
		c.body["thinking"] = withDisplay(map[string]any{"type": "enabled"}, c.display)
		return
	}
	if !ok || b <= 0 {
		b = 8192
	}
	// The Messages API rejects a budget below its floor ("thinking.budget_tokens:
	// must be >= 1024"), so the smallest budget it accepts is the floor here.
	// The none→minimal clamp feeds this writer too, and a 512 there is a 400.
	if b < ClaudeMinThinkingBudget {
		b = ClaudeMinThinkingBudget
	}
	c.body["thinking"] = withDisplay(map[string]any{"type": "enabled", "budget_tokens": b}, c.display)
}

func writeGeminiLevel(c *writerCtx) {
	tc, floor := GeminiConfig(c.eff, "gemini-level", c.caps, c.supported)
	SetGeminiThinking(c.body, tc)
	if floor > 0 {
		EnsureGeminiOutputFloor(c.body, floor, c.caps)
	}
}

func writeGeminiBudget(c *writerCtx) {
	tc, floor := GeminiConfig(c.eff, "gemini-budget", c.caps, c.supported)
	SetGeminiThinking(c.body, tc)
	if floor > 0 {
		EnsureGeminiOutputFloor(c.body, floor, c.caps)
	}
}

// writeZai emits the Z.ai dialect: Z.ai ignores thinking.disabled, so
// enable_thinking:false is the only way to switch thinking off.
func writeZai(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["enable_thinking"] = false
		delete(c.body, "thinking")
		return
	}
	c.body["thinking"] = map[string]any{"type": "enabled"}
	// reasoning_effort read only from GLM-5.2 onward (older ignore it).
	if !c.caps.ThinkingEffortSupported {
		return
	}
	switch lvl := ToLevel(c.eff); lvl {
	case "low", "minimal":
		c.body["reasoning_effort"] = "low"
	case "high", "medium":
		c.body["reasoning_effort"] = "high"
	default:
		c.body["reasoning_effort"] = "max"
	}
}

func writeQwen(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["enable_thinking"] = false
		return
	}
	c.body["enable_thinking"] = true
	if b, ok := ToBudget(c.eff, c.caps.ThinkingRange); ok && b > 0 {
		c.body["thinking_budget"] = b
	}
}

func writeDeepseek(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	c.body["thinking"] = map[string]any{"type": "enabled"}
	lvl := ToLevel(c.eff)
	want := "high"
	if lvl == "xhigh" || lvl == "max" {
		want = "max"
	}
	// Some backends 400 on "max" — clamp when levels exclude it.
	if want == "max" && len(c.supported) > 0 && !containsLevel(c.supported, "max") {
		want = "high"
	}
	c.body["reasoning_effort"] = want
}

func writeKimi(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	if e := ToKimiReasoningEffort(c.eff); e != "" {
		c.body["reasoning_effort"] = e
	}
}

// writeMinimax emits the MiniMax dialect: M3 is adaptive, M2.x cannot
// disable (the clamp above covers that case).
func writeMinimax(c *writerCtx) {
	typ := "adaptive"
	if c.none && c.canDisable {
		typ = "disabled"
	}
	c.body["thinking"] = map[string]any{"type": typ}
}

func writeHunyuan(c *writerCtx) {
	if c.none && c.canDisable {
		c.body["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	b, ok := ToBudget(c.eff, c.caps.ThinkingRange)
	if b == -1 {
		c.body["thinking"] = map[string]any{"type": "enabled"}
		return
	}
	if !ok || b <= 0 {
		b = 8192
	}
	c.body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": b}
}

// writeStep emits nothing on a disable: step models mandate reasoning, so
// omitting the field lets the upstream default apply.
func writeStep(c *writerCtx) {
	if c.none && c.canDisable {
		return
	}
	if lvl := ToLevel(c.eff); lvl != "" {
		lvl = levelFor(lvl, c.supported)
		if lvl == "xhigh" || lvl == "max" {
			lvl = "high"
		}
		c.body["reasoning_effort"] = lvl
	}
}

// writeTokenRouter emits the TokenRouter dialect: its enum is
// low/medium/high/xhigh/max, so "none" and "auto" are omitted. A model that
// cannot disable is not omitted: the clamp already turned the disable into
// the lowest effort, and that effort is what the request carries.
func writeTokenRouter(c *writerCtx) {
	if c.none && c.canDisable {
		return
	}
	if c.eff.Mode == ModeAuto {
		return
	}
	if lvl := ToLevel(c.eff); lvl != "" {
		lvl = knownLevel(lvl)
		// The enum is low/medium/high/xhigh/max: "minimal" is not one of them,
		// and the none→minimal clamp lands here. Its floor is "low".
		if lvl == "minimal" {
			lvl = "low"
		}
		c.body["reasoning_effort"] = lvl
	}
}

// writeKiro writes no field: the Kiro envelope carries the effort in its own
// schema, not in a body field. This port does not implement that injection
// (the Kiro translator documents it as not ported), so a Kiro request runs at
// the model's default effort. The format exists so the pass still strips the
// reasoning dialects on that lane instead of forwarding a field the Kiro
// envelope does not carry.
func writeKiro(c *writerCtx) {}

// writeCommandcode emits the CommandCode dialect: the native CLI sends
// reasoning_effort inside the params of the /alpha/generate envelope. Go's
// flat OpenAI body has no params object — buildCommandcodeBody wraps it
// later and copies the whitelisted top-level keys into params — so the field
// is written at the top level.
func writeCommandcode(c *writerCtx) {
	if params, ok := c.body["params"].(map[string]any); ok {
		if c.none && c.canDisable {
			delete(params, "reasoning_effort")
			return
		}
		if lvl := ToLevel(c.eff); lvl != "" {
			params["reasoning_effort"] = levelFor(lvl, c.supported)
		}
		return
	}
	if c.none && c.canDisable {
		return
	}
	if lvl := ToLevel(c.eff); lvl != "" {
		c.body["reasoning_effort"] = levelFor(lvl, c.supported)
	}
}
