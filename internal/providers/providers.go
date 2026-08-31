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
	VoicesURL     string            // override /audio/voices listing endpoint
	FetchURL      string            // override /web/fetch endpoint (Jina, Firecrawl, etc.)
	FetchMethod   string            // HTTP method for fetch: GET or POST (default POST)
}

// IsGeminiNative returns true if provider uses Gemini-native format.
func (p *ProviderConfig) IsGeminiNative() bool { return p.Format == "gemini-native" }

// IsGeminiOpenAICompat returns true if provider is Gemini behind an
// OpenAI-compatible endpoint whose tool schema validation is as strict as the
// native one (e.g. the "gemini" provider at /v1beta/openai/chat/completions).
func (p *ProviderConfig) IsGeminiOpenAICompat() bool { return p.Format == "gemini-openai" }

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
		StaticHeaders: map[string]string{"x-opencode-client": "desktop"},
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
		StaticHeaders: map[string]string{
			"HTTP-Referer": "https://cline.bot",
			"X-Title":      "Cline",
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
			"HTTP-Referer": "https://cline.bot",
			"X-Title":      "Cline",
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
			"Anthropic-Beta":    "claude-code-20250219,interleaved-thinking-2025-05-14",
		},
	},
	"codex": {
		BaseURL:    "https://chatgpt.com/backend-api/codex/responses",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"originator": "codex_cli_rs",
			"User-Agent": "codex_cli_rs/0.136.0",
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
		BaseURL:    "https://api.xquik.com/v1/x/search-tweets",
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
	"grok-web": {
		BaseURL:    "https://grok.com/rest/app-chat/conversations/new",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	
	"sdwebui": {
		BaseURL:    "http://localhost:7860/sdapi/v1/txt2img",
		NoAuth:     true,
		ImageURL:   "http://localhost:7860/sdapi/v1/txt2img",
	},
	"searxng": {
		BaseURL:    "http://localhost:4000/search",
		NoAuth:     true,
	},
	"comfyui": {
		BaseURL:    "http://localhost:8188",
		NoAuth:     true,
		ImageURL:   "http://localhost:8188",
	},
	"tortoise": {
		BaseURL:    "http://localhost:5000/api/tts",
		NoAuth:     true,
		TTSURL:     "http://localhost:5000/api/tts",
	},
	"coqui": {
		BaseURL:    "http://localhost:5002/api/tts",
		NoAuth:     true,
		TTSURL:     "http://localhost:5002/api/tts",
	},
	"edge-tts": {
		BaseURL:    "",
		NoAuth:     true,
	},
	"google-tts": {
		BaseURL:    "",
		NoAuth:     true,
	},
	"local-device": {
		BaseURL:    "",
		NoAuth:     true,
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
