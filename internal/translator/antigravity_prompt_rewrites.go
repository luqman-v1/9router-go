package translator

import "regexp"

// Antigravity's backend answers a system prompt that carries another coding
// harness's branding with a false 429 RESOURCE_EXHAUSTED, even when the
// account's quota is untouched. The rows below are the fingerprints observed
// in Hermes Agent / OpenClaw payloads (local hardening): wording, section
// headers, and environment identifiers that identify the caller as a
// competing client.
//
// The rewrite is deliberately literal: instruction bodies survive, only the
// fingerprinted phrasing becomes neutral. Rows run in order, so compound
// identifiers are rewritten before the generic `hermes` catch-all.
//
// ag-shield also rewrites its own brand names (`antigravity`, `9router`) and
// locale-specific markers. Those rows are NOT ported: this pipeline already
// rewrites `opencode` to `antigravity` for parity, so undoing that here would
// fight the upstream rewrite instead of removing a harness fingerprint.
type antigravityFingerprintRewrite struct {
	from *regexp.Regexp
	to   string
}

func literalFingerprint(from, to string) antigravityFingerprintRewrite {
	return antigravityFingerprintRewrite{
		from: regexp.MustCompile("(?i)" + regexp.QuoteMeta(from)),
		to:   to,
	}
}

func regexpFingerprint(from, to string) antigravityFingerprintRewrite {
	return antigravityFingerprintRewrite{
		from: regexp.MustCompile("(?i)" + from),
		to:   to,
	}
}

var antigravityFingerprintRewrites = []antigravityFingerprintRewrite{
	// Confirmed Hermes system-prompt sentences the backend reads as a
	// competing harness (local hardening).
	literalFingerprint(
		"solve coding tasks. Be concise, practical, and tool-aware.",
		"work on their coding tasks. Keep responses concise and practical, and use the available tools when they help.",
	),
	literalFingerprint(
		"You are pair programming with a user to solve coding tasks.",
		"You pair program with the user on coding tasks.",
	),
	literalFingerprint(
		"When the user asks you to build, run, or verify something, the deliverable is a working artifact backed by real tool output",
		"If the user asks you to build, run or check something, deliver a working result based on actual tool output",
	),
	literalFingerprint(
		"Reporting a blocker honestly is always better than inventing a result.",
		"It is always better to report a blocker honestly than to make up a result.",
	),
	literalFingerprint("Be direct.", "Get straight to the point."),
	literalFingerprint("is your authoritative reference", "is your reference guide"),
	literalFingerprint("# Google model operational directives", "# Model operational directives"),
	literalFingerprint(
		"Act as an effective and practical assistant. Your primary goal is to help the user by answering questions, writing and debugging code, analyzing information, creating content, and executing tasks using the available tools.",
		"You are a helpful coding and technical assistant. Assist the user with questions, code, analysis, and tasks using available tools.",
	),

	// URLs and multi-word phrases must run before the token-level rows below.
	regexpFingerprint(`https?://hermes-agent\.nousresearch\.com/docs`, "the official documentation"),
	literalFingerprint("nousresearch.com", "the documentation site"),
	literalFingerprint("For help with Hermes Agent itself", "For help with the assistant itself"),
	literalFingerprint("Hermes delivers their message", "the runtime delivers their message"),
	literalFingerprint(
		"You are chatting inside the Hermes desktop app, a graphical chat surface.",
		"You are chatting inside a graphical desktop chat surface.",
	),

	// Structural headers and markers.
	literalFingerprint("# Finishing the job", "# Task completion"),
	literalFingerprint("# Parallel tool calls", "# Concurrent tool usage"),
	literalFingerprint("## Mid-turn user steering", "## Mid-turn user guidance"),
	regexpFingerprint(`\[OUT-OF-BAND USER MESSAGE[^\]]*\]`, "[USER UPDATE]"),
	literalFingerprint("[/OUT-OF-BAND USER MESSAGE]", "[/USER UPDATE]"),
	literalFingerprint("<available_skills>", "<available_guides>"),
	literalFingerprint("</available_skills>", "</available_guides>"),
	literalFingerprint("Before replying, scan the skills below.", "Before replying, review the available guides below."),
	literalFingerprint("Skills come first:", "Guides come first:"),
	literalFingerprint("## Skills", "## Guides"),
	regexpFingerprint(`═{10,}\s*MEMORY \(your personal notes\)[^═]*═{10,}`, "--- CONTEXT NOTES ---"),
	regexpFingerprint(`═{10,}\s*USER PROFILE \(who the user is\)[^═]*═{10,}`, "--- USER CONTEXT ---"),

	// Runtime headers.
	literalFingerprint("# Hermes runtime environment", "# Runtime environment"),
	literalFingerprint("<!-- End Hermes runtime environment -->", "<!-- End runtime environment -->"),

	// Environment variables and identifiers.
	literalFingerprint("HERMES_KANBAN_", "AGENT_KANBAN_"),
	literalFingerprint("HERMES_TENANT", "AGENT_TENANT"),
	literalFingerprint("hermes-agent", "agent-guide"),
	literalFingerprint("hermes_tools", "agent_tools"),
	literalFingerprint("data-hermes-send", "data-agent-send"),
	literalFingerprint("window.hermes.send", "window.agent.send"),
	literalFingerprint("never via Hermes bot", "never via the assistant bot"),

	// Remaining brand tokens. "Hermes Agent" is its own row so the generic
	// word row below cannot produce "the agent Agent".
	literalFingerprint("Hermes Agent", "the agent"),
	regexpFingerprint(`\bhermes\b`, "agent"),

	// OpenClaw identity.
	literalFingerprint(
		"You are a personal assistant running inside OpenClaw.",
		"You are a personal assistant.",
	),
}

// antigravityHermesWholeLineRewrites strips whole Hermes runtime prompt lines
// that individually trigger a false 429 RESOURCE_EXHAUSTED on the Antigravity
// backend (upstream decolua/9router@bb399170a). They run before the generic
// token-level fingerprint rows so a whole line is excised cleanly instead of
// leaving trailing fingerprint text behind.
var antigravityHermesWholeLineRewrites = []struct {
	from *regexp.Regexp
	to   string
}{
	{from: regexp.MustCompile(`(?m)^[ \t]*You are Hermes Agent, built by Nous Research\. Be direct:[^\n]*`), to: "You are an AI assistant."},
	{from: regexp.MustCompile(`(?m)^[ \t]*<!-- End Hermes runtime environment -->[^\n]*(?:\r?\n)?`), to: ""},
	{from: regexp.MustCompile(`(?m)^[ \t]*# Google model operational directives[^\n]*(?:\r?\n)?`), to: ""},
	{from: regexp.MustCompile(`(?m)^[ \t]*If a tool, install, or network call fails and blocks the real path[^\n]*(?:\r?\n)?`), to: ""},
	{from: regexp.MustCompile(`(?m)^[ \t]*When the user asks you to build, run, or verify something, the deliverable is a working artifact[^\n]*(?:\r?\n)?`), to: ""},
	{from: regexp.MustCompile(`(?m)^[ \t]*For help with Hermes Agent itself[^\n]*(?:\r?\n)?`), to: ""},
	{from: regexp.MustCompile(`(?m)^[ \t]*Mid-turn, the user can steer you: Hermes delivers their message[^\n]*(?:\r?\n)?`), to: ""},
}

func rewriteAntigravityHermesWholeLines(text string) string {
	for _, rewrite := range antigravityHermesWholeLineRewrites {
		text = rewrite.from.ReplaceAllString(text, rewrite.to)
	}
	return text
}

// rewriteAntigravityFingerprints replaces every known harness fingerprint with
// neutral wording. It is applied to system instructions and tool descriptions,
// the two prompt slots the backend scans for competing-client branding;
// conversation turns keep the caller's own vocabulary.
func rewriteAntigravityFingerprints(text string) string {
	if text == "" {
		return text
	}
	// Whole-line Hermes markers run first so the lines are excised cleanly
	// without leaving leftover tags (upstream decolua/9router@bb399170a).
	text = rewriteAntigravityHermesWholeLines(text)
	text = hermesIdentityRegex.ReplaceAllString(text, "You are an AI assistant.")
	for _, rewrite := range antigravityFingerprintRewrites {
		text = rewrite.from.ReplaceAllString(text, rewrite.to)
	}
	return text
}

// sanitizeAntigravityToolFingerprints rewrites fingerprinted harness text in
// tool descriptions. Descriptions are prompt text the model reads, and the
// backend scans them the same way it scans the system instruction, so a
// Hermes-tagged tool description can earn the same false 429. The input slice
// and its declarations are copied, never mutated, because StripCompetitivePrompts
// receives a caller-owned request.
func sanitizeAntigravityToolFingerprints(tools []GeminiTool) []GeminiTool {
	if len(tools) == 0 {
		return tools
	}
	out := make([]GeminiTool, len(tools))
	for i, tool := range tools {
		declarations := make([]GeminiFunctionDecl, len(tool.FunctionDeclarations))
		for j, declaration := range tool.FunctionDeclarations {
			description := rewriteAntigravityFingerprints(declaration.Description)
			declaration.Description = normalizeHarnessBrandMarkers(description)
			declarations[j] = declaration
		}
		out[i] = GeminiTool{FunctionDeclarations: declarations}
	}
	return out
}
