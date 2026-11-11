package providers

import "strings"

// Command Code's documented Provider API (commandcode.ai/docs/provider) is a
// different product surface from the `commandcode` entry beside it. That entry
// speaks the CLI's internal /alpha/generate envelope with spoofed CLI identity
// headers, because it predates the public API and its key is a CLI key. This
// provider is the documented one: standard OpenAPI/Anthropic schemas, three
// lanes, no identity spoofing, and the same key.
//
// The lane a model travels on is not a guess — the live catalogue publishes it
// per model in `supported_endpoints`, and commandcode_goat_catalog.go is that
// declaration. Sending a Claude id to /chat/completions answers 400
// unsupported_model, and the reverse is refused the same way, so routing has to
// follow the feed rather than the provider.

// isCommandCodeGoatProvider reports whether provider is this provider under
// either its id or its uiAlias.
func isCommandCodeGoatProvider(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	return p == "commandcode-goat" || p == "cmc-goat"
}

// CommandcodeGoatTarget returns the lane a model is declared on, defaulting to
// the chat lane for an id the feed does not publish (a model released after
// this snapshot). The default matches what the endpoint's own error suggests:
// only Claude ids are Messages-exclusive.
func CommandcodeGoatTarget(model string) string {
	if mf, ok := commandcodeGoatModelFormats[model]; ok && mf.TargetFormat != "" {
		return mf.TargetFormat
	}
	return FormatOpenAI
}

// CommandcodeGoatModels returns the catalogue the feed published, in feed order.
func CommandcodeGoatModels() []string { return commandcodeGoatModels }

// commandCodeGoatCapabilities resolves the capability block for one model. The
// window is the feed's own context_length — the catalogue spans 200k to 1.05M,
// so a single hardcoded figure would either over-advertise a small model or
// needlessly compact a large one.
//
// MaxOutput stays zero: the feed publishes no output cap, so the models.dev
// catalogue and the pattern table supply it instead of this file guessing.
func commandCodeGoatCapabilities(model string) Capabilities {
	res := DefaultCapabilities
	res.Tools = true
	// The docs' answer on modalities: "Text and images. Audio, file, and
	// document parts are rejected by the schema. Pick a model without vision
	// and the upstream provider returns its own error, we don't pre-gate per
	// model." So vision is declared for the lane and the upstream decides.
	res.Vision = true
	res.Reasoning = true
	if CommandcodeGoatTarget(model) == FormatClaude {
		// The Messages lane is the Anthropic wire, so the thinking control has
		// to be the Anthropic one rather than an OpenAI reasoning_effort the
		// lane's schema does not carry.
		res.ThinkingFormat = "claude-adaptive"
	} else {
		res.ThinkingFormat = "openai"
	}
	res.ContextWindow = commandcodeGoatContextWindows[model]
	return res
}
