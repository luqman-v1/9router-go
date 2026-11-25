package translator

import (
	"strings"
	"testing"
)

// TestRewriteAntigravityFingerprints pins every row ported from ag-shield
// commit 0ffb051. Each row exists because Antigravity answers the fingerprint
// with a false 429 RESOURCE_EXHAUSTED; the neutral phrasing must keep the
// instruction body intact.
func TestRewriteAntigravityFingerprints(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "hermes pair-programming sentence",
			input: "You are pair programming with a user to solve coding tasks.",
			want:  "You pair program with the user on coding tasks.",
		},
		{
			name:  "coding task tail",
			input: "solve coding tasks. Be concise, practical, and tool-aware.",
			want:  "work on their coding tasks. Keep responses concise and practical, and use the available tools when they help.",
		},
		{
			name:  "working artifact sentence",
			input: "When the user asks you to build, run, or verify something, the deliverable is a working artifact backed by real tool output",
			want:  "",
		},
		{
			name:  "blocker sentence",
			input: "Reporting a blocker honestly is always better than inventing a result.",
			want:  "It is always better to report a blocker honestly than to make up a result.",
		},
		{
			name:  "be direct",
			input: "Be direct. Answer now.",
			want:  "Get straight to the point. Answer now.",
		},
		{
			name:  "authoritative reference",
			input: "DOCS.md is your authoritative reference.",
			want:  "DOCS.md is your reference guide.",
		},
		{
			name:  "google operational header",
			input: "# Google model operational directives\nBody",
			want:  "Body",
		},
		{
			name:  "docs url",
			input: "See https://hermes-agent.nousresearch.com/docs for help.",
			want:  "See the official documentation for help.",
		},
		{
			name:  "nous domain",
			input: "Visit nousresearch.com now.",
			want:  "Visit the documentation site now.",
		},
		{
			name:  "help sentence",
			input: "For help with Hermes Agent itself, run x.",
			want:  "",
		},
		{
			name:  "delivers sentence",
			input: "Hermes delivers their message.",
			want:  "the runtime delivers their message.",
		},
		{
			name:  "desktop app sentence",
			input: "You are chatting inside the Hermes desktop app, a graphical chat surface.",
			want:  "You are chatting inside a graphical desktop chat surface.",
		},
		{
			name:  "finishing header",
			input: "# Finishing the job",
			want:  "# Task completion",
		},
		{
			name:  "parallel header",
			input: "# Parallel tool calls",
			want:  "# Concurrent tool usage",
		},
		{
			name:  "steering header",
			input: "## Mid-turn user steering",
			want:  "## Mid-turn user guidance",
		},
		{
			name:  "out-of-band marker",
			input: "[OUT-OF-BAND USER MESSAGE from bob] hi",
			want:  "[USER UPDATE] hi",
		},
		{
			name:  "out-of-band end marker",
			input: "[/OUT-OF-BAND USER MESSAGE]",
			want:  "[/USER UPDATE]",
		},
		{
			name:  "available skills tags",
			input: "<available_skills>guide</available_skills>",
			want:  "<available_guides>guide</available_guides>",
		},
		{
			name:  "scan skills sentence",
			input: "Before replying, scan the skills below.",
			want:  "Before replying, review the available guides below.",
		},
		{
			name:  "skills come first",
			input: "Skills come first:",
			want:  "Guides come first:",
		},
		{
			name:  "skills header",
			input: "## Skills",
			want:  "## Guides",
		},
		{
			name:  "memory box",
			input: "══════════ MEMORY (your personal notes) abc ══════════",
			want:  "--- CONTEXT NOTES ---",
		},
		{
			name:  "user profile box",
			input: "══════════ USER PROFILE (who the user is) abc ══════════",
			want:  "--- USER CONTEXT ---",
		},
		{
			name:  "runtime header",
			input: "# Hermes runtime environment",
			want:  "# Runtime environment",
		},
		{
			name:  "runtime end marker",
			input: "<!-- End Hermes runtime environment -->",
			want:  "",
		},
		{
			name:  "kanban env var",
			input: "HERMES_KANBAN_STATE=1",
			want:  "AGENT_KANBAN_STATE=1",
		},
		{
			name:  "tenant env var",
			input: "HERMES_TENANT=x",
			want:  "AGENT_TENANT=x",
		},
		{
			name:  "agent path",
			input: "hermes-agent/config",
			want:  "agent-guide/config",
		},
		{
			name:  "tools directory",
			input: "hermes_tools/x",
			want:  "agent_tools/x",
		},
		{
			name:  "data attribute",
			input: "data-hermes-send",
			want:  "data-agent-send",
		},
		{
			name:  "window send call",
			input: "window.hermes.send('x')",
			want:  "window.agent.send('x')",
		},
		{
			name:  "bot phrase",
			input: "never via Hermes bot",
			want:  "never via the assistant bot",
		},
		{
			name:  "hermes agent token",
			input: "Hermes Agent is here.",
			want:  "the agent is here.",
		},
		{
			name:  "bare hermes",
			input: "Hermes is here.",
			want:  "agent is here.",
		},
		{
			name:  "lowercase hermes",
			input: "hermes is here.",
			want:  "agent is here.",
		},
		{
			name:  "openclaw identity",
			input: "You are a personal assistant running inside OpenClaw.",
			want:  "You are a personal assistant.",
		},
		{
			name:  "benign sentence untouched",
			input: "You are a helpful assistant.",
			want:  "You are a helpful assistant.",
		},
		{
			name:  "gateway brand preserved",
			input: "You are running antigravity through 9router.",
			want:  "You are running antigravity through 9router.",
		},
		{
			name:  "nous research without hermes untouched",
			input: "Created by Nous Research.",
			want:  "Created by Nous Research.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rewriteAntigravityFingerprints(tt.input); got != tt.want {
				t.Errorf("rewriteAntigravityFingerprints() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteAntigravityFingerprints_IsIdempotent(t *testing.T) {
	input := "You are pair programming with a user to solve coding tasks. Be direct. # Hermes runtime environment Hermes Agent"
	once := rewriteAntigravityFingerprints(input)
	twice := rewriteAntigravityFingerprints(once)
	if once != twice {
		t.Errorf("rewrite is not idempotent:\n once = %q\ntwice = %q", once, twice)
	}
}

// TestNormalizeHarnessMarkers_OMP covers the OMP half of the same filter. The
// tag rename alone was not enough: can1357/oh-my-pi#11699 reports the RFC 2119
// wording inside <system-conventions> as the fingerprint pair, so the whole
// block collapses to canonical neutral text.
func TestNormalizeHarnessMarkers_OMP(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "conventions block with current wording",
			input: `<system-conventions>
RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.
</system-conventions>`,
			want: antigravityCanonicalConventions,
		},
		{
			name:  "underscore conventions block with future wording",
			input: `<system_conventions data-version="2">Use MUST for mandatory steps and MAY for optional ones.</system_conventions>`,
			want:  antigravityCanonicalConventions,
		},
		{
			name:  "standalone RFC 2119 clause",
			input: "RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.",
			want:  antigravityRFCClauseReplacement,
		},
		{
			name:  "standalone RFC 8174 clause",
			input: "Per RFC 8174, the key words MUST and SHOULD carry their usual meaning.",
			want:  "Per Requirement terminology: MUST means mandatory, SHOULD means recommended, and MAY means optional.",
		},
		{
			name:  "underscore directive and critical tags",
			input: "<system_directive>dir stays</system_directive> <critical>crit stays</critical>",
			want:  "<instructions>dir stays</instructions> <important>crit stays</important>",
		},
		{
			name:  "hyphenated brand",
			input: "oh-my-pi coding harness oh-my-pi",
			want:  "AI coding assistant coding assistant",
		},
		{
			name:  "underscore brand",
			input: "oh_my_pi coding harness oh_my_pi",
			want:  "AI coding assistant coding assistant",
		},
		{
			name:  "mixed case brand",
			input: "OH MY PI coding harness OH MY PI",
			want:  "AI coding assistant coding assistant",
		},
		{
			name:  "omp live",
			input: "omp Live",
			want:  "coding assistant live",
		},
		{
			// A live Antigravity request whose remaining OMP markers were this
			// exact sentence and xd:// still answered 429; replacing both in
			// the replayed prompt returned 200.
			name:  "omp 18 identity sentence",
			input: "You are omp's trusted coding assistant.\n\n# Engineering",
			want:  "You are a trusted coding assistant.\n\n# Engineering",
		},
		{
			name:  "omp identity with typographic apostrophe",
			input: "You are omp’s trusted coding assistant.",
			want:  "You are a trusted coding assistant.",
		},
		{
			name:  "omp internal url scheme",
			input: "- omp://: harness docs, AVOID unless asked.",
			want:  "- docs://: harness docs, AVOID unless asked.",
		},
		{
			name:  "omp home path",
			input: "Managed skills: isolated `~/.omp/agent/managed-skills`.",
			want:  "Managed skills: isolated `.agent/agent/managed-skills`.",
		},
		{
			name:  "xd internal url scheme",
			input: "classification, bulk judgment → xd://eval/judge",
			want:  "classification, bulk judgment → internal://eval/judge",
		},
		{
			name:  "standalone omp token",
			input: "ask the omp harness for docs",
			want:  "ask the agent harness for docs",
		},
		{
			name:  "openmp is not omp branding",
			input: "OpenMP directives and omp_get_thread_num()",
			want:  "OpenMP directives and omp_get_thread_num()",
		},
		{
			name:  "omp substrings are not omp branding",
			input: "compiled code is complete and noncomparable",
			want:  "compiled code is complete and noncomparable",
		},
		{
			name:  "benign instructions untouched",
			input: "Follow the project style guide.",
			want:  "Follow the project style guide.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeHarnessMarkers(tt.input); got != tt.want {
				t.Errorf("normalizeHarnessMarkers() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeHarnessMarkers_OMPIsIdempotent(t *testing.T) {
	input := `<system-conventions>RFC 2119: MUST.</system-conventions> ` +
		`You are omp's trusted coding assistant. oh-my-pi coding harness oh-my-pi omp Live ` +
		`omp:// xd://eval ~/.omp/agent omp`
	once := normalizeHarnessMarkers(input)
	twice := normalizeHarnessMarkers(once)
	if once != twice {
		t.Errorf("normalizer is not idempotent:\n once = %q\ntwice = %q", once, twice)
	}
}

// TestStripCompetitivePrompts_FingerprintsAreSystemOnly pins the scope: the
// backend scans the system instruction for harness branding, while a caller's
// own conversation turns keep their vocabulary.
func TestStripCompetitivePrompts_FingerprintsAreSystemOnly(t *testing.T) {
	req := &GeminiRequest{
		SystemInstruction: &GeminiContent{
			Role:  "user",
			Parts: []GeminiPart{{Text: "Be direct. # Finishing the job"}},
		},
		Contents: []GeminiContent{
			{
				Role:  "user",
				Parts: []GeminiPart{{Text: "Be direct. # Finishing the job"}},
			},
		},
	}

	stripped := StripCompetitivePrompts(req)
	sys := stripped.SystemInstruction.Parts[0].Text
	if !strings.Contains(sys, "Get straight to the point.") || !strings.Contains(sys, "# Task completion") {
		t.Errorf("expected system fingerprints rewritten, got %q", sys)
	}
	user := stripped.Contents[0].Parts[0].Text
	if user != "Be direct. # Finishing the job" {
		t.Errorf("expected user turn untouched, got %q", user)
	}
}

func TestStripCompetitivePrompts_SanitizesToolDescriptions(t *testing.T) {
	const original = "Be direct. Hermes Agent runs this tool."
	req := &GeminiRequest{
		Tools: []GeminiTool{{
			FunctionDeclarations: []GeminiFunctionDecl{
				{Name: "read_file", Description: original},
				{Name: "run_command", Description: "Run a command."},
			},
		}},
	}

	stripped := StripCompetitivePrompts(req)
	got := stripped.Tools[0].FunctionDeclarations[0].Description
	if strings.Contains(got, "Be direct.") || strings.Contains(got, "Hermes") {
		t.Errorf("expected tool description fingerprints removed, got %q", got)
	}
	if want := "Get straight to the point. the agent runs this tool."; got != want {
		t.Errorf("tool description = %q, want %q", got, want)
	}
	if other := stripped.Tools[0].FunctionDeclarations[1].Description; other != "Run a command." {
		t.Errorf("benign tool description changed: %q", other)
	}
	if req.Tools[0].FunctionDeclarations[0].Description != original {
		t.Error("StripCompetitivePrompts mutated the caller's tool description")
	}
}

// TestStripCompetitivePrompts_SanitizesOMPToolDescriptions covers the OMP
// half of the tool-description filter. OMP documents its own schemes and home
// path in tool descriptions, and the backend scans those declarations the same
// way it scans the system instruction.
func TestStripCompetitivePrompts_SanitizesOMPToolDescriptions(t *testing.T) {
	const original = "Read docs from omp:// or xd://eval and ~/.omp/agent/managed-skills."
	req := &GeminiRequest{
		Tools: []GeminiTool{{
			FunctionDeclarations: []GeminiFunctionDecl{
				{Name: "read", Description: original},
			},
		}},
	}

	stripped := StripCompetitivePrompts(req)
	got := stripped.Tools[0].FunctionDeclarations[0].Description
	for _, marker := range []string{"omp://", "xd://", "~/.omp"} {
		if strings.Contains(got, marker) {
			t.Errorf("tool description still contains %q: %q", marker, got)
		}
	}
	if want := "Read docs from docs:// or internal://eval and .agent/agent/managed-skills."; got != want {
		t.Errorf("tool description = %q, want %q", got, want)
	}
	if req.Tools[0].FunctionDeclarations[0].Description != original {
		t.Error("StripCompetitivePrompts mutated the caller's tool description")
	}
}

// TestStripCompetitivePrompts_StripsHermesWholeLines verifies the seven
// Hermes whole-line markers added in upstream decolua/9router@bb399170a are
// excised completely, leaving surrounding instructions intact.
func TestStripCompetitivePrompts_StripsHermesWholeLines(t *testing.T) {
	triggers := []string{
		"<!-- End Hermes runtime environment -->",
		"# Google model operational directives",
		"If a tool, install, or network call fails and blocks the real path, say so directly and try an alternative (different package manager, different approach, ask the user). NEVER substitute plausible-looking fabricated output (made-up data, invented file contents, synthesised API responses) for results you couldn't actually produce. Reporting a blocker honestly is always better than inventing a result.",
		"When the user asks you to build, run, or verify something, the deliverable is a working artifact backed by real tool output — not a description of one. Do not stop after writing a stub, a plan, or a single command. Keep working until you have actually exercised the code or produced the requested result, then report what real execution returned.",
		"For help with Hermes Agent itself — configuring, setting up, using, extending, or troubleshooting it — or to understand your own features, tools, or capabilities, the documentation at https://hermes-agent.nousresearch.com/docs is your authoritative reference and always holds the latest, most up-to-date information.",
		"Mid-turn, the user can steer you: Hermes delivers their message as a standalone user message right after the latest tool results, wrapped exactly as:",
	}
	soul := "You are Hermes Agent, built by Nous Research. Be direct: match the length of your reply to the weight of the ask — a one-line question gets a one-line answer."

	for _, eol := range []string{"\n", "\r\n"} {
		t.Run(eol, func(t *testing.T) {
			lines := []string{soul, "Keep this line."}
			lines = append(lines, triggers...)
			lines = append(lines, "And this one.")
			text := strings.Join(lines, eol)

			req := &GeminiRequest{
				SystemInstruction: &GeminiContent{
					Role:  "user",
					Parts: []GeminiPart{{Text: text}},
				},
			}
			stripped := StripCompetitivePrompts(req)
			if stripped == nil || stripped.SystemInstruction == nil || len(stripped.SystemInstruction.Parts) == 0 {
				t.Fatal("system instruction lost")
			}
			got := stripped.SystemInstruction.Parts[0].Text
			for _, tr := range triggers {
				if strings.Contains(got, tr) {
					t.Errorf("trigger line still present: %q", tr)
				}
			}
			if strings.Contains(got, "Be direct:") {
				t.Errorf("SOUL identity not rewritten: %q", got)
			}
			if strings.Contains(got, "Nous Research") {
				t.Errorf("Nous Research not stripped: %q", got)
			}
			if !strings.Contains(got, "You are an AI assistant.") {
				t.Errorf("expected neutral identity, got %q", got)
			}
			if !strings.Contains(got, "Keep this line.") {
				t.Errorf("kept line lost: %q", got)
			}
			if !strings.Contains(got, "And this one.") {
				t.Errorf("kept line lost: %q", got)
			}

			// Begin marker (non-trigger) survives.
			beginReq := &GeminiRequest{
				SystemInstruction: &GeminiContent{
					Role:  "user",
					Parts: []GeminiPart{{Text: "<!-- Begin Hermes runtime environment -->" + eol + "CWD: /home"}},
				},
			}
			beginStripped := StripCompetitivePrompts(beginReq)
			beginText := beginStripped.SystemInstruction.Parts[0].Text
			if !strings.Contains(beginText, "Begin") || !strings.Contains(beginText, "runtime environment") {
				t.Errorf("begin marker wrongly stripped: %q", beginText)
			}
		})
	}
}
