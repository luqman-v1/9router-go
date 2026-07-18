package handlers

import "net/http"

// knownProviders maps provider IDs to their upstream configuration.
var knownProviders = map[string]ProviderConfig{
	"openai": {
		BaseURL:    "https://api.openai.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"anthropic": {
		BaseURL:    "https://api.anthropic.com/v1/messages",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
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
		BaseURL:    "https://openrouter.ai/api/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
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
		StaticHeaders: map[string]string{"x-opencode-client": "desktop"},
	},
	"gemini": {
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"github": {
		BaseURL:    "https://models.inference.ai.azure.com/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
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
	},
	"cohere": {
		BaseURL:    "https://api.cohere.com/v2/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"ollama": {
		BaseURL:    "http://localhost:11434/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"siliconflow": {
		BaseURL:    "https://api.siliconflow.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"cloudflare-ai": {
		BaseURL:    "https://api.cloudflare.com/client/v4/ai/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"mimo-free": {
		BaseURL:       mimoChatURL,
		AuthHeader:    "Authorization",
		AuthScheme:    "bearer",
		DefaultAPIKey: "mimo-dynamic",
		NoAuth:        true,
	},
}

// providerAliasMap maps all short aliases from 9router's open-sse registry to
// canonical provider IDs. This ensures any combo entry format (e.g. "oc/model")
// resolves correctly regardless of whether the provider has a hardcoded config.
var providerAliasMap = map[string]string{
	"aai": "assemblyai",
	"ag": "antigravity",
	"ark": "volcengine-ark",
	"bb": "blackbox",
	"bfl": "black-forest-labs",
	"bpm": "byteplus",
	"brave": "brave-search",
	"cc": "claude",
	"cf": "cloudflare-ai",
	"ch": "chutes",
	"cl": "cline",
	"cmc": "commandcode",
	"cu": "cursor",
	"cx": "codex",
	"dg": "deepgram",
	"ds": "deepseek",
	"el": "elevenlabs",
	"fal": "fal-ai",
	"fl": "featherless",
	"fw": "fireworks",
	"gb": "grok-cli",
	"gc": "gemini-cli",
	"gcli": "grok-cli",
	"gh": "github",
	"gpse": "google-pse",
	"gq": "groq",
	"grok-build": "grok-cli",
	"gw": "grok-web",
	"hf": "huggingface",
	"hyp": "hyperbolic",
	"jina": "jina-ai",
	"kc": "kilocode",
	"kr": "kiro",
	"mimo": "xiaomi-mimo",
	"mmf": "mimo-free",
	"nb": "nanobanana",
	"nv": "nvidia",
	"oa": "openai",
	"oc": "opencode",
	"ocg": "opencode-go",
	"or": "openrouter",
	"polly": "aws-polly",
	"pplx": "perplexity",
	"pplx-agent": "perplexity-agent",
	"pplx-responses": "perplexity-agent",
	"pw": "perplexity-web",
	"qd": "qoder",
	"runway": "runwayml",
	"stability": "stability-ai",
	"tg": "together",
	"ant": "anthropic",
	"cb": "cerebras",
	"vercel": "vercel-ai-gateway",
	"vn": "venice",
	"vx": "vertex",
	"vxp": "vertex-partner",
	"xmtp": "xiaomi-tokenplan",
}

// retryableStatusCodes are HTTP status codes that trigger account fallback.
var retryableStatusCodes = map[int]bool{
	http.StatusUnauthorized:    true, // 401
	http.StatusTooManyRequests: true, // 429
}
