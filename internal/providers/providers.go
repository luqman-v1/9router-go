package providers

import (
	"net/http"
	"os"
	"strings"
)

// ProviderConfig describes how to reach an upstream provider.
type ProviderConfig struct {
	BaseURL       string
	AuthHeader    string            // "Authorization" or "x-api-key"
	AuthScheme    string            // "bearer" or "raw"
	NoAuth        bool              // true = no API key required
	DefaultAPIKey string            // fallback API key when none provided
	StaticHeaders map[string]string // extra headers to set on every request
	Format        string            // "" (OpenAI standard), "gemini-native"
	ImageURL      string            // override /images/generations endpoint
	TTSURL        string            // override /audio/speech endpoint
	STTURL        string            // override /audio/transcriptions endpoint
	VideoURL      string            // override /videos/generations endpoint
	VoicesURL     string            // override /audio/voices listing endpoint
	SystemoneURL  string            // override /systemone endpoint (System One structured evaluation)
	FetchURL      string            // override /web/fetch endpoint (Jina, Firecrawl, etc.)
	FetchMethod   string            // HTTP method for fetch: GET or POST (default POST)
	UsageURL       string            // override /usage endpoint (quota tracker)
}

// IsGeminiNative returns true if provider uses Gemini-native format.
func (p *ProviderConfig) IsGeminiNative() bool { return p.Format == "gemini-native" }

// IsGeminiOpenAICompat returns true if provider is Gemini behind an
// OpenAI-compatible endpoint whose tool schema validation is as strict as the
// native one (e.g. the "gemini" provider at /v1beta/openai/chat/completions).
func (p *ProviderConfig) IsGeminiOpenAICompat() bool { return p.Format == "gemini-openai" }

// WithStaticHeader returns cfg with one static header set, copying the config
// and the header map so a shared catalog entry is never mutated. A header the
// config already carries is replaced in the copy.
func WithStaticHeader(cfg *ProviderConfig, name, value string) *ProviderConfig {
	if cfg == nil {
		return nil
	}
	cloned := *cfg
	cloned.StaticHeaders = make(map[string]string, len(cfg.StaticHeaders)+1)
	for k, v := range cfg.StaticHeaders {
		cloned.StaticHeaders[k] = v
	}
	cloned.StaticHeaders[name] = value
	return &cloned
}

// WithoutStaticHeader returns cfg with one static header removed, copying both
// the config and the map. It answers cfg unchanged when the header is absent,
// so the common path allocates nothing.
func WithoutStaticHeader(cfg *ProviderConfig, name string) *ProviderConfig {
	if cfg == nil {
		return nil
	}
	if _, present := cfg.StaticHeaders[name]; !present {
		return cfg
	}
	cloned := *cfg
	cloned.StaticHeaders = make(map[string]string, len(cfg.StaticHeaders)-1)
	for k, v := range cfg.StaticHeaders {
		if k != name {
			cloned.StaticHeaders[k] = v
		}
	}
	return &cloned
}

// modelsListURL is the OpenAI-compatible /v1/models endpoint of providers whose
// catalogue is fetched live for the dashboard's "Suggested free models" import.
// It is the same set upstream wires into PROVIDER_MODELS_CONFIG
// (src/app/api/providers/[id]/models/route.js, v0.5.91).
var modelsListURL = map[string]string{
	"tokenharbor": "https://tokenharbor.ai/v1/models",
	"dahl":        "https://inference.dahl.global/v1/models",
	"atria":       "https://api.atria-asi.ai/v1/models",
	"agnes":       "https://apihub.agnes-ai.com/v1/models",
	"bai":         "https://api.b.ai/v1/models",
	// Meta's Model API refuses /v1/models without the protocol version header,
	// so the live-catalog fetch carries it on top of the bearer token
	// (upstream PROVIDER_MODELS_CONFIG.muse).
	"muse": "https://api.meta.ai/v1/models",
}

// modelsListHeaders carries the extra headers one live-catalogue endpoint
// needs beyond the connection's bearer token. Upstream keys the same values
// off PROVIDER_MODELS_CONFIG per provider (src/app/api/providers/[id]/models/
// route.js), which sets headers per entry — the flat Go map only has room for
// the ones that actually differ.
var modelsListHeaders = map[string]map[string]string{
	"muse": {"x-api-version": "1.0.0", "Content-Type": "application/json"},
}

// ModelsListHeaders returns the extra headers the live catalogue endpoint of a
// provider requires, or nil when it needs none. The map is shared by every
// request, so a caller must not mutate it.
func ModelsListHeaders(provider string) map[string]string {
	return modelsListHeaders[strings.ToLower(provider)]
}

// ModelsListURL returns the live catalogue endpoint for a provider, or "" when
// the provider has none.
func ModelsListURL(provider string) string {
	return modelsListURL[strings.ToLower(provider)]
}

// AnthropicBetaRedactThinking asks Anthropic to return thinking blocks as a
// signature only. That is right for clients that never render thinking, but it
// blanks the very summaries a client requested with
// `thinking.display: "summarized"`, so it is dropped per request when the body
// asks for them. Upstream: ANTHROPIC_BETA_REDACT_THINKING in
// open-sse/providers/shared.js.
const AnthropicBetaRedactThinking = "redact-thinking-2026-02-12"

// WithoutBetaFlag returns a copy of headers with one Anthropic-Beta flag
// removed. The registry's header map is shared by every request, so a
// request-scoped edit has to copy rather than mutate.
func WithoutBetaFlag(headers map[string]string, flag string) map[string]string {
	current, ok := headers["Anthropic-Beta"]
	if !ok {
		return headers
	}
	kept := make([]string, 0, 8)
	for _, existing := range strings.Split(current, ",") {
		if trimmed := strings.TrimSpace(existing); trimmed != "" && trimmed != flag {
			kept = append(kept, trimmed)
		}
	}
	res := make(map[string]string, len(headers))
	for k, v := range headers {
		res[k] = v
	}
	res["Anthropic-Beta"] = strings.Join(kept, ",")
	return res
}

// MergeAnthropicBeta unions any number of comma-separated beta flag lists into
// one de-duplicated header value, keeping the order they were seen in. The
// caller's own flags are merged in rather than dropped: a client asking for a
// beta the gateway does not list would otherwise be refused without ever being
// told why. Port of mergeAnthropicBeta in open-sse/providers/shared.js.
func MergeAnthropicBeta(values ...string) string {
	seen := make(map[string]bool, 8)
	merged := make([]string, 0, 8)
	for _, value := range values {
		for _, flag := range strings.Split(value, ",") {
			flag = strings.TrimSpace(flag)
			if flag == "" || seen[flag] {
				continue
			}
			seen[flag] = true
			merged = append(merged, flag)
		}
	}
	return strings.Join(merged, ",")
}

// WithHeader returns a copy of headers with one entry set. The registry's
// header map is shared by every request, so a request-scoped change has to
// copy rather than mutate — the same reason WithoutBetaFlag copies.
func WithHeader(headers map[string]string, key, value string) map[string]string {
	res := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		res[k] = v
	}
	res[key] = value
	return res
}

// MergeHeaderOverrides returns a copy of headers with the operator's
// per-provider overrides applied on top. It is the Go shape of upstream's
// `Object.assign(headers, providerOverrides.headers)`
// (decolua/9router v0.5.95, open-sse/executors/base.js:132) — an override
// wins over what the registry set, which is the whole point: the operator is
// correcting a header the gateway sends, not adding a second opinion.
//
// The registry's header map is shared by every request, so this copies rather
// than mutating, for the same reason WithHeader copies.
func MergeHeaderOverrides(headers, overrides map[string]string) map[string]string {
	if len(overrides) == 0 {
		return headers
	}
	res := make(map[string]string, len(headers)+len(overrides))
	for k, v := range headers {
		res[k] = v
	}
	for k, v := range overrides {
		res[k] = v
	}
	return res
}

// KnownProviders maps provider IDs to their upstream configuration.
var KnownProviders = map[string]ProviderConfig{
	"openai": {
		BaseURL:    "https://api.openai.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"agnes": {
		BaseURL:    "https://apihub.agnes-ai.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	// Muse — Meta's Model API. Dual auth upstream (Muse Code subscription
	// device-code key and a dev.meta.ai pay-as-you-go key); both ride the same
	// bearer transport, and only an account-issued (OAuth) key needs the
	// protocol version header. Every Muse Spark model declares
	// targetFormat "openai-responses" (see museModelFormats), so /v1/responses
	// is the transport the executor dials for this provider.
	"muse": {
		BaseURL:    "https://api.meta.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		Format:     "openai-responses",
		StaticHeaders: map[string]string{
			"x-api-version": "1.0.0",
		},
	},
	// v1m System One — a calibrated decision engine, not a chat model. Its
	// two registry models carry kind "systemone", so the only endpoint it
	// answers is /v1/systemone. BaseURL is empty because the port has no
	// chat lane for this provider; an empty base never resolves a connection.
	"v1m": {
		AuthHeader:   "Authorization",
		AuthScheme:   "bearer",
		SystemoneURL: "https://v1m.ir/v1/systemone",
	},
	"anthropic": {
		BaseURL:    "https://api.anthropic.com/v1/messages",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
	},
	"dahl": {
		BaseURL:    "https://inference.dahl.global/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"bai": {
		BaseURL:    "https://api.b.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"atria": {
		BaseURL:    "https://api.atria-asi.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"deepseek": {
		BaseURL:    "https://api.deepseek.com/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"groq": {
		BaseURL:    "https://api.groq.com/openai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"nvidia": {
		BaseURL:    "https://integrate.api.nvidia.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"openrouter": {
		BaseURL:      "https://openrouter.ai/api/v1/chat/completions",
		AuthHeader:   "Authorization",
		AuthScheme:   "bearer",
		ImageURL:     "https://openrouter.ai/api/v1/images/generations",
		VideoURL:     "https://openrouter.ai/api/v1/videos",
		SystemoneURL: "https://openrouter.ai/api/v1/systemone",
		StaticHeaders: map[string]string{
			"HTTP-Referer": "https://endpoint-proxy.local",
			"X-Title":      "Endpoint Proxy",
		},
	},
	"cerebras": {
		BaseURL:    "https://api.cerebras.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"together": {
		BaseURL:    "https://api.together.xyz/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"fireworks": {
		BaseURL:    "https://api.fireworks.ai/inference/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"opencode": {
		BaseURL:       "https://opencode.ai/zen/v1/chat/completions",
		AuthHeader:    "Authorization",
		AuthScheme:    "bearer",
		DefaultAPIKey: "public",
		NoAuth:        true,
		SystemoneURL:  "https://opencode.ai/zen/v1/systemone",
		StaticHeaders: map[string]string{"x-opencode-client": "desktop", "User-Agent": "opencode/1.18.31"},
	},
	// Pay-as-you-go lane (upstream open-sse/providers/registry/opencode-zen.js):
	// a key is required, and the same key answers /chat/completions, /messages
	// and /responses. NoAuth stays false so a connection with no key is refused
	// instead of silently falling back to the free tier's "public" placeholder.
	"opencode-zen": {
		BaseURL:      "https://opencode.ai/zen/v1/chat/completions",
		AuthHeader:   "Authorization",
		AuthScheme:   "bearer",
		UsageURL:     "https://opencode.ai/zen/v1/usage",
		SystemoneURL: "https://opencode.ai/zen/v1/systemone",
		StaticHeaders: map[string]string{
			"x-opencode-client": "desktop",
			"User-Agent":        "opencode/1.18.31",
		},
	},
	"gemini": {
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		Format:     "gemini-openai",
	},
	"antigravity": {
		BaseURL:    "https://daily-cloudcode-pa.googleapis.com",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		Format:     "gemini-native",
	},
	"github": {
		BaseURL:    "https://api.githubcopilot.com/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"copilot-integration-id":              "vscode-chat",
			"editor-version":                      "vscode/1.110.0",
			"editor-plugin-version":               "copilot-chat/0.38.0",
			"user-agent":                          "GitHubCopilotChat/0.38.0",
			"openai-intent":                       "conversation-panel",
			"x-github-api-version":                "2025-04-01",
			"x-vscode-user-agent-library-version": "electron-fetch",
			"X-Initiator":                         "user",
			"Accept":                              "application/json",
			"Content-Type":                        "application/json",
		},
	},
	"mistral": {
		BaseURL:    "https://api.mistral.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"perplexity": {
		BaseURL:    "https://api.perplexity.ai/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"xai": {
		BaseURL:    "https://api.x.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		ImageURL:   "https://api.x.ai/v1/images/generations",
		VideoURL:   "https://api.x.ai/v1/videos",
	},
	"cohere": {
		BaseURL:    "https://api.cohere.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"ollama": {
		BaseURL:     "http://localhost:11434/v1/chat/completions",
		AuthHeader:  "Authorization",
		AuthScheme:  "bearer",
		FetchURL:    "https://ollama.com/api/web_fetch",
		FetchMethod: "POST",
	},
	"siliconflow": {
		BaseURL:    "https://api.siliconflow.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"cloudflare-ai": {
		BaseURL:    "https://api.cloudflare.com/client/v4/accounts/" + os.Getenv("CLOUDFLARE_ACCOUNT_ID") + "/ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"mimo-free": {
		BaseURL:       "https://api.xiaomimimimo.com/api/free-ai/openai/chat",
		AuthHeader:    "Authorization",
		AuthScheme:    "bearer",
		DefaultAPIKey: "mimo-dynamic",
		NoAuth:        true,
	},
	"blackbox": {
		BaseURL:    "https://api.blackbox.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"featherless": {
		BaseURL:    "https://api.featherless.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"hyperbolic": {
		BaseURL:    "https://api.hyperbolic.xyz/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"kilocode": {
		BaseURL:    "https://api.kilo.ai/api/openrouter/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"nanobanana": {
		BaseURL:    "https://api.nanobananaapi.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"opencode-go": {
		BaseURL:    "https://opencode.ai/zen/go/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"venice": {
		BaseURL:    "https://api.venice.ai/api/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"vercel-ai-gateway": {
		BaseURL:    "https://ai-gateway.vercel.sh/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"volcengine-ark": {
		BaseURL:    "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"xiaomi-mimo": {
		BaseURL:    "https://api.xiaomimimo.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"xiaomi-tokenplan": {
		BaseURL:    "https://token-plan-sgp.xiaomimimo.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"chutes": {
		BaseURL:    "https://llm.chutes.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"cline": {
		BaseURL:    "https://api.cline.bot/api/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"HTTP-Referer":       "https://cline.bot",
			"X-Title":            "Cline",
			"User-Agent":         "Cline/3.0.61",
			"X-PLATFORM":         "cli",
			"X-PLATFORM-VERSION": "3.0.61",
			"X-CLIENT-TYPE":      "cline-cli",
			"X-CLIENT-VERSION":   "3.0.61",
			"X-CORE-VERSION":     "3.0.61",
			"X-IS-MULTIROOT":     "false",
		},
	},
	"alicode": {
		BaseURL:    "https://coding.dashscope.aliyuncs.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"alicode-intl": {
		BaseURL:    "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"byteplus": {
		BaseURL:    "https://ark.ap-southeast.bytepluses.com/api/coding/v3/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"codebuddy-cn": {
		BaseURL:    "https://copilot.tencent.com/v2/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"User-Agent":          "CLI/2.108.1 CodeBuddy/2.108.1",
			"X-Product":           "SaaS",
			"X-IDE-Type":          "CLI",
			"X-IDE-Name":          "CLI",
			"x-requested-with":    "XMLHttpRequest",
			"x-codebuddy-request": "1",
		},
	},
	"codebuddy-intl": {
		BaseURL:    "https://www.codebuddy.ai/v2/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"User-Agent":          "IDE/2.108.1 CodeBuddy/2.108.1",
			"X-Product":           "SaaS",
			"X-IDE-Type":          "IDE",
			"X-IDE-Name":          "IDE",
			"x-requested-with":    "XMLHttpRequest",
			"x-codebuddy-request": "1",
		},
	},
	"gitlab": {
		BaseURL:    "https://gitlab.com/api/v4/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"glm-cn": {
		BaseURL:    "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"glm": {
		BaseURL:    "https://api.z.ai/api/coding/paas/v4/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"kimchi": {
		BaseURL:    "https://llm.kimchi.dev/openai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"User-Agent": "kimchi/0.1.50",
		},
	},
	"iflow": {
		BaseURL:    "https://apis.iflow.cn/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"alitp-intl": {
		BaseURL:    "https://token-plan.ap-southeast-1.aliyuncs.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"fish-audio": {
		BaseURL:    "https://api.fish.audio/v1/tts",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},

	"nebius": {
		BaseURL:    "https://api.studio.nebius.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"minimax": {
		BaseURL:    "https://api.minimax.io/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"kimi": {
		BaseURL:    "https://api.kimi.com/coding/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"clinepass": {
		BaseURL:    "https://api.cline.bot/api/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"HTTP-Referer":       "https://cline.bot",
			"X-Title":            "Cline",
			"User-Agent":         "Cline/3.0.61",
			"X-PLATFORM":         "cli",
			"X-PLATFORM-VERSION": "3.0.61",
			"X-CLIENT-TYPE":      "cline-cli",
			"X-CLIENT-VERSION":   "3.0.61",
			"X-CORE-VERSION":     "3.0.61",
			"X-IS-MULTIROOT":     "false",
		},
	},
	"perplexity-agent": {
		BaseURL:    "https://api.perplexity.ai/v1/responses",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		Format:     "openai-responses",
	},
	"commandcode": {
		BaseURL:    "https://api.commandcode.ai/alpha/generate",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"User-Agent":             "commandcode/0.25.7 (cli)",
			"x-command-code-version": "0.25.7",
			"x-cli-environment":      "cli",
		},
	},
	"ollama-local": {
		BaseURL:    "http://localhost:11434/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"minimax-cn": {
		BaseURL:    "https://api.minimaxi.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"kimi-coding": {
		BaseURL:    "https://api.kimi.com/coding/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"claude": {
		BaseURL:    "https://api.anthropic.com/v1/messages",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
		StaticHeaders: map[string]string{
			"anthropic-version":                         "2023-06-01",
			"Anthropic-Beta":                            "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24,structured-outputs-2025-12-15,fast-mode-2026-02-01,redact-thinking-2026-02-12,token-efficient-tools-2026-03-28",
			"Anthropic-Dangerous-Direct-Browser-Access": "true",
			"User-Agent":                                "claude-cli/2.1.280 (external, sdk-cli)",
			"X-App":                                     "cli",
			"X-Stainless-Helper-Method":                 "stream",
			"X-Stainless-Retry-Count":                   "0",
		},
	},
	"codex": {
		BaseURL:    "https://chatgpt.com/backend-api/codex/responses",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"originator": "codex_cli_rs",
			"User-Agent": CodexCLIUserAgent,
			// Upstream sends `version` next to the User-Agent; the codex
			// backend gates newer models on the pair.
			"version": CodexCLIVersionHeader,
		},
	},
	"grok-cli": {
		BaseURL:    "https://cli-chat-proxy.grok.com/v1/responses",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"kiro": {
		BaseURL:    "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"elevenlabs": {
		BaseURL:    "https://api.elevenlabs.io",
		AuthHeader: "xi-api-key",
		AuthScheme: "raw",
		TTSURL:     "https://api.elevenlabs.io/v1/text-to-speech",
		VoicesURL:  "https://api.elevenlabs.io/v1/voices",
	},
	"deepgram": {
		BaseURL:    "https://api.deepgram.com",
		AuthHeader: "token",
		AuthScheme: "raw",
		STTURL:     "https://api.deepgram.com/v1/listen",
	},
	"assemblyai": {
		BaseURL:    "https://api.assemblyai.com",
		AuthHeader: "Authorization",
		AuthScheme: "raw",
		STTURL:     "https://api.assemblyai.com/v2/transcript",
	},
	"stability-ai": {
		BaseURL:    "https://api.stability.ai",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		ImageURL:   "https://api.stability.ai/v2beta/stable-image/generate",
	},
	"black-forest-labs": {
		BaseURL:    "https://api.bfl.ai",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		ImageURL:   "https://api.bfl.ai/v1",
	},
	"fal-ai": {
		BaseURL:    "https://queue.fal.run",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		ImageURL:   "https://queue.fal.run",
	},
	"recraft": {
		BaseURL:       "https://external.api.recraft.ai",
		AuthHeader:    "Authorization",
		AuthScheme:    "bearer",
		DefaultAPIKey: "public",
		ImageURL:      "https://external.api.recraft.ai/v1/images/generations",
	},
	"azure": {
		BaseURL:    "",
		AuthHeader: "api-key",
		AuthScheme: "raw",
	},
	"jina-reader": {
		BaseURL:     "https://r.jina.ai",
		AuthHeader:  "Authorization",
		AuthScheme:  "bearer",
		FetchURL:    "https://r.jina.ai",
		FetchMethod: "GET",
	},
	"firecrawl": {
		BaseURL:    "https://api.firecrawl.com/v1",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		FetchURL:   "https://api.firecrawl.com/v1/scrape",
	},
	"freebuff": {
		BaseURL:    "https://www.codebuff.com/api/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"User-Agent": "ai-sdk/openai-compatible/1.0/codebuff",
		},
	},

	"aws-polly": {
		BaseURL:    "https://polly.us-east-1.amazonaws.com/v1/speech",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"brave-search": {
		BaseURL:    "https://api.search.brave.com/res/v1/web/search",
		AuthHeader: "X-Subscription-Token",
		AuthScheme: "raw",
	},
	"cartesia": {
		BaseURL:    "https://api.cartesia.ai/tts/bytes",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
		TTSURL:     "https://api.cartesia.ai/tts/bytes",
	},
	"exa": {
		BaseURL:    "https://api.exa.ai/search",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
	},
	"huggingface": {
		BaseURL:    "https://api-inference.huggingface.co/models",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		ImageURL:   "https://api-inference.huggingface.co/models",
	},
	"inworld": {
		BaseURL:    "https://api.inworld.ai/tts/v1/voice",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		TTSURL:     "https://api.inworld.ai/tts/v1/voice",
	},
	"jina-ai": {
		BaseURL:    "https://api.jina.ai/v1/embeddings",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"linkup": {
		BaseURL:    "https://api.linkup.so/v1/search",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"perplexity-web": {
		BaseURL:    "https://www.perplexity.ai/rest/sse/perplexity_ask",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"playht": {
		BaseURL:    "https://api.play.ht/api/v2/tts/stream",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		TTSURL:     "https://api.play.ht/api/v2/tts/stream",
	},
	// TinyFish — one x-api-key credential behind two separate hosts (search
	// and fetch), which is why BaseURL is a non-endpoint root here: appending
	// /v1/search to it would hit a host that does not serve search.
	"tinyfish": {
		BaseURL:    "https://api.tinyfish.ai",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
		FetchURL:   "https://api.fetch.tinyfish.ai",
		FetchMethod: "POST",
	},
	"runwayml": {
		BaseURL:    "https://api.dev.runwayml.com/v1",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		ImageURL:   "https://api.dev.runwayml.com/v1",
	},
	"searchapi": {
		BaseURL:    "https://www.searchapi.io/api/v1/search",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"serper": {
		BaseURL:    "https://google.serper.dev",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
	},
	"tavily": {
		BaseURL:    "https://api.tavily.com/search",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"xquik": {
		BaseURL:    "https://xquik.com/api/v1/x/tweets/search",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
	},
	"ollama-search": {
		BaseURL:    "https://ollama.com/api/web_search",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"zai-search": {
		BaseURL:    "https://api.z.ai/api/mcp/web_search",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"vertex": {
		BaseURL:    "https://aiplatform.googleapis.com/v1",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"vertex-partner": {
		BaseURL:    "https://aiplatform.googleapis.com/v1",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"voyage-ai": {
		BaseURL:    "https://api.voyageai.com/v1/embeddings",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"youcom": {
		BaseURL:    "https://ydc-index.io/v1/search",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
	},
	"qoder": {
		BaseURL:    "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	// Qoder CN is a distinct deployment with its own gateway and credentials.
	// It is never aliased onto qoder (AGENTS.md section 3.A).
	"qoder-cn": {
		BaseURL:    "https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"grok-web": {
		BaseURL:    "https://grok.com/rest/app-chat/conversations/new",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},

	"sdwebui": {
		BaseURL:  "http://localhost:7860/sdapi/v1/txt2img",
		NoAuth:   true,
		ImageURL: "http://localhost:7860/sdapi/v1/txt2img",
	},
	"searxng": {
		BaseURL: "http://localhost:4000/search",
		NoAuth:  true,
	},
	"comfyui": {
		BaseURL:  "http://localhost:8188",
		NoAuth:   true,
		ImageURL: "http://localhost:8188",
	},
	"tortoise": {
		BaseURL: "http://localhost:5000/api/tts",
		NoAuth:  true,
		TTSURL:  "http://localhost:5000/api/tts",
	},
	"coqui": {
		BaseURL: "http://localhost:5002/api/tts",
		NoAuth:  true,
		TTSURL:  "http://localhost:5002/api/tts",
	},
	"edge-tts": {
		BaseURL: "",
		NoAuth:  true,
	},
	"google-tts": {
		BaseURL: "",
		NoAuth:  true,
	},
	"local-device": {
		BaseURL: "",
		NoAuth:  true,
	},
	"topaz": {
		BaseURL:    "https://api.topaz.sh",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"cursor": {
		BaseURL:    "https://api2.cursor.sh",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"gemini-cli": {
		BaseURL:    "https://daily-cloudcode-pa.googleapis.com/v1internal",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"google-pse": {
		BaseURL:    "https://www.googleapis.com/customsearch/v1",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"alims-intl": {
		BaseURL:    "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"api-airforce": {
		BaseURL:    "https://api.airforce/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"HTTP-Referer": "https://endpoint-proxy.local",
			"X-Title":      "Endpoint Proxy",
		},
	},
	"baidu": {
		BaseURL:    "https://qianfan.baidubce.com/v2/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"bazaarlink": {
		BaseURL:    "https://bazaarlink.ai/api/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"bluesminds": {
		BaseURL:    "https://api.bluesminds.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"devin-cli": {
		BaseURL:    "devin://acp/stdio",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"kilo-gateway": {
		BaseURL:    "https://api.kilo.ai/api/gateway/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"llm7": {
		BaseURL:    "https://api.llm7.io/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"morph": {
		BaseURL:    "https://api.morphllm.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"poolside": {
		BaseURL:    "https://inference.poolside.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"sambanova": {
		BaseURL:    "https://api.sambanova.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"tencent": {
		BaseURL:    "https://api.hunyuan.cloud.tencent.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"tokenharbor": {
		BaseURL:    "https://tokenharbor.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"tokenrouter": {
		BaseURL:    "https://api.tokenrouter.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"trae": {
		BaseURL:    "https://core-normal.trae.ai/api/remote/v1",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"X-Trae-Client-Type":     "web",
			"X-Preferenced-Language": "en",
			"Referer":                "https://solo.trae.ai/",
		},
	},
	"windsurf": {
		BaseURL:    "https://server.codeium.com/exa.language_server_pb.LanguageServerService/GetChatMessage",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"Content-Type": "application/grpc-web+proto",
			"Accept":       "application/grpc-web+proto",
			"X-Grpc-Web":   "1",
		},
	},
	"zed": {
		BaseURL:    "https://cloud.zed.dev/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"content-type": "application/json",
		},
	},
}

// RetryableStatusCodes are HTTP status codes that trigger account fallback.
var RetryableStatusCodes = map[int]bool{
	http.StatusUnauthorized:       true, // 401
	http.StatusForbidden:          true, // 403 (Gemini/antigravity daily-quota errors can come as 403)
	http.StatusTooManyRequests:    true, // 429
	http.StatusBadGateway:         true, // 502
	http.StatusServiceUnavailable: true, // 503
	http.StatusGatewayTimeout:     true, // 504
}
