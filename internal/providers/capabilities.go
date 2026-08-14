package providers

import (
	"path"
	"strings"
)

// Capabilities represents what a model can do beyond plain text.
type Capabilities struct {
	Vision      bool
	PDF         bool
	AudioInput  bool
	VideoInput  bool
	ImageOutput bool
	AudioOutput bool
	Search      bool
	Tools       bool
	Reasoning   bool
}

var DefaultCapabilities = Capabilities{
	Vision:      false,
	PDF:         false,
	AudioInput:  false,
	VideoInput:  false,
	ImageOutput: false,
	AudioOutput: false,
	Search:      false,
	Tools:       true,
	Reasoning:   false,
}

var modelCapabilities = map[string]Capabilities{
	"claude-opus-5":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-5-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-5-agentic":          {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-5-thinking-agentic": {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.6":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.7":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-7":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.8":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-6":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-8":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.8-thinking":       {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-8-thinking":       {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-4.6":              {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-4-6":              {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5-thinking":       {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5-agentic":        {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5-thinking-agentic": {Vision: true, Reasoning: true, Search: true, Tools: true},
	"gpt-image-1":                    {ImageOutput: true},
	"glm-4.6v":                       {Vision: true, Reasoning: true, Tools: true},
	"vision-model":                   {Vision: true, Reasoning: true, Tools: true},
	"coder-model":                    {Reasoning: true, Tools: true},
	"kimi-k3":                        {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"k3":                             {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-for-coding":                {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-for-coding-highspeed":      {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-k2.7-code":                 {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-k2.7-code-highspeed":       {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
}

var providerCapabilities = map[string]map[string]Capabilities{
	"nvidia": {
		"minimaxai/minimax-m2.7":        {Reasoning: true, Tools: true},
		"minimaxai/minimax-m3":          {Vision: true, Reasoning: true, Tools: true},
		"z-ai/glm-5.2":                  {Reasoning: true, Tools: true},
		"deepseek-ai/deepseek-v4-pro":   {Reasoning: true, Tools: true},
		"deepseek-ai/deepseek-v4-flash": {Reasoning: true, Tools: true},
	},
	"codex": {
		"gpt-5.6-sol":          {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-sol-review":   {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-terra":        {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-terra-review": {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-luna":         {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-luna-review":  {Vision: true, Reasoning: true, Search: true, Tools: true},
	},
	"codebuddy-cn": {
		"glm-5.2":            {Reasoning: true, Tools: true},
		"glm-5.1":            {Reasoning: true, Tools: true},
		"glm-5.0":            {Reasoning: true, Tools: true},
		"glm-5.0-turbo":      {Reasoning: true, Tools: true},
		"glm-5v-turbo":       {Vision: true, Reasoning: true, Tools: true},
		"glm-4.7":            {Reasoning: true, Tools: true},
		"minimax-m3":         {Vision: true, Reasoning: true, Tools: true},
		"minimax-m2.7":       {Vision: true, Reasoning: true, Tools: true},
		"kimi-k2.7":          {Vision: true, Reasoning: true, Tools: true},
		"kimi-k2.6":          {Vision: true, Reasoning: true, Tools: true},
		"kimi-k2.5":          {Vision: true, Reasoning: true, Tools: true},
		"hy3-preview":        {Vision: true, Reasoning: true, Tools: true},
		"deepseek-v4-pro":    {Vision: true, Reasoning: true, Tools: true},
		"deepseek-v4-flash":  {Vision: true, Reasoning: true, Tools: true},
		"deepseek-v3-2-volc": {Reasoning: true, Tools: true},
	},
	"poolside": {
		"laguna-s-2.1":  {Reasoning: true, Tools: true},
		"laguna-xs-2.1": {Reasoning: true, Tools: true},
	},
}

func init() {
	kiroGpt56 := Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}
	providerCapabilities["kiro"] = map[string]Capabilities{
		"gpt-5.6-sol":                  kiroGpt56,
		"gpt-5.6-terra":                kiroGpt56,
		"gpt-5.6-luna":                 kiroGpt56,
		"gpt-5.6-sol-thinking":         kiroGpt56,
		"gpt-5.6-terra-thinking":       kiroGpt56,
		"gpt-5.6-luna-thinking":        kiroGpt56,
		"gpt-5.6-sol-agentic":          kiroGpt56,
		"gpt-5.6-terra-agentic":        kiroGpt56,
		"gpt-5.6-luna-agentic":         kiroGpt56,
		"gpt-5.6-sol-thinking-agentic": kiroGpt56,
		"gpt-5.6-terra-thinking-agentic": kiroGpt56,
		"gpt-5.6-luna-thinking-agentic": kiroGpt56,
	}
}

type patternCapability struct {
	pattern string
	caps    Capabilities
}

var patternCapabilities = []patternCapability{
	{"*claude*opus-5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus-4.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus-4.7*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus-4.8*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*sonnet-4.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*sonnet-4.7*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*haiku*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*sonnet*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*fable*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*mythos*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude-3*", Capabilities{Vision: true, Tools: true}},
	{"*claude*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},

	{"*gemini*image*", Capabilities{Vision: true, ImageOutput: true, Tools: true}},
	{"*gemini-3*pro*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true}},
	{"*gemini-3*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true}},
	{"*gemini-2.5*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true}},
	{"*gemini-2*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Search: true, Tools: true}},
	{"*gemini*", Capabilities{Vision: true, Search: true, Tools: true}},
	{"*gemma*", Capabilities{Vision: true, Tools: true}},
	{"*nanobanana*", Capabilities{Vision: true, ImageOutput: true, Tools: true}},

	{"*gpt-5*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*gpt-5*codex*", Capabilities{Reasoning: true, Search: true, Tools: true}},
	{"*gpt-5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*gpt-4o*", Capabilities{Vision: true, Search: true, Tools: true}},
	{"*gpt-4.1*", Capabilities{Vision: true, Tools: true}},
	{"*gpt-4-turbo*", Capabilities{Vision: true, Tools: true}},
	{"*gpt-4*", Capabilities{Tools: true}},
	{"*gpt-3.5*", Capabilities{Tools: true}},
	{"*gpt-oss*", Capabilities{Reasoning: true, Tools: true}},

	{"*o1-mini*", Capabilities{Reasoning: true, Tools: true}},
	{"*o1*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*o3*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*o4*", Capabilities{Vision: true, Reasoning: true, Tools: true}},

	{"*grok*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*grok-code*", Capabilities{Reasoning: true, Tools: true}},
	{"*grok-4.5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*grok-4*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*grok-3*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*grok*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},

	{"*qwen*vl*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*qwen*omni*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen*coder*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwen*max*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwen3.5*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen3.6*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen3.7*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen*plus*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*qwen*235b*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwq*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwen*", Capabilities{Reasoning: true, Tools: true}},

	{"*kimi*k3*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*kimi*for-coding*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*kimi*k2.7*code*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*kimi*k2*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*kimi*", Capabilities{Reasoning: true, Tools: true}},

	{"*glm-5*", Capabilities{Reasoning: true, Tools: true}},
	{"*glm-4.7*", Capabilities{Reasoning: true, Tools: true}},
	{"*glm-4*", Capabilities{Reasoning: true, Tools: true}},
	{"*glm*", Capabilities{Reasoning: true, Tools: true}},

	{"*deepseek-v4*", Capabilities{Reasoning: true, Tools: true}},
	{"*reasoner*", Capabilities{Reasoning: true, Tools: true}},
	{"*deepseek-r*", Capabilities{Reasoning: true, Tools: true}},
	{"*deepseek-chat*", Capabilities{Tools: true}},
	{"*deepseek*", Capabilities{Reasoning: true, Tools: true}},

	{"*minimax*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*minimax-m3*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*minimax-m2.7*", Capabilities{Reasoning: true, Tools: true}},
	{"*minimax*", Capabilities{Reasoning: true, Tools: true}},

	{"*mimo*v2.5*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Tools: true}},
	{"*mimo*omni*", Capabilities{Vision: true, AudioInput: true, Tools: true}},
	{"*mimo*", Capabilities{Vision: true, Tools: true}},

	{"*llama-4*", Capabilities{Vision: true, Tools: true}},
	{"*llama*", Capabilities{Tools: true}},

	{"*codestral*", Capabilities{Tools: true}},
	{"*mistral-large*", Capabilities{Vision: true, Tools: true}},
	{"*mistral*", Capabilities{Tools: true}},

	{"*command-a-vision*", Capabilities{Vision: true, Tools: true}},
	{"*command*", Capabilities{Tools: true}},

	{"*sonar*", Capabilities{Search: true, Tools: true}},
	{"*pplx*", Capabilities{Search: true, Tools: true}},
	{"*perplexity*", Capabilities{Search: true, Tools: true}},

	{"*laguna-s-2.1*free*", Capabilities{Reasoning: true, Tools: true}},
	{"*laguna-s-2.1*", Capabilities{Reasoning: true, Tools: true}},
	{"*laguna*", Capabilities{Reasoning: true, Tools: true}},

	{"*hunyuan*", Capabilities{Reasoning: true, Tools: true}},
	{"hy3*", Capabilities{Reasoning: true, Tools: true}},
	{"*step-*", Capabilities{Reasoning: true, Tools: true}},
	{"*nemotron*", Capabilities{Reasoning: true, Tools: true}},
	{"*ling-*", Capabilities{Reasoning: true, Tools: true}},
}

// matchPattern checks if a string matches a glob pattern (only supports * as wildcard)
func matchPattern(pattern, s string) bool {
	matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(s))
	if err != nil {
		return false
	}
	return matched
}

// GetModelTokenLimits returns the context window and maximum output tokens for a model.
func GetModelTokenLimits(model string) (contextWindow int, maxOutput int) {
	m := strings.ToLower(model)

	switch {
	case strings.Contains(m, "gemini-1.5") || strings.Contains(m, "gemini-2.0") || strings.Contains(m, "gemini-2.5") || strings.Contains(m, "gemini-3"):
		return 1048576, 65536
	case strings.Contains(m, "claude-3") || strings.Contains(m, "claude-sonnet") || strings.Contains(m, "claude-opus") || strings.Contains(m, "claude-haiku"):
		return 200000, 8192
	case strings.Contains(m, "gpt-4o") || strings.Contains(m, "gpt-4-turbo") || strings.Contains(m, "gpt-4.1") || strings.Contains(m, "gpt-5"):
		return 128000, 16384
	case strings.Contains(m, "o1") || strings.Contains(m, "o3"):
		return 200000, 100000
	case strings.Contains(m, "deepseek") || strings.Contains(m, "qwen") || strings.Contains(m, "glm") || strings.Contains(m, "kimi"):
		return 131072, 8192
	default:
		return 128000, 4096
	}
}

// GetCapabilitiesForModel resolves capabilities using the fallback chain.
func GetCapabilitiesForModel(provider, model string) Capabilities {
	if model == "" {
		return DefaultCapabilities
	}

	baseModel := model
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		baseModel = model[idx+1:]
	}

	// 1. Provider-specific override
	if provider != "" {
		if pCaps, ok := providerCapabilities[provider]; ok {
			if caps, ok := pCaps[model]; ok {
				return mergeCapabilities(DefaultCapabilities, caps)
			}
			if caps, ok := pCaps[baseModel]; ok {
				return mergeCapabilities(DefaultCapabilities, caps)
			}
		}
	}

	// 2. Canonical exact
	if caps, ok := modelCapabilities[baseModel]; ok {
		return mergeCapabilities(DefaultCapabilities, caps)
	}
	if caps, ok := modelCapabilities[model]; ok {
		return mergeCapabilities(DefaultCapabilities, caps)
	}

	// 3. Pattern match
	for _, p := range patternCapabilities {
		if matchPattern(p.pattern, baseModel) || matchPattern(p.pattern, model) {
			return mergeCapabilities(DefaultCapabilities, p.caps)
		}
	}

	// 4. Default floor
	return DefaultCapabilities
}

func mergeCapabilities(base, overlay Capabilities) Capabilities {
	if overlay.Vision {
		base.Vision = true
	}
	if overlay.PDF {
		base.PDF = true
	}
	if overlay.AudioInput {
		base.AudioInput = true
	}
	if overlay.VideoInput {
		base.VideoInput = true
	}
	if overlay.ImageOutput {
		base.ImageOutput = true
	}
	if overlay.AudioOutput {
		base.AudioOutput = true
	}
	if overlay.Search {
		base.Search = true
	}
	if !overlay.Tools {
		// if specifically disabled (Tools is true by default usually, but we check if we need to turn it off)
		// Wait, the merge logic in JS is { ...DEFAULT, ...caps }.
		// So if overlay sets tools: false, it should be false.
		// In Go, bool zero value is false. So we can't tell if overlay didn't set it, or set it to false.
		// However, in our hardcoded maps above, I explicitly included Tools: true for all that have it,
		// and we can assume any overlay boolean that is `false` is meant to be false if it differs from default.
		// Actually, to make it simple, let's just use the overlay if it has truthy values, except Tools which we default to true.
		// Let's just do a naive merge.
	}
	// For Go, since we define complete Capabilities structs in the maps with Tools: true where needed:
	return Capabilities{
		Vision:      base.Vision || overlay.Vision,
		PDF:         base.PDF || overlay.PDF,
		AudioInput:  base.AudioInput || overlay.AudioInput,
		VideoInput:  base.VideoInput || overlay.VideoInput,
		ImageOutput: base.ImageOutput || overlay.ImageOutput,
		AudioOutput: base.AudioOutput || overlay.AudioOutput,
		Search:      base.Search || overlay.Search,
		Tools:       overlay.Tools, // We made sure to set Tools:true in all overlays where it applies. If it's omitted, it becomes false. Wait, DefaultCapabilities has Tools=true. Let's make sure our maps above have Tools:true for everything except gpt-image-1.
		Reasoning:   base.Reasoning || overlay.Reasoning,
	}
}
