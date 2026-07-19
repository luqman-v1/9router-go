package providers

import (
	"net/http"
	"os"
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
}

// IsGeminiNative returns true if provider uses Gemini-native format.
func (p *ProviderConfig) IsGeminiNative() bool { return p.Format == "gemini-native" }

// KnownProviders maps provider IDs to their upstream configuration.
var KnownProviders = map[string]ProviderConfig{
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
	"antigravity": {
		BaseURL:    "https://cloudcode-pa.googleapis.com",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		Format:     "gemini-native",
	},
	"github": {
		BaseURL:    "https://api.githubcopilot.com/chat/completions",
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
		ImageURL:   "https://api.x.ai/v1/images/generations",
		VideoURL:   "https://api.x.ai/v1/videos",
	},
	"cohere": {
		BaseURL:    "https://api.cohere.ai/v1/chat/completions",
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
	},
	"iflow": {
		BaseURL:    "https://apis.iflow.cn/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},

	"nebius": {
		BaseURL:    "https://api.studio.nebius.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"qwen": {
		BaseURL:    "https://portal.qwen.ai/v1/chat/completions",
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
			"anthropic-version": "2023-06-01",
				"Anthropic-Beta": "claude-code-20250219,interleaved-thinking-2025-05-14",
			},
		},
		"codex": {
			BaseURL:    "https://chatgpt.com/backend-api/codex/responses",
			AuthHeader: "Authorization",
			AuthScheme: "bearer",
			StaticHeaders: map[string]string{
				"originator": "codex_cli_rs",
			},
		},
		"grok-cli": {
			BaseURL:    "https://cli-chat-proxy.grok.com",
			AuthHeader: "Authorization",
			AuthScheme: "bearer",
		},
		"kiro": {
			BaseURL:    "https://runtime.us-east-1.kiro.dev/generateAssistantResponse",
			AuthHeader: "Authorization",
			AuthScheme: "bearer",
		},
		"elevenlabs": {
			BaseURL:    "https://api.elevenlabs.io",
			AuthHeader: "xi-api-key",
			AuthScheme: "raw",
			TTSURL:     "https://api.elevenlabs.io/v1/text-to-speech",
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
}

// ProviderAliasMap maps short aliases to canonical provider IDs.
var ProviderAliasMap = map[string]string{
		"aai": "assemblyai",
		"ag": "antigravity",
		"ali": "alicode",
		"alii": "alicode-intl",
		"ant": "anthropic",
		"ark": "volcengine-ark",
		"az": "azure",
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
		"cb": "cerebras",
	"cd": "codebuddy-cn",
	"gl": "gitlab",
	"glmcn": "glm-cn",
	"if": "iflow",
	"ne": "nebius",
	"qw": "qwen",
	"vali": "volcengine-ark",
	"vercel": "vercel-ai-gateway",
	"vn": "venice",
	"xmtp": "xiaomi-tokenplan",
		"km": "kimi",
		"mm": "minimax",
		"cp": "clinepass",
		"pa": "perplexity-agent",
}

// RetryableStatusCodes are HTTP status codes that trigger account fallback.
var RetryableStatusCodes = map[int]bool{
	http.StatusUnauthorized:    true, // 401
	http.StatusTooManyRequests: true, // 429
	}
