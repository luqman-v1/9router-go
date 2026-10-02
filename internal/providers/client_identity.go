package providers

// Upstream ships one version constant per client family and derives every
// identity header from it (open-sse/providers/registry/codex.js CODEX_CLI_VERSION,
// open-sse/config/grokCli.js GROK_CLI_VERSION). These are the same two values,
// so the Go port has exactly one literal per family instead of one per call
// site — the previous layout hardcoded "0.2.99" and "0.2.93" in six files,
// which is how the grok-cli proxy ended up rejecting every request with
// HTTP 426 once cli-chat-proxy required 1.0.13.
const (
	// CodexCLIVersion is the codex CLI identity OpenAI's backend sees.
	CodexCLIVersion = "0.159.0"
	// CodexCLIUserAgent is the User-Agent header the codex backend expects.
	CodexCLIUserAgent = "codex_cli_rs/" + CodexCLIVersion
	// CodexCLIVersionHeader carries the same value in the `version` header,
	// which upstream sends alongside the User-Agent. Omitting it is what
	// leaves newer Codex models gated (upstream decolua/9router ca6e8407).
	CodexCLIVersionHeader = CodexCLIVersion

	// GrokCLIVersion is the @xai-official/grok identity cli-chat-proxy.grok.com
	// validates; anything below GrokCLIMinimumVersion is refused with HTTP 426.
	GrokCLIVersion = "1.0.44"
	// GrokCLIMinimumVersion is that floor, kept beside the shipped version so a
	// bump that reintroduces an older identity fails loudly instead of at
	// runtime against the proxy.
	GrokCLIMinimumVersion = "1.0.13"
	// GrokCLIUserAgent is the inference-path identity (grok-shell).
	GrokCLIUserAgent = "grok-shell/" + GrokCLIVersion + " (linux; x86_64)"
	// GrokCLIPagerUserAgent is the billing/probe identity. Upstream keeps a
	// separate pager UA because those calls go through the pager client, not
	// the inference one (open-sse/config/grokCli.js GROK_CLI_PAGER_USER_AGENT).
	GrokCLIPagerUserAgent = "grok-pager/" + GrokCLIVersion + " grok-shell/" + GrokCLIVersion + " (linux; x86_64)"

	// GrokCLIClientIdentifier is the x-grok-client-identifier sent by the
	// inference path; the pager paths below use GrokCLIPagerIdentifier.
	GrokCLIClientIdentifier = "grok-shell"
	// GrokCLIPagerIdentifier is the identifier the probe and OAuth paths send.
	GrokCLIPagerIdentifier = "grok-pager"
)