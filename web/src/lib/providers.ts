// Auto-generated provider catalog matching upstream 9router
export interface ProviderCatalogItem {
  id: string
  name: string
  category: 'oauth' | 'free' | 'freeTier' | 'apikey' | 'webCookie' | 'custom'
  alias: string
  color: string
  icon: string
  noAuth?: boolean
  priority?: number
  mediaPriority?: number
  hiddenKinds?: string[]
  serviceKinds?: string[]
  /** auth modes from upstream registry (e.g. clinepass ["apikey","oauth"] = dual buttons). */
  authModes?: string[]
  /** hidden from provider list (upstream parity) but detail page stays reachable. */
  hidden?: boolean
  /** Region choices for cluster-specific providers (upstream registry `regions`). */
  regions?: { id: string; label: string }[]
  defaultRegion?: string
  /** Upstream display.website + display.notice (signup/apiKey links). */
  website?: string
  notice?: { text?: string; apiKeyUrl?: string; signupUrl?: string }
  /** Upstream registry authType/authHint (cookie login hint, apikey/oauth/none markers). */
  authType?: string
  authHint?: string
  /** Upstream registry modelsFetcher (public catalog for "Suggested free models"). */
  modelsFetcher?: { url: string; type: string }
  /** Upstream registry systemoneConfig */
  systemoneConfig?: { baseUrl?: string; format?: string; headers?: Record<string, string> }
  /** Upstream registry passthroughModels: unknown ids are accepted unvalidated. */
  passthroughModels?: boolean
  /** Upstream registry credentialForm: which credential form the dashboard renders. */
  credentialForm?: string
  /**
   * Upstream registry apiKeyOptionalWith: the providerSpecificData field that stands in
   * for an API key, so the create and validate routes accept an empty key rather than
   * rejecting a documented setup.
   */
  apiKeyOptionalWith?: string
  /** Upstream registry features.usage: the quota tracker can read this provider. */
  usage?: boolean
  searchConfig?: Record<string, any>
  fetchConfig?: Record<string, any>
  searchViaChat?: {
    defaultModel?: string
    endpoint?: string
    pricingUrl?: string
    freeTier?: string
  }
}

export const PROVIDER_CATEGORIES = [
  { id: 'custom', label: 'Custom (OpenAI / Anthropic Compatible)' },
  { id: 'oauth', label: 'OAuth Providers' },
  { id: 'free', label: 'Free Providers (No Key)' },
  { id: 'freeTier', label: 'Free Tier Providers' },
  { id: 'apikey', label: 'API Key Providers' },
  { id: 'webCookie', label: 'Web Cookie Providers' },
] as const

export const PROVIDER_CATALOG: ProviderCatalogItem[] = [
  {
    "id": "agnes",
    "name": "Agnes AI",
    "category": "freeTier",
    "alias": "agnes",
    "color": "#7C3AED",
    "icon": "auto_awesome",
    "website": "https://agnes-ai.com",
    "notice": {"text":"OpenAI-compatible gateway from Agnes AI, offering free API credits on sign-up. Accepts a bearer token or an x-api-key header.", "apiKeyUrl":"https://platform.agnes-ai.com"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "atria",
    "name": "Atria Dawn",
    "category": "apikey",
    "alias": "atria",
    "color": "#C2410C",
    "icon": "flare",
    "website": "https://atria-asi.ai",
    "notice": {"text":"OpenAI-compatible endpoint from Atria Dawn (AtomInnoLab). Currently a research preview offering a single text model, Atria-Dawn-Preview.", "apiKeyUrl":"https://api.atria-asi.ai/dashboard"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "bai",
    "name": "B.AI",
    "category": "apikey",
    "alias": "bai",
    "color": "#0369A1",
    "icon": "account_balance",
    "website": "https://b.ai",
    "notice": {"text":"OpenAI-compatible gateway with one of the larger catalogues here. Accepts a bearer token or an x-api-key header. Model ids are fetched live from the provider.", "apiKeyUrl":"https://b.ai"},
    "modelsFetcher": {"url": "https://api.b.ai/v1/models", "type": "openai"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "dahl",
    "name": "Dahl Inference",
    "category": "apikey",
    "alias": "dahl",
    "color": "#1E40AF",
    "icon": "hub",
    "website": "https://dahl.global",
    "notice": {"text":"OpenAI-compatible Gonka inference node. Small, fixed catalogue (GLM-5.3-Flash, DeepSeek-V4-Flash, MiniMax-M2.7) at a flat per-token rate.", "apiKeyUrl":"https://dahl.global/dashboard"},
    "modelsFetcher": {"url": "https://inference.dahl.global/v1/models", "type": "openai"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "tokenharbor",
    "name": "Token Harbor",
    "category": "apikey",
    "alias": "tokenharbor",
    "color": "#0F766E",
    "icon": "anchor",
    "website": "https://tokenharbor.ai",
    "notice": {"text":"OpenAI-compatible aggregator. One API key reaches every model, billed per-token from a prepaid wallet. Model ids are bare (e.g. claude-opus-5.5, gpt-6-astra, qwen3.8-flash:free) and are fetched live from the provider.", "apiKeyUrl":"https://tokenharbor.ai/dashboard"},
    "modelsFetcher": {"url": "https://tokenharbor.ai/v1/models", "type": "openai"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "antigravity",
    "name": "Antigravity",
    "category": "oauth",
    "alias": "ag",
    "color": "#F59E0B",
    "icon": "rocket_launch",
    "website": "https://antigravity.google",
    "notice": {"signupUrl":"https://antigravity.google"},
    "noAuth": false,
    "priority": 20,
    "serviceKinds": [
      "llm",
      "image",
      "webSearch"
    ],
    "searchViaChat": {
      "defaultModel": "gemini-2.5-flash",
      "endpoint": "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent",
      "freeTier": "Free — Google Search grounding through an Antigravity OAuth account."
    }
  },
  {
    "id": "claude",
    "name": "Claude Code",
    "category": "oauth",
    "alias": "cc",
    "color": "#D97757",
    "icon": "smart_toy",
    "website": "https://claude.ai",
    "notice": {"signupUrl":"https://claude.ai"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "cline",
    "name": "Cline",
    "category": "oauth",
    "alias": "cl",
    "color": "#5B9BD5",
    "icon": "smart_toy",
    "website": "https://cline.bot",
    "notice": {"signupUrl":"https://cline.bot"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "clinepass",
    "name": "ClinePass",
    "category": "oauth",
    "alias": "clinepass",
    "color": "#5B9BD5",
    "icon": "vpn_key",
    "noAuth": false,
    "authModes": ["apikey", "oauth"],
    "website": "https://cline.bot",
    "notice": { "signupUrl": "https://app.cline.bot" },
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "codebuddy-intl",
    "name": "CodeBuddy",
    "category": "oauth",
    "alias": "cbai",
    "color": "#006EFF",
    "icon": "smart_toy",
    "website": "https://www.codebuddy.ai",
    "notice": {"signupUrl":"https://www.codebuddy.ai"},
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "codebuddy-cn",
    "name": "CodeBuddy CN",
    "category": "oauth",
    "alias": "cbcn",
    "color": "#006EFF",
    "icon": "smart_toy",
    "website": "https://copilot.tencent.com",
    "notice": {"signupUrl":"https://copilot.tencent.com"},
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "cursor",
    "name": "Cursor IDE",
    "category": "oauth",
    "alias": "cu",
    "color": "#00D4AA",
    "icon": "edit_note",
    "website": "https://cursor.com",
    "notice": {"signupUrl":"https://cursor.com"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "github",
    "name": "GitHub Copilot",
    "category": "oauth",
    "alias": "gh",
    "color": "#333333",
    "icon": "code",
    "website": "https://github.com/features/copilot",
    "notice": {"signupUrl":"https://github.com/features/copilot"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding"
    ]
  },
  {
    "id": "gitlab",
    "name": "GitLab Duo",
    "category": "oauth",
    "alias": "gitlab",
    "color": "#FC6D26",
    "icon": "code",
    "website": "https://gitlab.com",
    "notice": {"signupUrl":"https://gitlab.com"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "grok-cli",
    "name": "Grok CLI (Grok Build)",
    "category": "oauth",
    "alias": "gcli",
    "color": "#1DA1F2",
    "icon": "auto_awesome",
    "website": "https://x.ai",
    "notice": {"text":"Sign in with your xAI / Grok account via device code. Uses Grok Build subscription credits (cli-chat-proxy.grok.com).","signupUrl":"https://grok.com/supergrok"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "iflow",
    "name": "iFlow AI",
    "category": "oauth",
    "alias": "if",
    "color": "#6366F1",
    "icon": "water_drop",
    "website": "https://iflow.cn",
    "notice": {"signupUrl":"https://iflow.cn"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "kilocode",
    "name": "Kilo Code",
    "category": "oauth",
    "alias": "kc",
    "color": "#FF6B35",
    "icon": "code",
    "website": "https://kilocode.ai",
    "notice": {"signupUrl":"https://kilocode.ai"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ],
    "modelsFetcher": {"url":"https://api.kilo.ai/api/gateway/models","type":"openrouter-free"}
  },
  {
    "id": "kimi",
    "name": "Kimi",
    "category": "oauth",
    "alias": "kimi",
    "color": "#1E3A8A",
    "icon": "psychology",
    "website": "https://kimi.moonshot.cn",
    "notice": {"apiKeyUrl":"https://platform.moonshot.ai/console/api-keys","signupUrl":"https://www.kimi.com/code"},
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "priority": 170,
    "serviceKinds": [
      "llm",
      "webSearch"
    ]
  },
  {
    "id": "muse",
    "name": "Muse (Meta Model API)",
    "category": "oauth",
    "alias": "muse",
    "color": "#0866FF",
    "icon": "auto_awesome",
    "website": "https://muse.ai",
    "notice": {"text":"Sign in with your Meta account (Muse Code subscription) or paste a Model API key from dev.meta.ai. Subscription keys are minted per account; Meta may train on contributor-tier data.", "apiKeyUrl":"https://dev.meta.ai", "signupUrl":"https://muse.ai"},
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "authType": "apikey",
    "priority": 120,
    "passthroughModels": true,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "codex",
    "name": "OpenAI Codex",
    "category": "oauth",
    "alias": "cx",
    "color": "#3B82F6",
    "icon": "code",
    "website": "https://chatgpt.com/codex",
    "notice": {"signupUrl":"https://chatgpt.com/codex"},
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "image"
    ]
  },
  {
    "id": "qoder-cn",
    "name": "Qoder CN",
    "category": "oauth",
    "alias": "qdcn",
    "color": "#EC4899",
    "icon": "water_drop",
    "website": "https://qoder.com.cn",
    "notice": {"signupUrl":"https://qoder.com.cn"},
    "authHint": "Personal Access Token (pt-...) from https://qoder.com.cn/account/integrations",
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "qoder",
    "name": "Qoder",
    "category": "oauth",
    "alias": "qd",
    "color": "#EC4899",
    "icon": "water_drop",
    "website": "https://qoder.com",
    "notice": {"signupUrl":"https://qoder.com"},
    "authHint": "Personal Access Token (pt-...) từ https://qoder.com/account/integrations",
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "xai",
    "name": "xAI (Grok)",
    "category": "oauth",
    "alias": "xai",
    "color": "#1DA1F2",
    "icon": "auto_awesome",
    "website": "https://x.ai",
    "notice": {"apiKeyUrl":"https://console.x.ai"},
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "priority": 280,
    "serviceKinds": [
      "llm",
      "image",
      "video",
      "webSearch"
    ]
  },
  {
    "id": "xiaomi-mimo",
    "name": "Xiaomi MiMo",
    "category": "oauth",
    "alias": "mimo",
    "color": "#FF6900",
    "icon": "smart_toy",
    "website": "https://xiaomimimo.com",
    "notice": {"apiKeyUrl":"https://platform.xiaomimimo.com/console/api-keys","signupUrl":"https://mimo.xiaomimimo.com/desktop/invite/"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 290,
    "authModes": ["oauth", "apikey"],
    "serviceKinds": [
      "llm",
      "tts"
    ]
  },
  {
    "id": "zed",
    "name": "Zed",
    "category": "oauth",
    "alias": "zd",
    "color": "#A855F7",
    "icon": "code",
    "website": "https://zed.dev",
    "notice": {"signupUrl":"https://zed.dev/native_app_signin"},
    "authType": "oauth",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "freebuff",
    "name": "Freebuff",
    "category": "oauth",
    "alias": "fb",
    "color": "#0a0a0b",
    "icon": "bolt",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "gemini-cli",
    "name": "Gemini CLI",
    "category": "free",
    "alias": "gc",
    "color": "#4285F4",
    "icon": "terminal",
    "website": "https://github.com/google-gemini/gemini-cli",
    "notice": {"signupUrl":"https://github.com/google-gemini/gemini-cli"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "kiro",
    "name": "Kiro AI",
    "category": "free",
    "alias": "kr",
    "color": "#FF6B35",
    "icon": "psychology_alt",
    "website": "https://kiro.dev",
    "notice": {"signupUrl":"https://kiro.dev"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "mimo-free",
    "name": "MiMo Code Free",
    "category": "free",
    "alias": "mmf",
    "color": "#FF6900",
    "icon": "smart_toy",
    "noAuth": true,
    "serviceKinds": [
      "llm"
    ],
    "modelsFetcher": {"url":"https://models.dev/api.json","type":"mimo-free"}
  },
  {
    "id": "opencode",
    "name": "OpenCode Free",
    "category": "free",
    "alias": "oc",
    "color": "#E87040",
    "icon": "terminal",
    "noAuth": true,
    "priority": 40,
    "systemoneConfig": {
      "baseUrl": "https://opencode.ai/zen/v1/systemone",
      "format": "systemone"
    },
    "serviceKinds": [
      "llm",
      "systemone"
    ],
    "modelsFetcher": {"url":"https://opencode.ai/zen/v1/models","type":"opencode-free"}
  },
  {
    "id": "api-airforce",
    "name": "API.airforce",
    "category": "freeTier",
    "alias": "af",
    "color": "#0EA5E9",
    "icon": "flight",
    "website": "https://api.airforce",
    "notice": {"apiKeyUrl":"https://api.airforce"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ],
    "modelsFetcher": {"url":"https://api.airforce/v1/models","type":"airforce-free"}
  },
  {
    "id": "bazaarlink",
    "name": "Bazaarlink",
    "category": "freeTier",
    "alias": "bzl",
    "color": "#DC2626",
    "icon": "storefront",
    "website": "https://bazaarlink.ai",
    "notice": {"apiKeyUrl":"https://bazaarlink.ai"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "byteplus",
    "name": "BytePlus ModelArk",
    "category": "freeTier",
    "alias": "bpm",
    "color": "#2563EB",
    "icon": "cloud",
    "website": "https://console.byteplus.com/ark",
    "notice": {"text":"Free credits for new accounts. Access to Seed 2.0, Kimi K2 Thinking, GLM 4.7, GPT-OSS-120B models.","apiKeyUrl":"https://console.byteplus.com/ark/region:ark+ap-southeast-1/apiKey"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "cloudflare-ai",
    "name": "Cloudflare",
    "category": "freeTier",
    "alias": "cf",
    "color": "#F38020",
    "icon": "cloud",
    "website": "https://developers.cloudflare.com/workers-ai/",
    "notice": {"text":"Workers AI free tier. Requires a Cloudflare API token and Account ID.","apiKeyUrl":"https://dash.cloudflare.com/profile/api-tokens"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "image",
      "systemone"
    ]
  },
  {
    "id": "coqui",
    "name": "Coqui TTS",
    "category": "freeTier",
    "alias": "coqui",
    "color": "#10B981",
    "icon": "record_voice_over",
    "website": "https://github.com/coqui-ai/TTS",
    "authType": "none",
    "noAuth": true,
    "hidden": true,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "edge-tts",
    "name": "Edge TTS",
    "category": "freeTier",
    "alias": "edge-tts",
    "color": "#0078D4",
    "icon": "record_voice_over",
    "authType": "none",
    "noAuth": true,
    "mediaPriority": 5,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "gemini",
    "name": "Gemini",
    "category": "freeTier",
    "alias": "gemini",
    "color": "#4285F4",
    "icon": "diamond",
    "website": "https://ai.google.dev",
    "notice": {"apiKeyUrl":"https://aistudio.google.com/app/apikey"},
    "authType": "apikey",
    "noAuth": false,
    "mediaPriority": 1,
    "priority": 50,
    "serviceKinds": [
      "llm",
      "image",
      "tts",
      "stt",
      "embedding",
      "webSearch"
    ]
  },
  {
    "id": "google-tts",
    "name": "Google TTS",
    "category": "freeTier",
    "alias": "google-tts",
    "color": "#4285F4",
    "icon": "record_voice_over",
    "authType": "none",
    "noAuth": true,
    "mediaPriority": 5,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "kilo-gateway",
    "name": "Kilo Gateway",
    "category": "freeTier",
    "alias": "kgw",
    "color": "#8B5CF6",
    "icon": "login",
    "website": "https://kilo.ai",
    "notice": {"apiKeyUrl":"https://kilo.ai/dashboard?tab=apiKeys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "kimchi",
    "name": "Kimchi",
    "category": "freeTier",
    "alias": "kimchi",
    "color": "#FF521D",
    "icon": "restaurant",
    "website": "https://kimchi.dev",
    "notice": {"signupUrl":"https://app.kimchi.dev"},
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "local-device",
    "name": "Local Device",
    "category": "freeTier",
    "alias": "local-device",
    "color": "#64748B",
    "icon": "speaker",
    "authType": "none",
    "noAuth": true,
    "mediaPriority": 5,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "nvidia",
    "name": "NVIDIA NIM",
    "category": "freeTier",
    "alias": "nvidia",
    "color": "#76B900",
    "icon": "developer_board",
    "website": "https://developer.nvidia.com/nim",
    "notice": {"text":"Free access for NVIDIA Developer Program members (prototyping & testing).","apiKeyUrl":"https://build.nvidia.com/settings/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 20,
    "serviceKinds": [
      "llm",
      "tts",
      "embedding"
    ]
  },
  {
    "id": "ollama",
    "name": "Ollama Cloud",
    "category": "freeTier",
    "alias": "ollama",
    "color": "#ffffffff",
    "icon": "cloud",
    "website": "https://ollama.com",
    "notice": {"text":"Free tier: light usage, 1 cloud model at a time (limits reset every 5h & 7d). Pro $20/mo · Max $100/mo.","apiKeyUrl":"https://ollama.com/settings/keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 30,
    "serviceKinds": [
      "llm",
      "webFetch"
    ]
  },
  {
    "id": "openrouter",
    "name": "OpenRouter",
    "category": "freeTier",
    "alias": "openrouter",
    "color": "#F97316",
    "icon": "router",
    "website": "https://openrouter.ai",
    "notice": {"text":"Free tier: 27+ free models, no credit card needed, 200 req/day. After  0 credit: 1,000 req/day.","apiKeyUrl":"https://openrouter.ai/settings/keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 10,
    "systemoneConfig": {
      "baseUrl": "https://openrouter.ai/api/v1/systemone",
      "format": "systemone"
    },
    "serviceKinds": [
      "llm",
      "embedding",
      "tts",
      "video",
      "systemone"
    ],
    "modelsFetcher": {"url":"https://openrouter.ai/api/v1/models","type":"openrouter-free"}
  },
  {
    "id": "poolside",
    "name": "Poolside",
    "category": "freeTier",
    "alias": "ps",
    "color": "#0EA5E9",
    "icon": "water_drop",
    "website": "https://poolside.ai",
    "notice": {"apiKeyUrl":"https://platform.poolside.ai/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "searxng",
    "name": "SearXNG",
    "category": "freeTier",
    "alias": "searxng",
    "color": "#3B82F6",
    "icon": "saved_search",
    "website": "https://docs.searxng.org",
    "authType": "none",
    "noAuth": true,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "tortoise",
    "name": "Tortoise TTS",
    "category": "freeTier",
    "alias": "tortoise",
    "color": "#7C3AED",
    "icon": "record_voice_over",
    "website": "https://github.com/neonbjb/tortoise-tts",
    "authType": "none",
    "noAuth": true,
    "hidden": true,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "vertex",
    "name": "Vertex AI",
    "category": "freeTier",
    "alias": "vx",
    "color": "#4285F4",
    "icon": "cloud",
    "website": "https://cloud.google.com/vertex-ai",
    "notice": {"text":"New Google Cloud accounts get $300 free credits. Requires GCP project + Service Account with Vertex AI API enabled.","apiKeyUrl":"https://console.cloud.google.com/iam-admin/serviceaccounts"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "alicode",
    "name": "Alibaba",
    "category": "apikey",
    "alias": "alicode",
    "color": "#FF6A00",
    "icon": "cloud",
    "website": "https://bailian.console.aliyun.com",
    "notice": {"apiKeyUrl":"https://bailian.console.aliyun.com/?apiKey=1"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "alicode-intl",
    "name": "Alibaba Coding",
    "category": "apikey",
    "alias": "alicode-intl",
    "color": "#FF6A00",
    "icon": "cloud",
    "website": "https://www.alibabacloud.com/product/coding",
    "notice": {"apiKeyUrl":"https://www.alibabacloud.com/product/coding"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "alims-intl",
    "name": "Alibaba Studio",
    "category": "apikey",
    "alias": "alims-intl",
    "color": "#FF6A00",
    "icon": "cloud",
    "website": "https://modelstudio.console.alibabacloud.com",
    "notice": {"apiKeyUrl":"https://modelstudio.console.alibabacloud.com/?apiKey=1"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "alitp-intl",
    "name": "Alibaba Token Plan",
    "category": "apikey",
    "alias": "alitp-intl",
    "color": "#FF6A00",
    "icon": "cloud",
    "website": "https://www.alibabacloud.com/campaign/ai-landing-page-token",
    "notice": {"apiKeyUrl":"https://modelstudio.console.alibabacloud.com/?apiKey=1"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "anthropic",
    "name": "Anthropic",
    "category": "apikey",
    "alias": "anthropic",
    "color": "#D97757",
    "icon": "smart_toy",
    "website": "https://console.anthropic.com",
    "notice": {"apiKeyUrl":"https://console.anthropic.com/settings/keys"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "anthropic-version",
    "name": "anthropic-version",
    "category": "apikey",
    "alias": "anthropic-version",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "assemblyai",
    "name": "AssemblyAI",
    "category": "apikey",
    "alias": "aai",
    "color": "#0062FF",
    "icon": "record_voice_over",
    "website": "https://assemblyai.com",
    "notice": {"apiKeyUrl":"https://www.assemblyai.com/app/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 30,
    "serviceKinds": [
      "stt"
    ]
  },
  {
    "id": "aws-polly",
    "name": "AWS Polly",
    "category": "apikey",
    "alias": "polly",
    "color": "#FF9900",
    "icon": "record_voice_over",
    "website": "https://aws.amazon.com/polly/",
    "notice": {"text":"Use AWS Secret Access Key as API key; set providerSpecificData.accessKeyId and optional region.","apiKeyUrl":"https://console.aws.amazon.com/iam/home#/security_credentials"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "bedrock",
    "name": "AWS Bedrock",
    "category": "apikey",
    "alias": "br",
    "color": "#FF9900",
    "icon": "cloud",
    "website": "https://aws.amazon.com/bedrock/",
    "notice": {"text":"Two ways to authenticate, both entered in the AWS Bedrock Credentials section below. SSO / profile (recommended): fill in Profile and Region, leave the API key empty, then run `aws sso login --profile <name>` — credentials refresh automatically. Static keys: put the AWS secret access key in the API Key field and the key id in Access Key ID, adding Session Token if they are temporary (ASIA…) keys. A profile, if set, takes precedence over static keys.","apiKeyUrl":"https://console.aws.amazon.com/iam/home#/security_credentials"},
    "authType": "apikey",
    "noAuth": false,
    "credentialForm": "aws",
    "apiKeyOptionalWith": "profile",
    "passthroughModels": true
  },
  {
    "id": "bedrock-xai",
    "name": "AWS Bedrock (xAI)",
    "category": "apikey",
    "alias": "brx",
    "color": "#FF9900",
    "icon": "cloud",
    "website": "https://aws.amazon.com/bedrock/",
    "notice": {"text":"xAI Grok models hosted on AWS Bedrock. Authenticate exactly like the AWS Bedrock provider: fill in Profile and Region and leave the API key empty, then run `aws sso login --profile <name>`; or use static keys with the AWS secret access key as the API key. Note Grok is a reasoning model and spends output budget thinking before it answers — a small max_tokens returns finish_reason \"length\" with empty content, so allow a few thousand tokens.","apiKeyUrl":"https://console.aws.amazon.com/iam/home#/security_credentials"},
    "authType": "apikey",
    "noAuth": false,
    "credentialForm": "aws",
    "apiKeyOptionalWith": "profile",
    "passthroughModels": true
  },
  {
    "id": "azure",
    "name": "Azure OpenAI",
    "category": "apikey",
    "alias": "azure",
    "color": "#0078D4",
    "icon": "cloud",
    "website": "https://azure.microsoft.com/en-us/products/ai-services/openai-service",
    "notice": {"apiKeyUrl":"https://portal.azure.com/#view/Microsoft_Azure_ProjectOxford/CognitiveServicesHub/~/OpenAI"},
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "image",
      "embedding"
    ]
  },
  {
    "id": "baidu",
    "name": "Baidu Qianfan",
    "category": "apikey",
    "alias": "qianfan",
    "color": "#2932E1",
    "icon": "search",
    "website": "https://cloud.baidu.com/product/qianfan.html",
    "notice": {"apiKeyUrl":"https://console.bce.baidu.com/qianfan/ais/console/applicationConsole/application"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "black-forest-labs",
    "name": "Black Forest Labs",
    "category": "apikey",
    "alias": "bfl",
    "color": "#111827",
    "icon": "image",
    "website": "https://blackforestlabs.ai",
    "notice": {"apiKeyUrl":"https://api.bfl.ai"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "blackbox",
    "name": "Blackbox AI",
    "category": "apikey",
    "alias": "bb",
    "color": "#5B5FEF",
    "icon": "smart_toy",
    "website": "https://blackbox.ai",
    "notice": {"apiKeyUrl":"https://www.blackbox.ai/api-management"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "bluesminds",
    "name": "BluesMinds",
    "category": "apikey",
    "alias": "bm",
    "color": "#2563EB",
    "icon": "psychology",
    "website": "https://bluesminds.com",
    "notice": {"apiKeyUrl":"https://bluesminds.com"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "brave-search",
    "name": "Brave Search",
    "category": "apikey",
    "alias": "brave",
    "color": "#FB542B",
    "icon": "travel_explore",
    "website": "https://brave.com/search/api",
    "notice": {"apiKeyUrl":"https://api-dashboard.search.brave.com/app/keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "cartesia",
    "name": "Cartesia",
    "category": "apikey",
    "alias": "cartesia",
    "color": "#FF4F8B",
    "icon": "spatial_audio",
    "website": "https://cartesia.ai",
    "notice": {"apiKeyUrl":"https://play.cartesia.ai/keys"},
    "authType": "apikey",
    "noAuth": false,
    "hidden": true,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "cerebras",
    "name": "Cerebras",
    "category": "apikey",
    "alias": "cerebras",
    "color": "#FF4F00",
    "icon": "memory",
    "website": "https://www.cerebras.ai",
    "notice": {"apiKeyUrl":"https://cloud.cerebras.ai/platform"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "chutes",
    "name": "Chutes AI",
    "category": "apikey",
    "alias": "ch",
    "color": "#ffffffff",
    "icon": "water_drop",
    "website": "https://chutes.ai",
    "notice": {"apiKeyUrl":"https://chutes.ai/app/api"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "cohere",
    "name": "Cohere",
    "category": "apikey",
    "alias": "cohere",
    "color": "#39594D",
    "icon": "hub",
    "website": "https://cohere.com",
    "notice": {"apiKeyUrl":"https://dashboard.cohere.com/api-keys"},
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding"
    ]
  },
  {
    "id": "comfyui",
    "name": "ComfyUI",
    "category": "apikey",
    "alias": "comfyui",
    "color": "#4CAF50",
    "icon": "account_tree",
    "website": "https://github.com/comfyanonymous/ComfyUI",
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "commandcode",
    "name": "Command Code",
    "category": "apikey",
    "alias": "cmc",
    "color": "#000000",
    "icon": "smart_toy",
    "website": "https://commandcode.ai",
    "notice": {"text":"Use your CommandCode CLI API key (starts with user_...) from ~/.commandcode/auth.json or commandcode.ai/studio.","apiKeyUrl":"https://commandcode.ai/studio"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "deepgram",
    "name": "Deepgram",
    "category": "apikey",
    "alias": "dg",
    "color": "#13EF93",
    "icon": "mic",
    "website": "https://deepgram.com",
    "notice": {"text":"$200 free credit on signup (no card required). Aura-1: $0.015/1k chars, Aura-2: $0.030/1k chars (Pay-As-You-Go).","apiKeyUrl":"https://console.deepgram.com/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 20,
    "serviceKinds": [
      "stt"
    ]
  },
  {
    "id": "deepseek",
    "name": "DeepSeek",
    "category": "apikey",
    "alias": "ds",
    "color": "#4D6BFE",
    "icon": "bolt",
    "website": "https://deepseek.com",
    "notice": {"apiKeyUrl":"https://platform.deepseek.com/api_keys"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "devin-cli",
    "name": "Devin CLI",
    "category": "free",
    "alias": "devin-cli",
    "color": "#888888",
    "icon": "dns",
    "website": "https://devin.ai",
    "notice": {"text":"Install: `curl -fsSL https://cli.devin.ai/install.sh | bash` (macOS: `brew install --cask devin-cli`, Windows PowerShell: `irm https://static.devin.ai/cli/setup.ps1 | iex`). Then run `devin auth login`. No API key needed.","signupUrl":"https://cli.devin.ai"},
    "authType": "none",
    "noAuth": true,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "elevenlabs",
    "name": "ElevenLabs",
    "category": "apikey",
    "alias": "el",
    "color": "#6C47FF",
    "icon": "record_voice_over",
    "website": "https://elevenlabs.io",
    "notice": {"apiKeyUrl":"https://elevenlabs.io/app/settings/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "exa",
    "name": "Exa",
    "category": "apikey",
    "alias": "exa",
    "color": "#2563EB",
    "icon": "manage_search",
    "website": "https://exa.ai",
    "notice": {"apiKeyUrl":"https://dashboard.exa.ai/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch",
      "webFetch",
      "web"
    ]
  },
  {
    "id": "fal-ai",
    "name": "Fal.ai",
    "category": "apikey",
    "alias": "fal",
    "color": "#2563EB",
    "icon": "image",
    "website": "https://fal.ai",
    "notice": {"apiKeyUrl":"https://fal.ai/dashboard/keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "featherless",
    "name": "Featherless",
    "category": "apikey",
    "alias": "fl",
    "color": "#111827",
    "icon": "flutter_dash",
    "website": "https://featherless.ai",
    "notice": {"apiKeyUrl":"https://featherless.ai/account/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding"
    ]
  },
  {
    "id": "firecrawl",
    "name": "Firecrawl",
    "category": "apikey",
    "alias": "firecrawl",
    "color": "#F59E0B",
    "icon": "local_fire_department",
    "website": "https://firecrawl.dev",
    "notice": {"apiKeyUrl":"https://www.firecrawl.dev/app/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webFetch"
    ]
  },
  {
    "id": "fireworks",
    "name": "Fireworks AI",
    "category": "apikey",
    "alias": "fireworks",
    "color": "#7B2EF2",
    "icon": "local_fire_department",
    "website": "https://fireworks.ai",
    "notice": {"apiKeyUrl":"https://fireworks.ai/account/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding"
    ]
  },
  {
    "id": "fish-audio",
    "name": "Fish Audio",
    "category": "apikey",
    "alias": "fish",
    "color": "#1E9BF0",
    "icon": "record_voice_over",
    "website": "https://fish.audio",
    "notice": {"apiKeyUrl":"https://fish.audio/app/api-keys/"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "glm-cn",
    "name": "GLM (China)",
    "category": "apikey",
    "alias": "glm-cn",
    "color": "#DC2626",
    "icon": "code",
    "website": "https://open.bigmodel.cn",
    "notice": {"apiKeyUrl":"https://open.bigmodel.cn/usercenter/apikeys"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "glm",
    "name": "GLM Coding",
    "category": "apikey",
    "alias": "glm",
    "color": "#2563EB",
    "icon": "code",
    "website": "https://open.bigmodel.cn",
    "notice": {"apiKeyUrl":"https://open.bigmodel.cn/usercenter/apikeys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 140,
    "serviceKinds": [
      "llm",
      "webSearch"
    ]
  },
  {
    "id": "google-pse",
    "name": "Google PSE",
    "category": "apikey",
    "alias": "gpse",
    "color": "#4285F4",
    "icon": "search",
    "website": "https://programmablesearchengine.google.com",
    "notice": {"apiKeyUrl":"https://programmablesearchengine.google.com/controlpanel/create"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "groq",
    "name": "Groq",
    "category": "apikey",
    "alias": "groq",
    "color": "#F55036",
    "icon": "speed",
    "website": "https://groq.com",
    "notice": {"apiKeyUrl":"https://console.groq.com/keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 60,
    "serviceKinds": [
      "llm",
      "stt"
    ]
  },
  {
    "id": "huggingface",
    "name": "HuggingFace",
    "category": "apikey",
    "alias": "hf",
    "color": "#FFD21E",
    "icon": "face",
    "website": "https://huggingface.co",
    "notice": {"apiKeyUrl":"https://huggingface.co/settings/tokens"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 70,
    "serviceKinds": [
      "image",
      "stt"
    ]
  },
  {
    "id": "hyperbolic",
    "name": "Hyperbolic",
    "category": "apikey",
    "alias": "hyp",
    "color": "#00D4FF",
    "icon": "bolt",
    "website": "https://hyperbolic.xyz",
    "notice": {"apiKeyUrl":"https://app.hyperbolic.xyz/settings"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "inworld",
    "name": "Inworld TTS",
    "category": "apikey",
    "alias": "inworld",
    "color": "#FF6B6B",
    "icon": "record_voice_over",
    "website": "https://inworld.ai",
    "notice": {"text":"Free tier: 40 minutes/month TTS. Paid: TTS-1.5 Mini $0.01/min ($15/1M chars), TTS-1.5 Max $0.025/min ($30/1M chars). 270+ voices, 15 languages.","apiKeyUrl":"https://platform.inworld.ai/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "jina-ai",
    "name": "Jina AI",
    "category": "apikey",
    "alias": "jina",
    "color": "#2563EB",
    "icon": "blur_on",
    "website": "https://jina.ai",
    "notice": {"text":"10M free tokens on signup (non-commercial), no credit card required.","apiKeyUrl":"https://jina.ai/?sui=apikey"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "embedding"
    ]
  },
  {
    "id": "jina-reader",
    "name": "Jina Reader",
    "category": "apikey",
    "alias": "jina-reader",
    "color": "#000000",
    "icon": "menu_book",
    "website": "https://jina.ai/reader",
    "notice": {"apiKeyUrl":"https://jina.ai/?sui=apikey"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webFetch"
    ]
  },
  {
    "id": "kimi-coding",
    "name": "kimi-coding",
    "category": "oauth",
    "alias": "kimi-coding",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "linkup",
    "name": "Linkup",
    "category": "apikey",
    "alias": "linkup",
    "color": "#0EA5E9",
    "icon": "link",
    "website": "https://linkup.so",
    "notice": {"apiKeyUrl":"https://app.linkup.so/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "llm7",
    "name": "LLM7",
    "category": "apikey",
    "alias": "llm7",
    "color": "#7C3AED",
    "icon": "pool",
    "website": "https://llm7.io",
    "notice": {"apiKeyUrl":"https://llm7.io"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "minimax-code",
    "name": "MiniMax Code",
    "category": "oauth",
    "alias": "mmc",
    "color": "#FF4D4F",
    "icon": "smart_toy",
    "website": "https://agent.minimax.cn",
    "notice": {"text": "MiniMax Code coding subscription. Sign in with your MiniMax account; ~/.minimax is never touched, so this login does not sign the CLI out."},
    "authType": "oauth",
    "noAuth": false,
    "authModes": ["oauth"],
    "priority": 80,
    "usage": true,
    "serviceKinds": [
      "llm"
    ],
    "modelsFetcher": {"url":"https://agent.minimax.cn/mavis/api/v1/models?region=cn&buildEnv=prod","type":"minimax-code"}
  },
  {
    "id": "minimax-code-global",
    "name": "MiniMax Code (Global)",
    "category": "oauth",
    "alias": "mmg",
    "color": "#FF4D4F",
    "icon": "smart_toy",
    "website": "https://agent.minimax.io",
    "notice": {"text": "The international MiniMax Code site. Sign-ins are per site: an account on agent.minimax.cn says nothing about agent.minimax.io."},
    "authType": "oauth",
    "noAuth": false,
    "authModes": ["oauth"],
    "priority": 80,
    "usage": true,
    "serviceKinds": [
      "llm"
    ],
    "modelsFetcher": {"url":"https://agent.minimax.io/mavis/api/v1/models?region=en&buildEnv=prod","type":"minimax-code"}
  },
  {
    "id": "minimax-cn",
    "name": "Minimax (China)",
    "category": "apikey",
    "alias": "minimax-cn",
    "color": "#DC2626",
    "icon": "memory",
    "website": "https://www.minimaxi.com",
    "notice": {"apiKeyUrl":"https://platform.minimaxi.com/user-center/basic-information/interface-key"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 190,
    "serviceKinds": [
      "llm",
      "tts"
    ]
  },
  {
    "id": "minimax",
    "name": "Minimax Coding",
    "category": "apikey",
    "alias": "minimax",
    "color": "#7C3AED",
    "icon": "memory",
    "website": "https://www.minimaxi.com",
    "notice": {"apiKeyUrl":"https://platform.minimaxi.com/user-center/basic-information/interface-key"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 90,
    "serviceKinds": [
      "llm",
      "image",
      "tts",
      "webSearch"
    ]
  },
  {
    "id": "mmf",
    "name": "MMF",
    "category": "apikey",
    "alias": "mmf",
    "color": "#6366F1",
    "icon": "hub",
    "noAuth": false,
    "priority": 200,
    "hidden": true,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "mistral",
    "name": "Mistral",
    "category": "apikey",
    "alias": "mistral",
    "color": "#FF7000",
    "icon": "air",
    "website": "https://mistral.ai",
    "notice": {"apiKeyUrl":"https://console.mistral.ai/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding"
    ]
  },
  {
    "id": "morph",
    "name": "Morph",
    "category": "apikey",
    "alias": "morph",
    "color": "#14B8A6",
    "icon": "change_history",
    "website": "https://morphllm.com",
    "notice": {"apiKeyUrl":"https://morphllm.com"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "nanobanana",
    "name": "NanoBanana API",
    "category": "apikey",
    "alias": "nb",
    "color": "#FFD700",
    "icon": "extension",
    "website": "https://nanobananaapi.ai",
    "notice": {"text":"3rd-party proxy for Google Nano Banana (Gemini 2.5/3 Flash Image). For official, use Gemini provider.","apiKeyUrl":"https://nanobananaapi.ai/dashboard"},
    "noAuth": false,
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "nebius",
    "name": "Nebius AI",
    "category": "apikey",
    "alias": "nebius",
    "color": "#6C5CE7",
    "icon": "cloud",
    "website": "https://nebius.com",
    "notice": {"apiKeyUrl":"https://studio.nebius.com/settings/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding"
    ]
  },
  {
    "id": "ollama-local",
    "name": "Ollama Local",
    "category": "apikey",
    "alias": "ollama-local",
    "color": "#ffffffff",
    "icon": "cloud",
    "website": "https://ollama.com",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "ollama-search",
    "name": "Ollama Search",
    "category": "apikey",
    "alias": "ollama-search",
    "color": "#ffffff",
    "icon": "cloud",
    "website": "https://ollama.com",
    "notice": {"text":"Web search via Ollama Cloud subscription. Reuses the API key from the Ollama (chat) provider.","apiKeyUrl":"https://ollama.com/settings/keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "openai",
    "name": "OpenAI",
    "category": "apikey",
    "alias": "openai",
    "color": "#10A37F",
    "icon": "auto_awesome",
    "website": "https://platform.openai.com",
    "notice": {"apiKeyUrl":"https://platform.openai.com/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 30,
    "serviceKinds": [
      "llm",
      "image",
      "tts",
      "stt",
      "embedding",
      "webSearch"
    ]
  },
  {
    "id": "openai-intent",
    "name": "openai-intent",
    "category": "apikey",
    "alias": "openai-intent",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "opencode-go",
    "name": "OpenCode Go",
    "category": "apikey",
    "alias": "ocg",
    "color": "#E87040",
    "icon": "terminal",
    "website": "https://opencode.ai/auth",
    "notice": {"text":"OpenCode Go subscription: $5/mo (then 10/mo). Access to Kimi, GLM, Qwen, MiMo, MiniMax models.","apiKeyUrl":"https://opencode.ai/auth"},
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "image",
      "video",
      "embedding"
    ]
  },
  {
    "id": "opencode-zen",
    "name": "OpenCode Zen",
    "category": "apikey",
    "alias": "ocz",
    "color": "#E87040",
    "icon": "terminal",
    "website": "https://opencode.ai/auth",
    "notice": {"text":"OpenCode Zen PAYG: pay-as-you-go, key from https://opencode.ai/auth. Same models as Zen: paid + free tiers on the fast lane.","apiKeyUrl":"https://opencode.ai/auth"},
    "noAuth": false,
    "priority": 205,
    "passthroughModels": true,
    "usage": true,
    "systemoneConfig": {
      "baseUrl": "https://opencode.ai/zen/v1/systemone",
      "format": "systemone"
    },
    "serviceKinds": [
      "llm",
      "systemone"
    ],
    "modelsFetcher": {"url":"https://opencode.ai/zen/v1/models","type":"opencode-free"}
  },
  {
    "id": "originator",
    "name": "originator",
    "category": "apikey",
    "alias": "originator",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "perplexity",
    "name": "Perplexity",
    "category": "apikey",
    "alias": "pplx",
    "color": "#20808D",
    "icon": "search",
    "website": "https://www.perplexity.ai",
    "notice": {"apiKeyUrl":"https://www.perplexity.ai/settings/api"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 180,
    "serviceKinds": [
      "llm",
      "webSearch"
    ]
  },
  {
    "id": "perplexity-agent",
    "name": "Perplexity Agent",
    "category": "apikey",
    "alias": "pa",
    "color": "#20808D",
    "icon": "travel_explore",
    "website": "https://www.perplexity.ai",
    "notice": {"text":"Perplexity Agent API exposes GPT, Claude, Gemini, Grok, GLM, Kimi, and Sonar models through one OpenAI-compatible Responses API.","apiKeyUrl":"https://www.perplexity.ai/settings/api"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 181,
    "serviceKinds": [
      "llm",
      "webSearch"
    ]
  },
  {
    "id": "playht",
    "name": "PlayHT",
    "category": "apikey",
    "alias": "playht",
    "color": "#00B4D8",
    "icon": "play_circle",
    "website": "https://play.ht",
    "notice": {"apiKeyUrl":"https://play.ht/studio/api-access"},
    "authType": "apikey",
    "noAuth": false,
    "hidden": true,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "recraft",
    "name": "Recraft",
    "category": "apikey",
    "alias": "recraft",
    "color": "#EC4899",
    "icon": "image",
    "website": "https://recraft.ai",
    "notice": {"apiKeyUrl":"https://www.recraft.ai/profile/api"},
    "authType": "apikey",
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "runwayml",
    "name": "Runway ML",
    "category": "apikey",
    "alias": "runway",
    "color": "#000000",
    "icon": "movie",
    "website": "https://runwayml.com",
    "notice": {"apiKeyUrl":"https://dev.runwayml.com"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "image",
      "video"
    ]
  },
  {
    "id": "sambanova",
    "name": "SambaNova",
    "category": "apikey",
    "alias": "samba",
    "color": "#F97316",
    "icon": "memory",
    "website": "https://sambanova.ai",
    "notice": {"apiKeyUrl":"https://cloud.sambanova.ai/apis"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "sdwebui",
    "name": "SD WebUI",
    "category": "apikey",
    "alias": "sdwebui",
    "color": "#FF7043",
    "icon": "brush",
    "website": "https://github.com/AUTOMATIC1111/stable-diffusion-webui",
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "searchapi",
    "name": "SearchAPI",
    "category": "apikey",
    "alias": "searchapi",
    "color": "#0EA5A4",
    "icon": "search",
    "website": "https://www.searchapi.io",
    "notice": {"apiKeyUrl":"https://www.searchapi.io/dashboard"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "serper",
    "name": "Serper",
    "category": "apikey",
    "alias": "serper",
    "color": "#4F46E5",
    "icon": "search",
    "website": "https://serper.dev",
    "notice": {"apiKeyUrl":"https://serper.dev/api-key"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "siliconflow",
    "name": "SiliconFlow",
    "category": "apikey",
    "alias": "siliconflow",
    "color": "#5B6EF5",
    "icon": "cloud_queue",
    "website": "https://cloud.siliconflow.com",
    "notice": {"apiKeyUrl":"https://cloud.siliconflow.com/account/ak"},
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "image"
    ]
  },
  {
    "id": "stability-ai",
    "name": "Stability AI",
    "category": "apikey",
    "alias": "stability",
    "color": "#8B5CF6",
    "icon": "image",
    "website": "https://stability.ai",
    "notice": {"apiKeyUrl":"https://platform.stability.ai/account/keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "tavily",
    "name": "Tavily",
    "category": "apikey",
    "alias": "tavily",
    "color": "#5B21B6",
    "icon": "search",
    "website": "https://tavily.com",
    "notice": {"apiKeyUrl":"https://app.tavily.com/home"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch",
      "webFetch",
      "web"
    ]
  },
  {
    "id": "tencent",
    "name": "Tencent Hunyuan",
    "category": "apikey",
    "alias": "hunyuan",
    "color": "#0052D9",
    "icon": "cloud",
    "website": "https://cloud.tencent.com/product/hunyuan",
    "notice": {"apiKeyUrl":"https://console.cloud.tencent.com/hunyuan/api-key"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "tinyfish",
    "name": "TinyFish",
    "category": "apikey",
    "alias": "tinyfish",
    "color": "#FF6700",
    "icon": "dns",
    "website": "https://www.tinyfish.ai/",
    "notice": {"apiKeyUrl":"https://agent.tinyfish.ai/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch",
      "webFetch"
    ]
  },
  {
    "id": "together",
    "name": "Together AI",
    "category": "apikey",
    "alias": "together",
    "color": "#0F6FFF",
    "icon": "group_work",
    "website": "https://www.together.ai",
    "notice": {"apiKeyUrl":"https://api.together.xyz/settings/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding"
    ]
  },
  {
    "id": "tokenrouter",
    "name": "TokenRouter",
    "category": "apikey",
    "alias": "tokenrouter",
    "color": "#0EA5E9",
    "icon": "hub",
    "website": "https://www.tokenrouter.com",
    "notice": {"text":"OpenAI-compatible gateway. 300+ models (OpenAI, Claude, Gemini, Qwen, DeepSeek, Kimi, GLM, dsb).","apiKeyUrl":"https://www.tokenrouter.com"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "embedding",
      "image"
    ]
  },
  {
    "id": "topaz",
    "name": "Topaz",
    "category": "apikey",
    "alias": "topaz",
    "color": "#059669",
    "icon": "image",
    "website": "https://topazlabs.com",
    "notice": {"apiKeyUrl":"https://topazlabs.com/account"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "image"
    ]
  },
  {
    "id": "trae",
    "name": "trae",
    "category": "oauth",
    "alias": "trae",
    "color": "#888888",
    "icon": "dns",
    "website": "https://www.trae.ai",
    "notice": {"signupUrl":"https://www.trae.ai"},
    "authType": "oauth",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "user-agent",
    "name": "user-agent",
    "category": "apikey",
    "alias": "user-agent",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "v1m",
    "name": "v1m (System One)",
    "category": "apikey",
    "alias": "v1m",
    "color": "#6366F1",
    "icon": "psychology",
    "website": "https://v1m.ir",
    "notice": {"text":"v1m System One calibrated decision engine. Fast probabilistic evaluations over state and questions.", "apiKeyUrl":"https://v1m.ir"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 45,
    "systemoneConfig": {
      "baseUrl": "https://v1m.ir/v1/systemone",
      "format": "systemone"
    },
    "serviceKinds": [
      "systemone"
    ]
  },
  {
    "id": "venice",
    "name": "Venice AI",
    "category": "apikey",
    "alias": "venice",
    "color": "#DC2626",
    "icon": "shield",
    "website": "https://venice.ai",
    "notice": {"text":"OpenAI-compatible. Private inference + uncensored models (Venice Uncensored, GLM, Qwen, DeepSeek, Llama).","apiKeyUrl":"https://venice.ai/settings/api"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "image",
      "embedding"
    ]
  },
  {
    "id": "vercel-ai-gateway",
    "name": "Vercel AI Gateway",
    "category": "apikey",
    "alias": "vercel",
    "color": "#111827",
    "icon": "deployed_code",
    "website": "https://vercel.com/ai-gateway",
    "notice": {"text":"Unified OpenAI-compatible endpoint from Vercel. Use your AI Gateway API key, then pick models with provider/model IDs like anthropic/claude-sonnet-4.6 or openai/gpt-5.4.","apiKeyUrl":"https://vercel.com/dashboard/~/ai-gateway"},
    "noAuth": false,
    "priority": 160,
    "serviceKinds": [
      "llm",
      "embedding",
      "image",
      "webSearch"
    ]
  },
  {
    "id": "vertex-partner",
    "name": "Vertex Partner",
    "category": "apikey",
    "alias": "vxp",
    "color": "#34A853",
    "icon": "cloud",
    "website": "https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/use-partner-models",
    "notice": {"apiKeyUrl":"https://console.cloud.google.com/iam-admin/serviceaccounts"},
    "noAuth": false,
    "serviceKinds": [
      "llm",
      "video"
    ]
  },
  {
    "id": "volcengine-ark",
    "name": "Volcengine Ark",
    "category": "apikey",
    "alias": "ark",
    "color": "#1677FF",
    "icon": "cloud",
    "website": "https://ark.cn-beijing.volces.com",
    "notice": {"apiKeyUrl":"https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey"},
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "voyage-ai",
    "name": "Voyage AI",
    "category": "apikey",
    "alias": "voyage",
    "color": "#0EA5E9",
    "icon": "data_array",
    "website": "https://www.voyageai.com",
    "notice": {"apiKeyUrl":"https://dash.voyageai.com/api-keys"},
    "authType": "apikey",
    "noAuth": false,
    "serviceKinds": [
      "embedding"
    ]
  },
  {
    "id": "windsurf",
    "name": "windsurf",
    "category": "oauth",
    "alias": "windsurf",
    "color": "#888888",
    "icon": "dns",
    "website": "https://windsurf.com",
    "notice": {"signupUrl":"https://windsurf.com"},
    "authType": "oauth",
    "noAuth": false,
    "authModes": ["oauth", "apikey"],
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "x-codebuddy-request",
    "name": "x-codebuddy-request",
    "category": "apikey",
    "alias": "x-codebuddy-request",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "x-github-api-version",
    "name": "x-github-api-version",
    "category": "apikey",
    "alias": "x-github-api-version",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "x-requested-with",
    "name": "x-requested-with",
    "category": "apikey",
    "alias": "x-requested-with",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "x-vscode-user-agent-library-version",
    "name": "x-vscode-user-agent-library-version",
    "category": "apikey",
    "alias": "x-vscode-user-agent-library-version",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "xiaomi-tokenplan",
    "name": "Xiaomi MiMo (Token Plan)",
    "category": "apikey",
    "alias": "xmtp",
    "color": "#FF6700",
    "icon": "smart_toy",
    "website": "https://mimo.xiaomi.com",
    "notice": {"text":"Xiaomi MiMo Token Plan subscription (API key starts with tp-). Token Plan keys are cluster-specific — select the region matching your subscription.","apiKeyUrl":"https://mimo.xiaomi.com"},
    "noAuth": false,
    "regions": [
      {
        "id": "sgp",
        "label": "Singapore (新加坡)"
      },
      {
        "id": "cn",
        "label": "China (中国大陆)"
      },
      {
        "id": "ams",
        "label": "Amsterdam (阿姆斯特丹)"
      }
    ],
    "defaultRegion": "sgp",
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "xquik",
    "name": "Xquik",
    "category": "apikey",
    "alias": "xquik",
    "color": "#5C3327",
    "icon": "tag",
    "website": "https://docs.xquik.com/api-reference/x/search-tweets",
    "notice": {"text":"Searches public X posts. Billing uses 1 Xquik credit per returned post.","apiKeyUrl":"https://xquik.com"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "youcom",
    "name": "You.com Search",
    "category": "apikey",
    "alias": "youcom",
    "color": "#7C3AED",
    "icon": "search",
    "website": "https://you.com",
    "notice": {"apiKeyUrl":"https://api.you.com"},
    "authType": "apikey",
    "noAuth": false,
    "priority": 999,
    "serviceKinds": [
      "webSearch"
    ]
  },
  {
    "id": "zai-search",
    "name": "zai-search",
    "category": "apikey",
    "alias": "zai-search",
    "color": "#888888",
    "icon": "dns",
    "noAuth": false,
    "serviceKinds": [

    ]
  },
  {
    "id": "grok-web",
    "name": "Grok Web (Subscription)",
    "category": "webCookie",
    "alias": "gw",
    "color": "#1DA1F2",
    "icon": "auto_awesome",
    "website": "https://grok.com",
    "authType": "cookie",
    "authHint": "Paste your sso= cookie value from grok.com",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "perplexity-web",
    "name": "Perplexity Web (Pro/Max)",
    "category": "webCookie",
    "alias": "pw",
    "color": "#20808D",
    "icon": "search",
    "website": "https://www.perplexity.ai",
    "authType": "cookie",
    "authHint": "Paste your __Secure-next-auth.session-token cookie value from perplexity.ai",
    "noAuth": false,
    "serviceKinds": [
      "llm"
    ]
  },
  {
    "id": "selfhosted-tts",
    "name": "Self-Hosted TTS",
    "category": "apikey",
    "alias": "selfhosted-tts",
    "color": "#10B981",
    "icon": "volume_up",
    "website": "https://github.com/remsky/Kokoro-FastAPI",
    "authType": "none",
    "noAuth": true,
    "priority": 50,
    "serviceKinds": [
      "tts"
    ]
  },
  {
    "id": "selfhosted-stt",
    "name": "Self-Hosted STT",
    "category": "apikey",
    "alias": "selfhosted-stt",
    "color": "#3B82F6",
    "icon": "mic",
    "website": "https://github.com/ggml-org/whisper.cpp",
    "authType": "apikey",
    "noAuth": false,
    "priority": 50,
    "serviceKinds": [
      "stt"
    ]
  },
  {
    "id": "selfhosted-embedding",
    "name": "Self-Hosted Embedding",
    "category": "apikey",
    "alias": "selfhosted-embedding",
    "color": "#8B5CF6",
    "icon": "layers",
    "website": "https://github.com/ggml-org/llama.cpp",
    "authType": "apikey",
    "noAuth": true,
    "serviceKinds": [
      "embedding"
    ]
  }
]

export const PROVIDER_CATALOG_MAP = new Map(PROVIDER_CATALOG.map((p) => [p.id, p]))

export function isChatProvider(p: ProviderCatalogItem): boolean {
  return (p.serviceKinds ?? ['llm']).includes('llm')
}

export function getProvidersByKind(kind: string): ProviderCatalogItem[] {
  return PROVIDER_CATALOG
    .filter((p) => {
      const kinds = p.serviceKinds ?? ['llm']
      if (!kinds.includes(kind)) return false
      if (p.hidden) return false
      if (p.hiddenKinds?.includes(kind)) return false
      return true
    })
    .sort((a, b) => ((a.priority ?? a.mediaPriority ?? 999) - (b.priority ?? b.mediaPriority ?? 999)))
}

export const MEDIA_PROVIDER_KINDS = [
  'embedding',
  'image',
  'tts',
  'stt',
  'video',
  'webSearch',
  'webFetch',
  'web',
] as const
