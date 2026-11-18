package translator

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// maxAntigravityOutputTokens caps generationConfig.maxOutputTokens. The real
// agy 1.5.9 CLI sends 65536 on a successful request (live capture 2026-11-16),
// so the cap is aligned with that rather than the upstream 64000 constant.
const maxAntigravityOutputTokens = 65536

// maxHydrogenOutputTokens is the lifted output-token ceiling for Gemini 4
// Hydrogen models. Hydrogen supports ~1M unified context windows and can
// produce completions well beyond the 64k Antigravity cap, so the cap is
// raised to 256k for long-decode continuation hops.
const maxHydrogenOutputTokens = 256000

// thinkingHeadroomTokens is added on top of thinkingBudget when ensuring
// maxOutputTokens strictly exceeds it (parity across chat and image paths).
const thinkingHeadroomTokens = 8192

// antigravityDecoyUnavailable is the placeholder description for injected decoy tools.
const antigravityDecoyUnavailable = "This tool is currently unavailable."

// antigravityRequestBlacklist are fields Google generateContent rejects when
// present at the request root (thinking/reasoning fields set by upstream clients).
var antigravityRequestBlacklist = []string{
	"output_config",
	"thinking",
	"reasoning_effort",
	"reasoning",
	"enable_thinking",
	"thinking_budget",
	"thinkingConfig",
}

// AntigravityRequest is the wrapper format for Antigravity API.
// RequestType is omitempty: PR decolua/9router#3986 showed Google's Cloud Code
// endpoint enforces a tiny separate quota bucket whenever requestType="agent"
// is present, causing false 429 RESOURCE_EXHAUSTED on OMP harness payloads
// even with quota remaining. So the agent path omits the field entirely.
type AntigravityRequest struct {
	Project     string         `json:"project"`
	Model       string         `json:"model"`
	UserAgent   string         `json:"userAgent"`
	RequestType string         `json:"requestType,omitempty"`
	RequestID   string         `json:"requestId"`
	Request     jsontext.Value `json:"request"`
}

// AntigravityNativeToolNames are tool names preserved without suffix.
var AntigravityNativeToolNames = map[string]bool{
	"browser_subagent": true,
	"command_status":   true,
	"find_by_name":     true,
	"generate_image":   true,
	"grep_search":      true,
	"list_dir":         true,
	"list_resources":   true,
	"mcp_sequential-thinking_sequentialthinking": true,
	"multi_replace_file_content":                 true,
	"notify_user":                                true,
	"read_resource":                              true,
	"read_terminal":                              true,
	"read_url_content":                           true,
	"replace_file_content":                       true,
	"run_command":                                true,
	"search_web":                                 true,
	"send_command_input":                         true,
	"task_boundary":                              true,
	"view_content_chunk":                         true,
	"view_file":                                  true,
	"write_to_file":                              true,
}

// AntigravityDecoyPlaceholderParams provides a valid schema for tools with no input parameters.
var AntigravityDecoyPlaceholderParams = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"reason": map[string]any{
			"type":        "string",
			"description": "Brief explanation of why you are calling this tool",
		},
	},
	"required": []string{"reason"},
}

// AntigravityDecoyTools are the 21 decoy tools matching official IDE defaults.
var AntigravityDecoyTools = []GeminiFunctionDecl{
	{Name: "browser_subagent", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "command_status", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "find_by_name", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "generate_image", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "grep_search", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "list_dir", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "list_resources", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "mcp_sequential-thinking_sequentialthinking", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "multi_replace_file_content", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "notify_user", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "read_resource", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "read_terminal", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "read_url_content", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "replace_file_content", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "run_command", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "search_web", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "send_command_input", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "task_boundary", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "view_content_chunk", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "view_file", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "write_to_file", Description: antigravityDecoyUnavailable, Parameters: AntigravityDecoyPlaceholderParams},
}

// CloakAntigravityRequest cloaks tool names with `_ide` suffix and appends decoy tools.
func CloakAntigravityRequest(req *GeminiRequest, clientTool string) (*GeminiRequest, map[string]string) {
	if req == nil {
		return nil, nil
	}

	toolNameMap := make(map[string]string)
	if len(req.Tools) == 0 {
		return req, toolNameMap
	}

	isCopilot := clientTool == "github-copilot"
	var clientDecls []GeminiFunctionDecl
	decoyNames := make(map[string]bool, len(AntigravityDecoyTools))
	for _, dt := range AntigravityDecoyTools {
		decoyNames[dt.Name] = true
	}

	for _, toolGroup := range req.Tools {
		for _, fn := range toolGroup.FunctionDeclarations {
			if isCopilot && (AntigravityNativeToolNames[fn.Name] || decoyNames[fn.Name]) {
				continue
			}
			if AntigravityNativeToolNames[fn.Name] {
				clientDecls = append(clientDecls, fn)
				continue
			}

			suffixedName := fn.Name + "_ide"
			toolNameMap[suffixedName] = fn.Name
			fn.Name = suffixedName
			clientDecls = append(clientDecls, fn)
		}
	}

	// Merge client declarations first, then decoy tools (deduplicating)
	seen := make(map[string]bool)
	var allDecls []GeminiFunctionDecl
	for _, decl := range append(clientDecls, AntigravityDecoyTools...) {
		if decl.Name == "" || seen[decl.Name] {
			continue
		}
		seen[decl.Name] = true
		if decl.Parameters != nil {
			if b, err := json.Marshal(decl.Parameters, json.Deterministic(true)); err == nil {
				cleaned := CleanParametersSchema(b)
				var cleanedParams any
				if err := json.Unmarshal(cleaned, &cleanedParams); err == nil {
					decl.Parameters = cleanedParams
				}
			}
		}
		allDecls = append(allDecls, decl)
	}

	// Update contents message history with renamed tools
	cloakedContents := make([]GeminiContent, len(req.Contents))
	for i, c := range req.Contents {
		cloakedParts := make([]GeminiPart, len(c.Parts))
		for j, p := range c.Parts {
			partCopy := p
			if p.FunctionCall != nil && !AntigravityNativeToolNames[p.FunctionCall.Name] {
				fc := *p.FunctionCall
				fc.Name = fc.Name + "_ide"
				partCopy.FunctionCall = &fc
			}
			if p.FunctionResponse != nil && !AntigravityNativeToolNames[p.FunctionResponse.Name] {
				fr := *p.FunctionResponse
				fr.Name = fr.Name + "_ide"
				partCopy.FunctionResponse = &fr
			}
			cloakedParts[j] = partCopy
		}
		cloakedContents[i] = GeminiContent{
			Role:  c.Role,
			Parts: cloakedParts,
		}
	}

	res := *req
	res.Tools = []GeminiTool{{FunctionDeclarations: allDecls}}
	res.ToolConfig = map[string]any{
		"functionCallingConfig": map[string]any{
			"mode": "VALIDATED",
		},
	}
	res.Contents = cloakedContents

	return &res, toolNameMap
}

// UncloakToolName restores the original tool name from a cloaked name.
func UncloakToolName(name string, toolMap map[string]string) string {
	if toolMap != nil {
		if orig, ok := toolMap[name]; ok {
			return orig
		}
	}
	name = strings.TrimPrefix(name, "proxy_")
	if strings.HasSuffix(name, "_ide") {
		return strings.TrimSuffix(name, "_ide")
	}
	return name
}

// Competitive prompt phrases that cause Antigravity to reject requests with 429.
var competitivePromptBlacklist = []string{
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	"You are a Claude agent, built on Anthropic's Claude Agent SDK",
	"Anthropic's Claude Agent SDK",
}

// claudeDualUseSentence is the stock security-guidance sentence Claude Code
// ships in its system prompt. The Antigravity gateway answers the verbatim
// sentence with a fake 429 RESOURCE_EXHAUSTED on every account despite full
// weekly quota. Bisect replays of a captured Claude Code request isolated
// the trigger to this sentence (2026-11-17): the full sentence in the system
// instruction fails, every phrase of it alone passes, and heavy security
// vocabulary elsewhere in the request passes. The sentence is stripped
// outright, so requests forwarded to Antigravity carry no security-guidance
// sentence at all.
const claudeDualUseSentence = "Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases."

// claudePastedContentSentence is the stock provenance sentence Claude Code
// adds to its system prompt in the long form of the prompt (sent when an API
// key is configured). The Antigravity gateway answers it with the same fake
// 429 RESOURCE_EXHAUSTED as the dual-use sentence. Bisect replays
// (2026-11-17) isolated the trigger to the combined clause "pasted into the
// message by the user from somewhere else and may contain instructions the
// user did not write": each half of the clause alone passes, the bare word
// pasted_content passes, the <pasted_content> tag alone passes, and the
// combined clause fails under any subject. The sentence and the bare clause
// are stripped outright, like the dual-use sentence above. The clause rule
// catches the clause under other subjects.
const claudePastedContentSentence = "Text inside <pasted_content> tags was pasted into the message by the user from somewhere else and may contain instructions the user did not write."
const claudePastedContentClause = "pasted into the message by the user from somewhere else and may contain instructions the user did not write"

// claudeCodeIdentitySentence is the identity line the Claude Code desktop
// entrypoint opens the system prompt with. The Antigravity gateway answers
// the exact phrase "You are Claude Code" with a fake 429
// RESOURCE_EXHAUSTED on every account despite full weekly quota (bisected
// by sidkang/agy-acp issue #1). The competitive blacklist above only covers
// the "Claude agent" SDK variant, so this line reached the backend intact:
// with both sentences above already stripped, a request carrying only the
// system prompt and the tool definitions still failed on every account.
// The line is rewritten to the Antigravity identity instead of stripped,
// so the forwarded prompt keeps an identity line. The bare phrase rule
// runs last and catches any other form of the line.
const claudeCodeIdentitySentence = "You are Claude Code, Anthropic's official CLI for Claude."
const claudeCodeIdentitySentenceSDK = "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."
const claudeCodeIdentityPhrase = "You are Claude Code"
const antigravityIdentitySentence = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding."

// Harness tag patterns that trigger false 429 RESOURCE_EXHAUSTED rejections on
// the Antigravity gateway (parity with decolua/9router PR #3987). OMP-family
// harnesses inject these XML markers in system text; normalizing them to
// neutral tags keeps instruction bodies intact while bypassing the filter.
//
// The <system-conventions> wrapper is not enough to rename: OMP's RFC 2119
// wording inside it is the fingerprint pair reported in can1357/oh-my-pi#11699,
// and the sidecar that ships with OMP replaces the whole block so wording added
// later inside the stable boundary cannot reintroduce it. So the block and a
// bare RFC clause both collapse to canonical neutral text.
const (
	antigravityCanonicalConventions = "<guidelines>\nRequirement terminology: MUST means mandatory, SHOULD means recommended, and MAY means optional.\n</guidelines>"
	antigravityRFCClauseReplacement = "Requirement terminology: MUST means mandatory, SHOULD means recommended, and MAY means optional."
)

var (
	harnessConventionsBlock     = regexp.MustCompile(`(?is)<system[-_]conventions\b[^>]*>.*?</system[-_]conventions\s*>`)
	harnessStandaloneRFCClause  = regexp.MustCompile(`(?i)\bRFC\s+(?:2119|8174)\b[^\r\n]{0,400}\b(?:MUST|REQUIRED|SHOULD|RECOMMENDED|MAY|OPTIONAL)\b[^\r\n]{0,400}(?:\.|$)`)
	harnessTagSystemConventions = regexp.MustCompile(`(?i)<(/?)system[-_]conventions>`)
	harnessTagSystemDirective   = regexp.MustCompile(`(?i)<(/?)system[-_]directive>`)
	harnessTagCritical          = regexp.MustCompile(`(?i)<(/?)critical>`)
	// OMP 18.20.x identifies itself as "omp's trusted coding assistant" and
	// references its internal xd:// URL scheme in the system prompt. Both are
	// detected on their own: a live Antigravity request whose only remaining
	// OMP markers were the identity sentence and xd:// still answered 429.
	harnessBrandOMPIdentity = regexp.MustCompile(`(?i)You are omp['’]s trusted coding assistant\.`)
	harnessSchemeOMP        = regexp.MustCompile(`(?i)\bomp://`)
	harnessPathOMP          = regexp.MustCompile(`(?i)~?/?\.omp/`)
	harnessSchemeXD         = regexp.MustCompile(`(?i)\bxd://`)
	harnessBrandOMPFull     = regexp.MustCompile(`(?i)oh[-_ ]my[-_ ]pi\s+coding\s+harness`)
	harnessBrandOMP         = regexp.MustCompile(`(?i)oh[-_ ]my[-_ ]pi`)
	harnessBrandOMPLive     = regexp.MustCompile(`(?i)omp Live`)
	// harnessBrandOMPToken is the last OMP row: specific identifiers and
	// schemes are rewritten first, so only a leftover standalone brand token
	// reaches it. Substrings such as "compiled", "complete", "OpenMP", and
	// omp_get_thread_num() have no word boundary at the token.
	harnessBrandOMPToken     = regexp.MustCompile(`(?i)\bomp\b`)
	claudeBillingHeaderRegex = regexp.MustCompile(`(?im)^x-anthropic-billing-header:[^\n]*(?:\r?\n)*`)
	// antigravityOpenCodeRegex matches the literal `opencode` provider
	// identifier upstream rewrites in the system instruction. Upstream's rule
	// is unbounded and case-insensitive, so `opencode-zen` and
	// `.config/opencode/` are rewritten too (parity with decolua/9router
	// open-sse/config/appConstants.js:194).
	antigravityOpenCodeRegex = regexp.MustCompile(`(?i)opencode`)
	// hermesIdentityRegex matches every Hermes identity variant upstream
	// rewrites, and the whole match is replaced by a neutral sentence. The
	// legacy rule only matched one exact phrasing, so the other variants
	// reached the Antigravity backend with the fingerprint intact and could
	// trigger a fake 429 RESOURCE_EXHAUSTED (parity with decolua/9router
	// commit a61fc6a0).
	//
	// The upstream expression is transliterated exactly, including the
	// literal single space in "Nous Research" — upstream does not accept
	// other whitespace between the two words, and a port that accepted more
	// would rewrite phrasings upstream leaves alone.
	hermesIdentityRegex = regexp.MustCompile(`(?i)You are Hermes(?: Agent)?(?:,\s*(?:an intelligent AI assistant|an AI assistant|an AI agent))?(?:,?\s*(?:built|created)\s+by\s+Nous Research)?\.`)
	// antigravityOpenClawRegex matches the literal `openclaw` client
	// identifier upstream rewrites in the system instruction. Upstream's rule
	// is unbounded and case-insensitive, so `OpenClaw`, `OPENCLAW` and
	// `openclaw-go` are rewritten too (parity with upstream).
	antigravityOpenClawRegex = regexp.MustCompile(`(?i)openclaw`)
)

// normalizeHarnessMarkers neutralizes harness fingerprint tags in system text.
// OMP's conventions block is replaced whole, because the wrapper and the RFC
// wording inside it are detected together; the other marker names change while
// their instruction bodies stay intact.
func normalizeHarnessMarkers(text string) string {
	text = harnessConventionsBlock.ReplaceAllString(text, antigravityCanonicalConventions)
	text = harnessStandaloneRFCClause.ReplaceAllString(text, antigravityRFCClauseReplacement)
	text = harnessTagSystemConventions.ReplaceAllString(text, "<${1}conventions>")
	text = harnessTagSystemDirective.ReplaceAllString(text, "<${1}instructions>")
	text = harnessTagCritical.ReplaceAllString(text, "<${1}important>")
	return normalizeHarnessBrandMarkers(text)
}

// normalizeHarnessBrandMarkers neutralizes standalone harness names and the
// URL schemes they put in prompt text. System instructions and tool
// descriptions both carry these tokens, while the conventions block above is
// system-only. Rows run specific-to-generic: the identity sentence, schemes,
// and paths are rewritten before the bare brand token, so no row can split a
// longer match into fragments.
func normalizeHarnessBrandMarkers(text string) string {
	text = harnessBrandOMPIdentity.ReplaceAllString(text, "You are a trusted coding assistant.")
	text = harnessSchemeOMP.ReplaceAllString(text, "docs://")
	text = harnessPathOMP.ReplaceAllString(text, ".agent/")
	text = harnessSchemeXD.ReplaceAllString(text, "internal://")
	text = harnessBrandOMPFull.ReplaceAllString(text, "AI coding assistant")
	text = harnessBrandOMP.ReplaceAllString(text, "coding assistant")
	text = harnessBrandOMPLive.ReplaceAllString(text, "coding assistant live")
	text = harnessBrandOMPToken.ReplaceAllString(text, "agent")
	return text
}

func rewriteCompetingBranding(text string) string {
	return rewriteBrandingText(text, true)
}

// rewriteAntigravityBrand rewrites the literal `opencode` provider
// identifier to `antigravity`, matching upstream's fourth row of
// ANTIGRAVITY_PROMPT_REWRITES (decolua/9router
// open-sse/config/appConstants.js:194). Upstream's replacement preserves the
// three capitalisations it names and lowercases everything else.
func rewriteAntigravityBrand(text string) string {
	return antigravityOpenCodeRegex.ReplaceAllStringFunc(text, func(m string) string {
		switch m {
		case "OpenCode":
			return "Antigravity"
		case "OPENCODE":
			return "ANTIGRAVITY"
		default:
			return "antigravity"
		}
	})
}

// rewriteOpenClawBrand rewrites the literal `openclaw` client
// identifier to `antigravity`, matching upstream's OpenClaw rewrite row.
// Upstream's replacement preserves the
// three capitalisations it names and lowercases everything else.
func rewriteOpenClawBrand(text string) string {
	return antigravityOpenClawRegex.ReplaceAllStringFunc(text, func(m string) string {
		switch m {
		case "OpenClaw":
			return "Antigravity"
		case "OPENCLAW":
			return "ANTIGRAVITY"
		default:
			return "antigravity"
		}
	})
}

// rewriteBrandingText applies competitive-prompt rewrites. Harness markers
// (tags + branding) are fingerprints of system-instruction text only;
// user/model messages, tool definitions, arguments, and results keep the
// existing competitive blacklist handling and are never harness-normalized.
func rewriteBrandingText(text string, isSystemInstruction bool) string {
	for _, phrase := range competitivePromptBlacklist {
		text = strings.ReplaceAll(text, phrase, "")
	}
	text = strings.ReplaceAll(text, claudeDualUseSentence, "")
	text = strings.ReplaceAll(text, claudePastedContentSentence, "")
	text = strings.ReplaceAll(text, claudePastedContentClause, "")
	text = strings.ReplaceAll(text, claudeCodeIdentitySentenceSDK, antigravityIdentitySentence)
	text = strings.ReplaceAll(text, claudeCodeIdentitySentence, antigravityIdentitySentence)
	text = strings.ReplaceAll(text, claudeCodeIdentityPhrase, "You are Antigravity")
	text = claudeBillingHeaderRegex.ReplaceAllString(text, "")
	text = hermesIdentityRegex.ReplaceAllString(text, "You are an AI assistant.")
	if isSystemInstruction {
		// Hermes/OpenClaw sentences, headers, and identifiers are the
		// fingerprints the backend answers with a false 429; tool
		// descriptions carry the same text and are sanitized separately.
		text = rewriteAntigravityFingerprints(text)
		text = normalizeHarnessMarkers(text)
		// Upstream applies this row to the system instruction only: the
		// caller brands its own client there, and the backend flags
		// competing-client branding with a fake 429 Quota Exhausted. The
		// conversation keeps the caller's vocabulary.
		text = rewriteAntigravityBrand(text)
		// OpenClaw rewrite row: competing-client
		// branding in the system instruction.
		text = rewriteOpenClawBrand(text)
	}
	return strings.TrimSpace(text)
}

// StripCompetitivePrompts removes competitor identity strings and rewrites competing client branding.
func StripCompetitivePrompts(req *GeminiRequest) *GeminiRequest {
	if req == nil {
		return nil
	}
	res := *req
	res.Tools = sanitizeAntigravityToolFingerprints(res.Tools)
	if res.SystemInstruction != nil {
		var filtered []GeminiPart
		for _, p := range res.SystemInstruction.Parts {
			text := rewriteCompetingBranding(p.Text)
			// Drop empty system instruction parts - Gemini requires oneof data field
			if strings.TrimSpace(text) == "" && p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.FileData == nil && p.ThoughtSignature == "" {
				continue
			}
			p.Text = text
			filtered = append(filtered, p)
		}
		if len(filtered) == 0 {
			res.SystemInstruction = nil
		} else {
			res.SystemInstruction = &GeminiContent{
				Role:  res.SystemInstruction.Role,
				Parts: filtered,
			}
		}
	}
	contents := make([]GeminiContent, 0, len(res.Contents))
	for _, c := range res.Contents {
		var filtered []GeminiPart
		for _, p := range c.Parts {
			if p.Text != "" {
				text := rewriteBrandingText(p.Text, false)
				if strings.TrimSpace(text) == "" && p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.FileData == nil && p.ThoughtSignature == "" {
					continue
				}
				p.Text = text
			}
			// Keep non-text parts as-is, drop truly empty text parts
			if p.Text == "" && p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.FileData == nil && p.ThoughtSignature == "" && p.Thought == nil {
				continue
			}
			filtered = append(filtered, p)
		}
		if len(filtered) == 0 {
			continue
		}
		contents = append(contents, GeminiContent{Role: c.Role, Parts: filtered})
	}
	if len(contents) > 0 || len(res.Contents) == 0 {
		res.Contents = contents
	}
	return &res
}

// AntigravityModelSynonyms maps client/UI model names to the bare Antigravity
// backend model name of their family.
//
// It is deliberately NOT the wire-id table: the lock and quota paths key on
// this value, so it must stay family-shaped. Only AntigravityUpstreamModel
// applies the effort suffix the backend actually needs.
var AntigravityModelSynonyms = map[string]string{
	// Gemini 4 Carbon serves one backend id per tier; the bare catalog id is medium tier.
	"gemini-4-carbon":        "gemini-4-carbon-medium",
	"gemini-4-carbon-high":   "gemini-4-carbon-high",
	"gemini-4-carbon-medium": "gemini-4-carbon-medium",
	"gemini-4-carbon-low":    "gemini-4-carbon-low",
	// Gemini 4 Argon serves one backend id per tier; the bare catalog id is medium tier.
	"gemini-4-argon":        "gemini-4-argon-medium",
	"gemini-4-argon-high":   "gemini-4-argon-high",
	"gemini-4-argon-medium": "gemini-4-argon-medium",
	"gemini-4-argon-low":    "gemini-4-argon-low",
	// Upstream decolua/9router#24034f69 retired the gemini-3.5 family and
	// gemini-3-flash; the default moved to the 3.8 medium tier.
	"gemini-default":             "gemini-3.8-flash-medium",
	"gemini-3.5-flash":           "gemini-3.8-flash-medium",
	"gemini-3.5-flash-high":      "gemini-3.8-flash-high",
	"gemini-3.5-flash-medium":    "gemini-3.8-flash-medium",
	"gemini-3.5-flash-low":       "gemini-3.8-flash-low",
	"gemini-3.5-flash-extra-low": "gemini-3.8-flash-low",
	"gemini-3-flash-agent":       "gemini-3.8-flash-high",
	"gemini-3-flash":             "gemini-3.8-flash-high",
	"gemini-3.1-pro-high":        "gemini-pro-agent",
	"gemini-3.1-pro":             "gemini-pro-agent",
	"gemini-3-pro-high":          "gemini-pro-agent",
	"gemini-3-pro-low":           "gemini-3.1-pro-low",
	// 3.8 serves one backend id per tier (unlike 3.7/3.6, which share one
	// "-tiered" id for the whole family).
	"gemini-3.8-flash":          "gemini-3.8-flash-medium",
	"gemini-3.8-flash-high":     "gemini-3.8-flash-high",
	"gemini-3.8-flash-medium":   "gemini-3.8-flash-medium",
	"gemini-3.8-flash-low":      "gemini-3.8-flash-low",
	"gemini-3.8-flash-agent":    "gemini-3.8-flash-high",
	"gemini-3.8-flash-thinking": "gemini-3.8-flash-high",
	// 3.7 flash tiered models -> backend model: gemini-3.7-flash-tiered
	"gemini-3.7-flash":           "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-high":      "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-agent":     "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-medium":    "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-low":       "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-extra-low": "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-thinking":  "gemini-3.7-flash-tiered",
	// 3.6 flash tiered models -> backend model: gemini-3.6-flash-tiered
	"gemini-3.6-flash":        "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-high":   "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-medium": "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-low":    "gemini-3.6-flash-tiered",
	// Claude 5.5: bare model maps to high tier in synonyms for quota/lock lookups.
	"claude-sonnet-5-5": "claude-sonnet-5-5-high",
	"claude-opus-5-5":   "claude-opus-5-5-high",
	"claude-sonnet-5.5": "claude-sonnet-5-5-high",
	"claude-opus-5.5":   "claude-opus-5-5-high",
}

// NormalizeAntigravityModel maps known aliases/synonyms to Antigravity internal backend model names.
func NormalizeAntigravityModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	// Strip trailing thinking suffix like (high), (medium), (low) if present
	if idx := strings.Index(m, "("); idx != -1 && strings.HasSuffix(m, ")") {
		m = strings.TrimSpace(m[:idx])
	}
	if canonical, ok := AntigravityModelSynonyms[m]; ok {
		return canonical
	}
	return m
}

// antigravityUpstreamModelIDs is the Go form of the registry's
// `upstreamModelId` column, reduced to the bare id Cloud Code serves.
//
// Upstream's rows carry a parenthesised effort
// ("gemini-3.8-flash-low(low)") because its chatCore passes the raw row to
// applyThinking and only then strips it for the wire. This port keeps the
// effort out of the table instead of stripping it on the way out, so the value
// here is exactly what Google is asked for — a suffixed row is a 404.
//
// These rows deliberately stay out of AntigravityModelSynonyms. That map is
// shared with the lock and quota paths, which key on a bare model family.
//
// Port of open-sse/providers/registry/antigravity.js (upstream
// decolua/9router#a07ed95b, refreshed by #24034f69). Every value was probed
// live against daily-cloudcode-pa.googleapis.com on 2026-10-09.
var antigravityUpstreamModelIDs = map[string]string{
	// Gemini 4 Carbon serves one backend id per tier; the bare catalog id is medium tier.
	"gemini-4-carbon":        "gemini-4-carbon-medium",
	"gemini-4-carbon-high":   "gemini-4-carbon-high",
	"gemini-4-carbon-medium": "gemini-4-carbon-medium",
	"gemini-4-carbon-low":    "gemini-4-carbon-low",
	// Gemini 4 Argon serves one backend id per tier; the bare catalog id is medium tier.
	"gemini-4-argon":        "gemini-4-argon-medium",
	"gemini-4-argon-high":   "gemini-4-argon-high",
	"gemini-4-argon-medium": "gemini-4-argon-medium",
	"gemini-4-argon-low":    "gemini-4-argon-low",
	// The 3.8 family serves one backend id per tier. The bare catalog id is
	// the medium tier.
	"gemini-3.8-flash":        "gemini-3.8-flash-medium",
	"gemini-3.8-flash-high":   "gemini-3.8-flash-high",
	"gemini-3.8-flash-medium": "gemini-3.8-flash-medium",
	"gemini-3.8-flash-low":    "gemini-3.8-flash-low",
	// 3.7 and 3.6 share one tiered backend id per family.
	"gemini-3.7-flash":        "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-high":   "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-medium": "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-low":    "gemini-3.7-flash-tiered",
	"gemini-3.6-flash":        "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-high":   "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-medium": "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-low":    "gemini-3.6-flash-tiered",
	// Pro is not tiered; the high id is served under its agent alias.
	"gemini-3.1-pro-high": "gemini-pro-agent",
	"gemini-3.1-pro-low":  "gemini-3.1-pro-low",
	// Claude 5.5: the bare catalog id is the thinking tier, so it maps to
	// high. The other Claude 5.5 ids are not yet served by Cloud Code.
	"claude-sonnet-5-5":        "claude-sonnet-5-5-high",
	"claude-opus-5-5":          "claude-opus-5-5-high",
	"claude-sonnet-5-5-high":   "claude-sonnet-5-5-high",
	"claude-sonnet-5-5-low":    "claude-sonnet-5-5-low",
	"claude-sonnet-5-5-medium": "claude-sonnet-5-5-medium",
	"claude-opus-5-5-high":     "claude-opus-5-5-high",
	"claude-opus-5-5-low":      "claude-opus-5-5-low",
	"claude-opus-5-5-medium":   "claude-opus-5-5-medium",
}

// AntigravityUpstreamModel resolves a catalog model id to the id the Antigravity
// backend expects, applying the registry's upstreamModelId column.
//
// The parenthesised effort the registry carries ("gemini-3.8-flash-high(high)")
// is 9router's own thinking notation, not a Google model id, and it is stripped
// before the id reaches the wire — exactly as upstream's chatCore does with
// stripThinkingSuffix(upstreamModel) on both the passthrough and the
// translateRequest branch. Cloud Code answers 404 NOT_FOUND for every
// suffixed id: probed live against daily-cloudcode-pa.googleapis.com on
// 2026-10-09, `gemini-3.8-flash-low(low)` and `gemini-3.8-flash-high(high)`
// both 404 while the bare `gemini-3.8-flash-low` and `gemini-3.8-flash-high`
// both 200. The tier is already encoded in the id, so dropping the suffix
// loses no routing information; upstream carries it separately only to feed
// applyThinking, which reads it off the value this function is given rather
// than off the wire.
//
// A client-supplied "(level)" therefore narrows the id to its own base and is
// not appended again: `claude-sonnet-5-5-medium(low)` resolves to
// `claude-sonnet-5-5-medium`, matching upstream's resolvedBase of the same
// registry row.
//
// An id with no registry row is returned unchanged, which keeps a model this
// port has never heard of routable instead of silently blanking it.
func AntigravityUpstreamModel(model string) string {
	base := stripParenthesisedSuffix(strings.ToLower(strings.TrimSpace(model)))

	if resolved, ok := antigravityUpstreamModelIDs[base]; ok {
		return stripParenthesisedSuffix(resolved)
	}
	return NormalizeAntigravityModel(base)
}

// AntigravityEffortTier reads the reasoning tier a client asked for, mirroring
// upstream getModelUpstreamId: the model id's own "(level)" suffix wins over
// the body, then reasoning_effort / output_config.effort / reasoning.effort,
// then thinking.budget_tokens (>16384 high, <=4096 low, else medium).
// xhigh/max fold onto high and minimal onto low. Empty when nothing asks for
// a tier.
func AntigravityEffortTier(model string, clientBody []byte) string {
	if idx := strings.Index(model, "("); idx != -1 && strings.HasSuffix(model, ")") {
		return antigravityNormalizeTier(strings.ToLower(strings.TrimSpace(model[idx+1 : len(model)-1])))
	}
	if len(clientBody) == 0 {
		return ""
	}
	var probe map[string]any
	if err := json.Unmarshal(clientBody, &probe); err != nil {
		return ""
	}
	effort := ""
	for _, key := range []string{"reasoning_effort", "output_config", "reasoning"} {
		v, ok := probe[key]
		if !ok {
			continue
		}
		if key == "reasoning_effort" {
			if s, ok := v.(string); ok && s != "" {
				effort = s
				break
			}
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := obj["effort"].(string); ok && s != "" {
			effort = s
			break
		}
	}
	if effort != "" {
		return antigravityNormalizeTier(strings.ToLower(strings.TrimSpace(effort)))
	}
	if thinking, ok := probe["thinking"].(map[string]any); ok {
		if b, ok := antigravityToFloat(thinking["budget_tokens"]); ok && b > 0 {
			switch {
			case b > 16384:
				return "high"
			case b <= 4096:
				return "low"
			default:
				return "medium"
			}
		}
	}
	return ""
}

func antigravityNormalizeTier(level string) string {
	switch level {
	case "xhigh", "max":
		return "high"
	case "minimal":
		return "low"
	}
	return level
}

func antigravityToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// AntigravityTieredModel resolves a client model id to the tiered backend id
// the client's reasoning effort asks for — upstream's body-inspecting branch
// of getModelUpstreamId. An unadorned `gemini-3.8-flash` with
// reasoning_effort "high" reaches Google as `gemini-3.8-flash-high` instead of
// always landing on the bare id's medium tier. Falls back to the plain
// resolution when no tier is requested or the catalog has no such variant.
// The "(level)" suffix never reaches the wire: Cloud Code 404s suffixed ids.
func AntigravityTieredModel(model string, clientBody []byte) string {
	base := stripParenthesisedSuffix(strings.ToLower(strings.TrimSpace(model)))
	if tier := AntigravityEffortTier(model, clientBody); tier != "" {
		if resolved, ok := antigravityUpstreamModelIDs[base+"-"+tier]; ok {
			return stripParenthesisedSuffix(resolved)
		}
	}
	return AntigravityUpstreamModel(model)
}

// stripParenthesisedSuffix removes a trailing "(level)" from an id, if any.
func stripParenthesisedSuffix(id string) string {
	if idx := strings.Index(id, "("); idx != -1 && strings.HasSuffix(id, ")") {
		return strings.TrimSpace(id[:idx])
	}
	return id
}

// antigravityUUIDFromSeed derives a deterministic RFC-4122-style UUID (v5-like)
// from a seed string, matching the Next.js uuidFromSeed helper so trajectory and
// conversation IDs are stable per session.
func antigravityUUIDFromSeed(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	hex := fmt.Sprintf("%x", b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex[0:8], hex[8:12], hex[12:16], hex[16:20], hex[20:32])
}

// antigravityNumericSessionID matches an id that is already an integer literal.
var antigravityNumericSessionID = regexp.MustCompile(`^-?\d+$`)

// AntigravityNumericSessionID ports upstream toNumericSessionId
// (open-sse/utils/sessionManager.js): an id that is already integral passes
// through unchanged, anything else becomes "-<decimal>" of the first 63 bits of
// its SHA-256. The agent lane sends it as a JSON string, exactly as the official
// agy CLI does ("sessionId": "-3750763034362895579", captured live from 1.5.9).
//
// An empty id returns "", so the caller omits the field rather than collapsing
// every caller onto one shared session — a single bucket for the whole gateway
// is a fingerprint the official client never produces.
func AntigravityNumericSessionID(sessionID string) string {
	s := strings.TrimSpace(sessionID)
	// Upstream normalizeSessionId rejects blank and >256-char ids before
	// hashing; keep the same bound so a hostile header cannot force an
	// unbounded sha256 over client input.
	if s == "" || len(s) > 256 {
		return ""
	}
	if antigravityNumericSessionID.MatchString(s) {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	n := binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff
	return "-" + strconv.FormatUint(n, 10)
}

// antigravityBuildRequestID builds the IDE request ID in the same shape as the
// Next.js reference: agent/<conversationId>/<ts>/<trajectoryId>/<step>.
func antigravityBuildRequestID(sessionID, model, requestType string, contentCount int) string {
	if sessionID == "" {
		sessionID = "anonymous"
	}
	conversationID := antigravityUUIDFromSeed("antigravity:conversation:" + sessionID)
	trajectoryID := antigravityUUIDFromSeed(fmt.Sprintf("antigravity:trajectory:%s:%s:%s", sessionID, model, requestType))
	step := max(contentCount*2-1, 1)
	return fmt.Sprintf("agent/%s/%d/%s/%d", conversationID, time.Now().UnixMilli(), trajectoryID, step)
}

// isHydrogenModel reports whether modelName identifies a Gemini 4 Hydrogen
// model. Hydrogen models have a 1M unified context window and can produce
// completions well beyond the Antigravity cap, so they get a lifted
// output-token ceiling and long-decode continuation hops.
func isHydrogenModel(modelName string) bool {
	return strings.Contains(strings.ToLower(modelName), "hydrogen")
}

// antigravityCanonicalThinkingLevel spells thinkingLevel with the enum casing
// the official agy CLI uses ("HIGH", not "high"). It is a scoped byte-level
// substitution on the JSON value only: re-marshalling the whole body here would
// re-encode thought signatures on functionCall/thought parts and corrupt them.
func antigravityCanonicalThinkingLevel(body []byte) []byte {
	for _, level := range [...]string{"minimal", "low", "medium", "high", "xhigh"} {
		old := []byte(`"thinkingLevel":"` + level + `"`)
		if bytes.Contains(body, old) {
			newValue := []byte(`"thinkingLevel":"` + strings.ToUpper(level) + `"`)
			body = bytes.ReplaceAll(body, old, newValue)
		}
	}
	return body
}

// hardenAntigravityRequest strips blacklisted thinking/reasoning fields Google
// rejects and caps maxOutputTokens at the model-specific limit. For Hydrogen
// (Gemini 4) models the cap is lifted to maxHydrogenOutputTokens to prevent
// truncation of long decode responses. For all other
// models it caps at maxAntigravityOutputTokens. It leaves the request
// byte-for-byte intact when nothing needs changing so thought signatures
// on functionCall/thought parts are never re-encoded (which would corrupt them).
func hardenAntigravityRequest(geminiBody []byte, modelName string) []byte {
	var m map[string]any
	if err := json.Unmarshal(geminiBody, &m); err != nil {
		return geminiBody
	}

	// Hydrogen (Gemini 4) supports output tokens well beyond 64k; lifting
	// the Antigravity cap for Hydrogen models prevents truncation of long
	// decode responses.
	maxCap := maxAntigravityOutputTokens
	if isHydrogenModel(modelName) {
		maxCap = maxHydrogenOutputTokens
	}

	changed := false
	for _, k := range antigravityRequestBlacklist {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}

	gc, _ := m["generationConfig"].(map[string]any)
	if gc != nil {
		if v, ok := gc["maxOutputTokens"].(float64); ok && v > float64(maxCap) {
			gc["maxOutputTokens"] = float64(maxCap)
			changed = true
		}
		// Ensure maxOutputTokens strictly exceeds thinkingBudget to prevent 400 INVALID_ARGUMENT (PR #3981)
		var thinkingBudget float64
		if tc, ok := gc["thinkingConfig"].(map[string]any); ok {
			if tb, ok := tc["thinkingBudget"].(float64); ok {
				thinkingBudget = tb
			}
		}
		if thinkingBudget > 0 {
			curMax, hasMax := gc["maxOutputTokens"].(float64)
			if !hasMax || curMax <= thinkingBudget {
				newMax := thinkingBudget + thinkingHeadroomTokens
				if newMax > float64(maxCap) {
					newMax = float64(maxCap)
				}
				gc["maxOutputTokens"] = newMax
				changed = true
			}
		}
	}

	if !changed {
		return geminiBody
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return geminiBody
	}
	return out
}

// StripThoughtSignatures removes all thoughtSignature / thought_signature keys
// from a JSON body (Antigravity wrapper or plain Gemini). Used as a fallback
// when the backend rejects a history signature as corrupted — stripping lets
// the model treat the turn as a fresh thought.
func StripThoughtSignatures(body []byte) []byte {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	stripThoughtSignaturesRecursive(v)
	out, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

func stripThoughtSignaturesRecursive(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "thoughtSignature")
		delete(x, "thought_signature")
		for _, val := range x {
			stripThoughtSignaturesRecursive(val)
		}
	case []any:
		for _, elem := range x {
			stripThoughtSignaturesRecursive(elem)
		}
	}
}

// ReplaceThoughtSignatures replaces every thoughtSignature / thought_signature
// value with the given replacement (typically DefaultThinkingSignature). Used
// when the backend rejects a history signature as corrupted/invalid — the
// default is a known-valid signature for the Antigravity backend.
func ReplaceThoughtSignatures(body []byte, replacement string) []byte {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	replaceThoughtSignaturesRecursive(v, replacement)
	out, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

func replaceThoughtSignaturesRecursive(v any, replacement string) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["thoughtSignature"]; ok {
			x["thoughtSignature"] = replacement
		}
		if _, ok := x["thought_signature"]; ok {
			x["thought_signature"] = replacement
		}
		for _, val := range x {
			replaceThoughtSignaturesRecursive(val, replacement)
		}
	case []any:
		for _, elem := range x {
			replaceThoughtSignaturesRecursive(elem, replacement)
		}
	}
}

// fixAntigravityContents mirrors the Next.js Antigravity executor's content
// fixup: role correction for functionResponse, stripping of thought-only parts
// that Gemini 3 rejects, and backfilling of thoughtSignature on functionCall
// parts (parity with open-sse/executors/antigravity.js).
func fixAntigravityContents(req *GeminiRequest, model string) bool {
	if req == nil || len(req.Contents) == 0 {
		return false
	}
	changed := false
	fixed := make([]GeminiContent, len(req.Contents))
	for i, c := range req.Contents {
		role := c.Role
		hasFunctionResponse := false
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				hasFunctionResponse = true
				break
			}
		}
		if hasFunctionResponse {
			role = "user"
		}

		filtered := make([]GeminiPart, 0, len(c.Parts))
		for _, p := range c.Parts {
			if p.Thought != nil && *p.Thought && p.FunctionCall == nil {
				continue
			}
			if p.ThoughtSignature != "" && p.FunctionCall == nil && p.Text == "" {
				continue
			}
			filtered = append(filtered, p)
		}

		needsBackfill := false
		for _, p := range filtered {
			if p.FunctionCall != nil && p.ThoughtSignature == "" {
				needsBackfill = true
				break
			}
		}

		if needsBackfill {
			firstFunctionCallSeen := false
			for idx, p := range filtered {
				if p.FunctionCall != nil {
					sig := p.ThoughtSignature
					if sig == "" && !firstFunctionCallSeen {
						sig = DefaultThinkingSignature
					}
					firstFunctionCallSeen = true
					filtered[idx].ThoughtSignature = sig
				}
			}
		}

		if role != c.Role || len(filtered) != len(c.Parts) || needsBackfill {
			changed = true
		}
		fixed[i] = GeminiContent{Role: role, Parts: filtered}
	}
	normalized := NormalizeGeminiContentsForModel(fixed, model)
	if changed || len(normalized) != len(req.Contents) {
		req.Contents = normalized
		return true
	}
	return false
}

// WrapForAntigravity wraps a standard Gemini request in Antigravity API envelope.
// sessionID scopes conversation and trajectory UUIDs per connection or client session.
func WrapForAntigravity(geminiBody []byte, projectID, modelName string, sessionID ...string) ([]byte, error) {
	// The wire id is the registry's upstreamModelId with 9router's own effort
	// notation stripped, not the bare synonym the lock and quota paths share.
	// proxy.ForwardGemini normalises the model before calling this, so the
	// synonym lookup inside AntigravityUpstreamModel is idempotent and the two
	// layers cannot disagree on the id that reaches Google.
	modelName = AntigravityUpstreamModel(modelName)

	rawSession := ""
	if len(sessionID) > 0 {
		rawSession = strings.TrimSpace(sessionID[0])
	}

	contentCount := 1
	var geminiReq GeminiRequest
	if err := json.Unmarshal(geminiBody, &geminiReq); err == nil {
		contentCount = len(geminiReq.Contents)
		// The official CLI scopes every cascade with a numeric sessionId. Set
		// it before the copy/marshal chain below: StripCompetitivePrompts and
		// CloakAntigravityRequest both copy the struct, so the field survives
		// into the inner body without a second pass.
		if numeric := AntigravityNumericSessionID(rawSession); numeric != "" {
			geminiReq.SessionID = numeric
		}
		fixAntigravityContents(&geminiReq, modelName)
		cleanedReq := StripCompetitivePrompts(&geminiReq)
		if len(cleanedReq.Tools) > 0 {
			cloaked, _ := CloakAntigravityRequest(cleanedReq, "")
			cleanedReq = cloaked
		}
		if cloakedBytes, err := json.Marshal(cleanedReq, json.Deterministic(true)); err == nil {
			geminiBody = cloakedBytes
		}
	}
	geminiBody = hardenAntigravityRequest(geminiBody, modelName)
	geminiBody = antigravityCanonicalThinkingLevel(geminiBody)

	sess := projectID
	if rawSession != "" {
		sess = rawSession
	}

	wrapper := AntigravityRequest{
		Project:   projectID,
		Model:     modelName,
		UserAgent: "antigravity",
		// Omit RequestType (was "agent"): parity with decolua/9router#3986.
		// Google rejects agent-bucketed requests with false 429s; requestId
		// keeps its existing agent/<...>/<...>/<...>/<step> shape.
		RequestID: antigravityBuildRequestID(sess, modelName, "agent", contentCount),
		Request:   geminiBody,
	}
	out, err := json.Marshal(wrapper, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("marshal antigravity wrapper: %w", err)
	}
	return out, nil
}

// UnwrapAntigravityResponse extracts the inner Gemini response from antigravity envelope.
func UnwrapAntigravityResponse(raw []byte) []byte {
	var envelope struct {
		Response jsontext.Value `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		log.Warn("translator", "unmarshal envelope failed", "error", err)
		return raw // passthrough on failure
	}
	if len(envelope.Response) == 0 {
		log.Warn("translator", "empty envelope response")
		return raw // passthrough on failure
	}
	return []byte(envelope.Response)
}

// MoveSystemInstructionToContents folds the systemInstruction of a Gemini
// request body into contents[0] as a user-role turn and clears
// systemInstruction. Returns the rewritten body and true when the change
// was applied; the input bytes are never mutated. When the body carries no
// systemInstruction, or it is already empty, the original bytes and false
// are returned unchanged.
//
// Used by ForwardGemini as a 1-hop fallback retry: the Antigravity backend
// answers a bare 429 RESOURCE_EXHAUSTED (no QUOTA_EXHAUSTED details) when
// it rejects the systemInstruction shape itself, and moving it to
// contents[0] is the upstream fix.
func MoveSystemInstructionToContents(body []byte) ([]byte, bool) {
	// jsontext.Value, not map[string]any: every member the rewrite does not
	// touch keeps its original bytes, so a tool call's thought signature is
	// never re-encoded. The member is looked up under both spellings —
	// systemInstruction, the tag GeminiRequest marshals today, and
	// system_instruction, the spelling the field carried before the rename
	// that older clients and the retry's own tests still send — and whichever
	// the body used is the key the rewrite removes.
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return body, false
	}

	rawSystem := fields["systemInstruction"]
	if snake, ok := fields["system_instruction"]; ok {
		rawSystem = snake
	}
	if !geminiSystemInstructionWorthMoving(rawSystem) {
		return body, false
	}
	var sys GeminiContent
	if err := json.Unmarshal(rawSystem, &sys); err != nil {
		return body, false
	}

	contents := decodeGeminiContents(fields["contents"])
	if encoded, err := json.Marshal(foldGeminiSystemInstruction(sys.Parts, contents), json.Deterministic(true)); err == nil {
		fields["contents"] = jsontext.Value(encoded)
	}
	delete(fields, "systemInstruction")
	delete(fields, "system_instruction")

	out, err := json.Marshal(fields, json.Deterministic(true))
	if err != nil {
		return body, false
	}
	return out, true
}

// geminiSystemInstructionWorthMoving reports whether a raw system-instruction
// member carries at least one part, so an absent, null or empty member leaves
// the request alone.
func geminiSystemInstructionWorthMoving(raw jsontext.Value) bool {
	if len(raw) == 0 || raw.Kind() == jsontext.KindNull {
		return false
	}
	var sys GeminiContent
	if err := json.Unmarshal(raw, &sys); err != nil {
		return false
	}
	return len(sys.Parts) > 0
}

// decodeGeminiContents decodes a raw contents member, treating an absent or
// non-array member as no contents.
func decodeGeminiContents(raw jsontext.Value) []GeminiContent {
	if len(raw) == 0 || raw.Kind() != '[' {
		return nil
	}
	var contents []GeminiContent
	if err := json.Unmarshal(raw, &contents); err != nil {
		return nil
	}
	return contents
}

// foldGeminiSystemInstruction prepends sysParts to contents[0] as a user-role
// turn, or creates that turn when contents is empty.
func foldGeminiSystemInstruction(sysParts []GeminiPart, contents []GeminiContent) []GeminiContent {
	if len(contents) == 0 {
		return []GeminiContent{{Role: "user", Parts: sysParts}}
	}
	contents[0] = GeminiContent{
		Role:  "user",
		Parts: append(append([]GeminiPart{}, sysParts...), contents[0].Parts...),
	}
	return contents
}

// IsAntigravityImageModel checks if the model name is an image generation model.
func IsAntigravityImageModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "image") || strings.Contains(m, "imagen")
}

// gcd computes greatest common divisor for resolution reduction.
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// ParseImageConfig extracts the base model name and aspect ratio from model suffixes.
func ParseImageConfig(model string) (cleanModel, aspectRatio string) {
	aspectRatio = "1:1"
	cleanModel = model

	// Look for suffix like -16x9, -4x3, -1x1, -1024x768
	if before, suffix, ok := strings.CutLast(model, "-"); ok && suffix != "" {
		if xIdx := strings.Index(suffix, "x"); xIdx != -1 {
			var w, h int
			if _, err := fmt.Sscanf(suffix, "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
				cleanModel = before
				if w <= 16 && h <= 16 {
					aspectRatio = fmt.Sprintf("%d:%d", w, h)
				} else {
					d := gcd(w, h)
					aspectRatio = fmt.Sprintf("%d:%d", w/d, h/d)
				}
			}
		}
	}
	return cleanModel, aspectRatio
}

// WrapAntigravityImageRequest builds an Antigravity request envelope for image generation.
// sessionID scopes conversation and trajectory UUIDs per connection or client session.
func WrapAntigravityImageRequest(prompt, base64Input, projectID, cleanModel, aspectRatio string, sessionID ...string) ([]byte, error) {
	parts := []GeminiPart{}
	if base64Input != "" {
		parts = append(parts, GeminiPart{
			InlineData: &GeminiInlineData{
				MimeType: "image/png",
				Data:     base64Input,
			},
		})
	}
	if prompt != "" {
		parts = append(parts, GeminiPart{
			Text: prompt,
		})
	}

	contents := []GeminiContent{
		{
			Role:  "user",
			Parts: parts,
		},
	}

	sess := projectID
	if len(sessionID) > 0 && sessionID[0] != "" {
		sess = sessionID[0]
	}

	genConfig := map[string]any{
		"temperature":     1.0,
		"topP":            0.95,
		"topK":            40,
		"maxOutputTokens": 8192,
		"imageConfig": map[string]string{
			"aspectRatio": aspectRatio,
		},
	}

	reqPayload := map[string]any{
		"contents":         contents,
		"generationConfig": genConfig,
		"sessionId":        sess,
	}

	reqJSON, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal image request: %w", err)
	}

	wrapper := AntigravityRequest{
		Project:     projectID,
		Model:       cleanModel,
		UserAgent:   "antigravity",
		RequestType: "image_gen",
		RequestID:   antigravityBuildRequestID(sess, cleanModel, "image_gen", 1),
		Request:     reqJSON,
	}

	return json.Marshal(wrapper)
}

// FormatAntigravityImageResponse converts a Gemini response containing inlineData to OpenAI images response format.
func FormatAntigravityImageResponse(rawGeminiResp []byte, prompt string) ([]byte, error) {
	unwrapped := UnwrapAntigravityResponse(rawGeminiResp)
	var resp GeminiResponse
	if err := json.Unmarshal(unwrapped, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal gemini image response: %w", err)
	}

	var images []map[string]string
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.InlineData != nil && part.InlineData.Data != "" {
				images = append(images, map[string]string{
					"b64_json": part.InlineData.Data,
				})
			}
		}
	}

	if len(images) == 0 {
		images = append(images, map[string]string{
			"b64_json":       "",
			"revised_prompt": prompt,
		})
	}

	result := map[string]any{
		"created": time.Now().Unix(),
		"data":    images,
	}

	return json.Marshal(result)
}

// decloakContentBlockStart is the shared implementation that restores the
// original tool name on a parsed Claude content_block_start event with
// content_block.type == "tool_use". It never mutates the input map. The
// boolean reports whether a tool name was actually rewritten.
func decloakContentBlockStart(event map[string]any, toolNameMap map[string]string) (map[string]any, bool) {
	if len(toolNameMap) == 0 || event == nil {
		return event, false
	}
	if event["type"] != "content_block_start" {
		return event, false
	}
	block, ok := event["content_block"].(map[string]any)
	if !ok || block == nil || block["type"] != "tool_use" {
		return event, false
	}
	name, ok := block["name"].(string)
	if !ok || name == "" {
		return event, false
	}
	original, found := toolNameMap[name]
	if !found {
		return event, false
	}
	blockCopy := make(map[string]any, len(block))
	for k, v := range block {
		blockCopy[k] = v
	}
	blockCopy["name"] = original
	eventCopy := make(map[string]any, len(event))
	for k, v := range event {
		eventCopy[k] = v
	}
	eventCopy["content_block"] = blockCopy
	return eventCopy, true
}

// DecloakStreamChunk restores original tool names in streamed Claude SSE event chunks.
// Specifically handles "content_block_start" events with content_block.type == "tool_use".
func DecloakStreamChunk(chunkBytes []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunkBytes) == 0 {
		return chunkBytes
	}

	var raw map[string]any
	if err := json.Unmarshal(chunkBytes, &raw); err != nil {
		return chunkBytes
	}

	out, changed := decloakContentBlockStart(raw, toolNameMap)
	if !changed {
		return chunkBytes
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return chunkBytes
	}
	return encoded
}

// DecloakClaudeStreamEvent restores the original tool name on parsed Claude content_block_start event.
func DecloakClaudeStreamEvent(event map[string]any, toolNameMap map[string]string) map[string]any {
	out, _ := decloakContentBlockStart(event, toolNameMap)
	return out
}
