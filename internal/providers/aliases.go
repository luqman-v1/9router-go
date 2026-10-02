package providers

// ProviderAliasMap maps short aliases to canonical provider IDs.
var ProviderAliasMap = map[string]string{
	"b-ai":           "bai",
	"agnes-ai":       "agnes",
	"atria-asi":      "atria",
	"dahl-inference": "dahl",
	"thh":            "tokenharbor",
	"th":             "tokenharbor",
	"aai":            "assemblyai",
	"ag":             "antigravity",
	"ali":            "alicode",
	"ali-tp":         "alitp-intl",
	"alii":           "alicode-intl",
	"alitp":          "alitp-intl",
	"ant":            "anthropic",
	"ark":            "volcengine-ark",
	"az":             "azure",
	"bb":             "blackbox",
	"bfl":            "black-forest-labs",
	"bpm":            "byteplus",
	"brave":          "brave-search",
	"cb":             "cerebras",
	"cbai":           "codebuddy-intl",
	"cc":             "claude",
	"cd":             "codebuddy-cn",
	"cf":             "cloudflare-ai",
	"ch":             "chutes",
	"cl":             "cline",
	"cmc":            "commandcode",
	"cp":             "clinepass",
	"cu":             "cursor",
	"cx":             "codex",
	"dg":             "deepgram",
	"ds":             "deepseek",
	"el":             "elevenlabs",
	"fal":            "fal-ai",
	"fb":             "freebuff",
	"fish":           "fish-audio",
	"fl":             "featherless",
	"fw":             "fireworks",
	"gb":             "grok-cli",
	"gc":             "gemini-cli",
	"gcli":           "grok-cli",
	"gh":             "github",
	"gl":             "gitlab",
	"glmcn":          "glm-cn",
	"gpse":           "google-pse",
	"gq":             "groq",
	"grok-build":     "grok-cli",
	"gw":             "grok-web",
	"hf":             "huggingface",
	"hyp":            "hyperbolic",
	"if":             "iflow",
	"jina":           "jina-ai",
	"kc":             "kilocode",
	"km":             "kimi",
	"kr":             "kiro",
	"mimo":           "xiaomi-mimo",
	"mm":             "minimax",
	"mmf":            "mimo-free",
	"nb":             "nanobanana",
	"muse-ai":           "muse",
	"meta-model-api":    "muse",
	"muse-code":         "muse",
	"muse-subscription": "muse",
	"systemone":         "v1m",
	// Upstream publishes `jev` as a v1m alias, but jev-1.13-free already
	// belongs to opencode and jev-1.13 to opencode-zen. Registering it here
	// would let `jev/<model>` cross into v1m, which AGENTS.md §3 forbids and
	// which would silently hijack a model id two other providers already own.
	// The canonical `v1m/` prefix still reaches every v1m model.
	"ne":             "nebius",
	"nv":             "nvidia",
	"oa":             "openai",
	"oc":             "opencode",
	"ocz":            "opencode-zen",
	"or":             "openrouter",
	"pa":             "perplexity-agent",
	"polly":          "aws-polly",
	"pplx":           "perplexity",
	"pplx-agent":     "perplexity-agent",
	"pplx-responses": "perplexity-agent",
	"pw":             "perplexity-web",
	"qd":             "qoder",
	"qdcn":           "qoder-cn",
	"runway":         "runwayml",
	"stability":      "stability-ai",
	"tg":             "together",
	"vali":           "volcengine-ark",
	"vercel":         "vercel-ai-gateway",
	"vn":             "venice",
	"xmtp":           "xiaomi-tokenplan",
	"af":             "api-airforce",
	"bzl":            "bazaarlink",
	"bm":             "bluesminds",
	"cbcn":           "codebuddy-cn",
	"dv":             "devin-cli",
	"hunyuan":        "tencent",
	"kgw":            "kilo-gateway",
	"ps":             "poolside",
	"qianfan":        "baidu",
	"samba":          "sambanova",
	"tr":             "trae",
	"voyage":         "voyage-ai",
	"vx":             "vertex",
	"vxp":            "vertex-partner",
	"ws":             "windsurf",
	"xq":             "xquik",
	"zd":             "zed",
}

// ResolveAlias returns the canonical provider ID for an alias, or the alias itself if not found.
func ResolveAlias(alias string) string {
	if canonical, ok := ProviderAliasMap[alias]; ok {
		return canonical
	}
	return alias
}

// IsNoAuthProvider reports whether a provider is marked noAuth in the registry,
// i.e. it is usable without any configured credential. Aliases resolve to their
// canonical id first, so callers may pass either form.
func IsNoAuthProvider(providerID string) bool {
	canon := ResolveAlias(providerID)
	if cfg, ok := KnownProviders[canon]; ok {
		return cfg.NoAuth
	}
	return false
}

// GetProviderAlias returns the alias upstream publishes a provider under, ported
// from the registry's uiAlias/alias. Providers without a registry alias are
// published under their id (clinepass, nvidia, openrouter, openai) — exactly
// like upstream getProviderAlias: AI_PROVIDERS[id]?.alias || id.
//
// ProviderAliasMap stays the alias -> canonical id table used when RESOLVING
// incoming model ids (cp/* still routes to clinepass), so removing the old
// first-wins inverse here only changes the published prefix.
func GetProviderAlias(providerID string) string {
	if alias, ok := RegistryAliases[providerID]; ok && alias != "" {
		return alias
	}
	return providerID
}
