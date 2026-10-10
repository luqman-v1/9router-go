// Auto-generated from upstream open-sse provider registry
export interface ProviderModel {
  id: string
  name?: string
  upstreamModelId?: string
  kind?: string
  type?: string
  thinking?: boolean
  imageGen?: boolean
  capabilities?: string[]
  isFree?: boolean
  contextLength?: number
  [key: string]: unknown
}

export const PROVIDER_ID_TO_ALIAS: Record<string, string> = {
  "agnes": "agnes",
  "bai": "bai",
  "atria": "atria",
  "dahl": "dahl",
  "tokenharbor": "tokenharbor",
  "alicode": "alicode",
  "alicode-intl": "alicode-intl",
  "alims-intl": "alims-intl",
  "alitp-intl": "alitp-intl",
  "anthropic": "anthropic",
  "antigravity": "ag",
  "api-airforce": "af",
  "assemblyai": "aai",
  "aws-polly": "aws-polly",
  "azure": "azure",
  "bedrock": "br",
  "bedrock-xai": "brx",
  "baidu": "qianfan",
  "bazaarlink": "bzl",
  "black-forest-labs": "bfl",
  "blackbox": "bb",
  "bluesminds": "bm",
  "byteplus": "bpm",
  "cartesia": "cartesia",
  "cerebras": "cerebras",
  "chutes": "chutes",
  "claude": "cc",
  "cline": "cl",
  "clinepass": "clinepass",
  "cloudflare-ai": "cf",
  "codebuddy-cn": "cbcn",
  "codebuddy-intl": "cbai",
  "codex": "cx",
  "cohere": "cohere",
  "comfyui": "comfyui",
  "commandcode": "cmc",
  "coqui": "coqui",
  "cursor": "cu",
  "deepgram": "dg",
  "deepseek": "ds",
  "devin-cli": "devin-cli",
  "elevenlabs": "elevenlabs",
  "fal-ai": "fal",
  "featherless": "fl",
  "fireworks": "fireworks",
  "fish-audio": "fish-audio",
  "freebuff": "fb",
  "gemini": "gemini",
  "gemini-cli": "gc",
  "github": "gh",
  "gitlab": "gitlab",
  "glm": "glm",
  "glm-cn": "glm-cn",
  "grok-cli": "gcli",
  "grok-web": "gw",
  "groq": "groq",
  "huggingface": "hf",
  "hyperbolic": "hyp",
  "iflow": "if",
  "inworld": "inworld",
  "jina-ai": "jina-ai",
  "kilo-gateway": "kgw",
  "kilocode": "kc",
  "kimchi": "kimchi",
  "kimi": "kimi",
  "kiro": "kr",
  "llm7": "llm7",
  "mimo-free": "mmf",
  "minimax": "minimax",
  "minimax-cn": "minimax-cn",
  "minimax-code": "mmc",
  "minimax-code-global": "mmg",
  "mistral": "mistral",
  "mmf": "mmf",
  "muse": "muse",
  "tinyfish": "tinyfish",
  "morph": "morph",
  "nanobanana": "nb",
  "nebius": "nebius",
  "nvidia": "nvidia",
  "ollama": "ollama",
  "ollama-local": "ollama-local",
  "openai": "openai",
  "opencode": "oc",
  "opencode-go": "ocg",
  "opencode-zen": "ocz",
  "openrouter": "openrouter",
  "perplexity": "pplx",
  "perplexity-agent": "pa",
  "perplexity-web": "pw",
  "playht": "playht",
  "poolside": "ps",
  "qoder": "qd",
  "qoder-cn": "qdcn",
  "recraft": "recraft",
  "runwayml": "runway",
  "sambanova": "samba",
  "sdwebui": "sdwebui",
  "selfhosted-embedding": "selfhosted-embedding",
  "selfhosted-stt": "selfhosted-stt",
  "selfhosted-tts": "selfhosted-tts",
  "siliconflow": "siliconflow",
  "stability-ai": "stability",
  "tencent": "hunyuan",
  "together": "together",
  "tokenrouter": "tokenrouter",
  "tortoise": "tortoise",
  "trae": "trae",
  "venice": "venice",
  "vercel-ai-gateway": "vercel-ai-gateway",
  "vertex": "vx",
  "vertex-partner": "vxp",
  "volcengine-ark": "ark",
  "v1m": "v1m",
  "voyage-ai": "voyage",
  "windsurf": "windsurf",
  "xai": "xai",
  "xiaomi-mimo": "mimo",
  "xiaomi-tokenplan": "xmtp",
  "zed": "zd",
};

const FREEBUFF_MODELS: ProviderModel[] = [
  {
    "id": "z-ai/glm-5.3-flash",
    "name": "GLM 5.3 Flash",
    "capabilities": ["reasoning"]
  },
  {
    "id": "deepseek/deepseek-v4-flash",
    "name": "DeepSeek V4.1 Flash",
    "capabilities": ["reasoning"]
  },
  {
    "id": "openai/gpt-5.6-luna",
    "name": "GPT-5.6 Luna",
    "capabilities": ["reasoning"]
  },
  {
    "id": "mimo/mimo-v2.5",
    "name": "MiMo 2.5",
    "capabilities": ["reasoning"]
  },
  {
    "id": "upstage/solar-pro4",
    "name": "Solar Pro 4",
    "capabilities": ["reasoning"]
  },
  {
    "id": "meta/muse-spark-1.2-contributor",
    "name": "Muse Spark 1.2",
    "capabilities": ["reasoning"]
  },
  {
    "id": "anthropic/claude-fable-5",
    "name": "Claude Fable 5 (limited offer)",
    "capabilities": ["reasoning"]
  }
];

export const BUILTIN_MODELS_BY_PROVIDER: Record<string, ProviderModel[]> = {
  "fb": FREEBUFF_MODELS,
  "br": [
    {
      "id": "us.anthropic.claude-opus-5",
      "name": "Claude Opus 5"
    },
    {
      "id": "us.anthropic.claude-sonnet-5",
      "name": "Claude Sonnet 5"
    },
    {
      "id": "us.anthropic.claude-fable-5",
      "name": "Claude Fable 5"
    },
    {
      "id": "us.anthropic.claude-opus-4-8",
      "name": "Claude Opus 4.8"
    },
    {
      "id": "us.anthropic.claude-opus-4-7",
      "name": "Claude Opus 4.7"
    },
    {
      "id": "us.anthropic.claude-sonnet-4-6",
      "name": "Claude Sonnet 4.6"
    },
    {
      "id": "us.anthropic.claude-opus-4-5-20251101-v1:0",
      "name": "Claude Opus 4.5"
    },
    {
      "id": "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
      "name": "Claude Sonnet 4.5"
    },
    {
      "id": "us.anthropic.claude-opus-4-1-20250805-v1:0",
      "name": "Claude Opus 4.1"
    },
    {
      "id": "us.anthropic.claude-sonnet-4-20250514-v1:0",
      "name": "Claude Sonnet 4"
    },
    {
      "id": "us.anthropic.claude-3-haiku-20240307-v1:0",
      "name": "Claude 3 Haiku"
    }
  ],
  "brx": [
    {
      "id": "us.xai.grok-4.6",
      "name": "Grok 4.6"
    },
    {
      "id": "global.xai.grok-4.6",
      "name": "Grok 4.6 (global)"
    }
  ],
  "freebuff": FREEBUFF_MODELS,
  "alicode-intl": [
    {
      "id": "qwen3.5-plus",
      "name": "Qwen3.5 Plus"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "qwen3-coder-next",
      "name": "Qwen3 Coder Next"
    },
    {
      "id": "qwen3-coder-plus",
      "name": "Qwen3 Coder Plus"
    },
    {
      "id": "glm-4.7",
      "name": "GLM 4.7"
    }
  ],
  "alicode": [
    {
      "id": "qwen3.5-plus",
      "name": "Qwen3.5 Plus"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "qwen3-max-2026-01-23",
      "name": "Qwen3 Max"
    },
    {
      "id": "qwen3-coder-next",
      "name": "Qwen3 Coder Next"
    },
    {
      "id": "qwen3-coder-plus",
      "name": "Qwen3 Coder Plus"
    },
    {
      "id": "glm-4.7",
      "name": "GLM 4.7"
    }
  ],
  "anthropic": [
    {
      "id": "claude-sonnet-4-20250514",
      "name": "Claude Sonnet 4"
    },
    {
      "id": "claude-opus-4-20250514",
      "name": "Claude Opus 4"
    },
    {
      "id": "claude-3-5-sonnet-20241022",
      "name": "Claude 3.5 Sonnet"
    }
  ],
  "ag": [
    {
      "id": "gemini-2.5-flash",
      "name": "Gemini 2.5 Flash"
    },
    {
      "id": "gemini-2.5-flash-lite",
      "name": "Gemini 2.5 Flash Lite"
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro"
    },
    {
      "id": "gemini-2.5-flash-thinking",
      "name": "Gemini 2.5 Flash (Thinking)"
    },
    {
      "id": "gemini-3.1-pro-high",
      "name": "Gemini 3.1 Pro (High)"
    },
    {
      "id": "gemini-3.1-flash-lite",
      "name": "Gemini 3.1 Flash Lite"
    },
    {
      "id": "gemini-3.5-flash-lite",
      "name": "Gemini 3.5 Flash Lite"
    },
    {
      "id": "gemini-3.8-flash-high",
      "name": "Gemini 3.8 Flash (High)",
      "upstreamModelId": "gemini-3.8-flash-high(high)"
    },
    {
      "id": "gemini-3.8-flash-medium",
      "name": "Gemini 3.8 Flash (Medium)",
      "upstreamModelId": "gemini-3.8-flash-medium(medium)"
    },
    {
      "id": "gemini-3.8-flash-low",
      "name": "Gemini 3.8 Flash (Low)",
      "upstreamModelId": "gemini-3.8-flash-low(low)"
    },
    {
      "id": "gemini-3.8-flash",
      "name": "Gemini 3.8 Flash",
      "upstreamModelId": "gemini-3.8-flash-medium(medium)"
    },
    {
      "id": "gemini-3.7-flash-high",
      "name": "Gemini 3.7 Flash (High)",
      "upstreamModelId": "gemini-3.7-flash-tiered(high)"
    },
    {
      "id": "gemini-3.7-flash-medium",
      "name": "Gemini 3.7 Flash (Medium)",
      "upstreamModelId": "gemini-3.7-flash-tiered(medium)"
    },
    {
      "id": "gemini-3.7-flash-low",
      "name": "Gemini 3.7 Flash (Low)",
      "upstreamModelId": "gemini-3.7-flash-tiered(low)"
    },
    {
      "id": "gemini-3.6-flash-high",
      "name": "Gemini 3.6 Flash (High)",
      "upstreamModelId": "gemini-3.6-flash-tiered(high)"
    },
    {
      "id": "gemini-3.6-flash-medium",
      "name": "Gemini 3.6 Flash (Medium)",
      "upstreamModelId": "gemini-3.6-flash-tiered(medium)"
    },
    {
      "id": "gemini-3.6-flash-low",
      "name": "Gemini 3.6 Flash (Low)",
      "upstreamModelId": "gemini-3.6-flash-tiered(low)"
    },
    {
      "id": "gemini-3.1-pro-high",
      "name": "Gemini 3.1 Pro (High)"
    },
    {
      "id": "gemini-pro-agent",
      "name": "Gemini 3.1 Pro (High)"
    },
    {
      "id": "gemini-3.1-pro-low",
      "name": "Gemini 3.1 Pro (Low)"
    },
    {
      "id": "claude-sonnet-5-5-high",
      "name": "Claude Sonnet 5.5 (High)",
      "upstreamModelId": "claude-sonnet-5-5-high(high)"
    },
    {
      "id": "claude-sonnet-5-5-medium",
      "name": "Claude Sonnet 5.5 (Medium)",
      "upstreamModelId": "claude-sonnet-5-5-medium(medium)"
    },
    {
      "id": "claude-sonnet-5-5-low",
      "name": "Claude Sonnet 5.5 (Low)",
      "upstreamModelId": "claude-sonnet-5-5-low(low)"
    },
    {
      "id": "claude-sonnet-5-5",
      "name": "Claude Sonnet 5.5 (Thinking)",
      "upstreamModelId": "claude-sonnet-5-5-high(high)"
    },
    {
      "id": "claude-opus-5-5-high",
      "name": "Claude Opus 5.5 (High)",
      "upstreamModelId": "claude-opus-5-5-high(high)"
    },
    {
      "id": "claude-opus-5-5-medium",
      "name": "Claude Opus 5.5 (Medium)",
      "upstreamModelId": "claude-opus-5-5-medium(medium)"
    },
    {
      "id": "claude-opus-5-5-low",
      "name": "Claude Opus 5.5 (Low)",
      "upstreamModelId": "claude-opus-5-5-low(low)"
    },
    {
      "id": "claude-opus-5-5",
      "name": "Claude Opus 5.5 (Thinking)",
      "upstreamModelId": "claude-opus-5-5-high(high)"
    },
    {
      "id": "claude-sonnet-4-6",
      "name": "Claude Sonnet 4.6 (Thinking)"
    },
    {
      "id": "claude-opus-4-6-thinking",
      "name": "Claude Opus 4.6 (Thinking)"
    },
    {
      "id": "gpt-oss-120b-medium",
      "name": "GPT-OSS 120B (Medium)"
    },
    {
      "id": "gemini-3.1-flash-image",
      "name": "Gemini 3.1 Flash (Image)",
      "kind": "image",
      "imageGen": true,
      "capabilities": [
        "textToImage"
      ]
    }
  ],
  "assemblyai": [
    {
      "id": "universal-3-pro",
      "name": "Universal 3 Pro",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "universal-2",
      "name": "Universal 2",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "best",
      "name": "Best (Nano + Universal)",
      "kind": "stt"
    },
    {
      "id": "nano",
      "name": "Nano (Fast)",
      "kind": "stt"
    }
  ],
  "aws-polly": [
    {
      "id": "standard",
      "name": "Standard",
      "kind": "tts"
    },
    {
      "id": "neural",
      "name": "Neural",
      "kind": "tts"
    },
    {
      "id": "long-form",
      "name": "Long-Form",
      "kind": "tts"
    },
    {
      "id": "generative",
      "name": "Generative",
      "kind": "tts"
    }
  ],
  "black-forest-labs": [
    {
      "id": "flux-pro-1.1",
      "name": "FLUX Pro 1.1",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "flux-pro-1.1-ultra",
      "name": "FLUX Pro 1.1 Ultra",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "flux-pro",
      "name": "FLUX Pro",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "flux-dev",
      "name": "FLUX Dev",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "flux-kontext-pro",
      "name": "FLUX Kontext Pro (Edit)",
      "params": [
        "size"
      ],
      "capabilities": [
        "edit"
      ],
      "kind": "image"
    },
    {
      "id": "flux-kontext-max",
      "name": "FLUX Kontext Max (Edit)",
      "params": [
        "size"
      ],
      "capabilities": [
        "edit"
      ],
      "kind": "image"
    }
  ],
  "blackbox": [
    {
      "id": "claude-fable-5",
      "name": "Claude Fable 5",
      "upstreamModelId": "blackboxai/anthropic/claude-fable-5"
    },
    {
      "id": "claude-opus-4.8",
      "name": "Claude Opus 4.8",
      "upstreamModelId": "blackboxai/anthropic/claude-opus-4.8"
    },
    {
      "id": "claude-sonnet-4.6",
      "name": "Claude Sonnet 4.6",
      "upstreamModelId": "blackboxai/anthropic/claude-sonnet-4.6"
    },
    {
      "id": "gpt-5.5",
      "name": "GPT-5.5",
      "upstreamModelId": "blackboxai/openai/gpt-5.5"
    },
    {
      "id": "gpt-5.4-pro",
      "name": "GPT-5.4 Pro",
      "upstreamModelId": "blackboxai/openai/gpt-5.4-pro"
    },
    {
      "id": "gpt-5.4",
      "name": "GPT-5.4",
      "upstreamModelId": "blackboxai/openai/gpt-5.4"
    },
    {
      "id": "gpt-5.3-codex",
      "name": "GPT-5.3 Codex",
      "upstreamModelId": "blackboxai/openai/gpt-5.3-codex"
    },
    {
      "id": "gpt-5.4-nano",
      "name": "GPT-5.4 Nano",
      "upstreamModelId": "blackboxai/openai/gpt-5.4-nano"
    },
    {
      "id": "deepseek-v4-flash",
      "name": "DeepSeek V4 Flash",
      "upstreamModelId": "blackboxai/deepseek/deepseek-v4-flash"
    },
    {
      "id": "grok-4.3",
      "name": "Grok 4.3",
      "upstreamModelId": "blackboxai/x-ai/grok-4.3"
    }
  ],
  "byteplus": [
    {
      "id": "seed-2-0-pro-260328",
      "name": "Seed 2.0 Pro"
    },
    {
      "id": "seed-2-0-code-preview-260328",
      "name": "Seed 2.0 Code Preview"
    },
    {
      "id": "seed-2-0-mini-260215",
      "name": "Seed 2.0 Mini"
    },
    {
      "id": "seed-2-0-lite-260228",
      "name": "Seed 2.0 Lite"
    },
    {
      "id": "kimi-k2-thinking-251104",
      "name": "Kimi K2 Thinking"
    },
    {
      "id": "glm-4-7-251222",
      "name": "GLM 4.7"
    },
    {
      "id": "gpt-oss-120b-250805",
      "name": "GPT-OSS-120B"
    }
  ],
  "cartesia": [
    {
      "id": "sonic-2",
      "name": "Sonic 2",
      "kind": "tts"
    },
    {
      "id": "sonic-3",
      "name": "Sonic 3",
      "kind": "tts"
    }
  ],
  "cerebras": [
    {
      "id": "gpt-oss-120b",
      "name": "GPT OSS 120B"
    },
    {
      "id": "zai-glm-4.7",
      "name": "ZAI GLM 4.7"
    },
    {
      "id": "llama-3.3-70b",
      "name": "Llama 3.3 70B"
    },
    {
      "id": "llama-4-scout-17b-16e-instruct",
      "name": "Llama 4 Scout"
    },
    {
      "id": "qwen-3-235b-a22b-instruct-2507",
      "name": "Qwen3 235B A22B"
    },
    {
      "id": "qwen-3-32b",
      "name": "Qwen3 32B"
    }
  ],
  "cc": [
    {
      "id": "claude-opus-5-5",
      "name": "Claude Opus 5.5"
    },
    {
      "id": "claude-opus-5",
      "name": "Claude Opus 5"
    },
    {
      "id": "claude-fable-5-1",
      "name": "Claude Fable 5.1"
    },
    {
      "id": "claude-fable-5",
      "name": "Claude Fable 5"
    },
    {
      "id": "claude-sonnet-5",
      "name": "Claude Sonnet 5"
    },
    {
      "id": "claude-haiku-4-5-20251001",
      "name": "Claude 4.5 Haiku"
    }
  ],
  "cl": [
    {
      "id": "anthropic/claude-opus-4.7",
      "name": "Claude Opus 4.7"
    },
    {
      "id": "anthropic/claude-sonnet-4.6",
      "name": "Claude Sonnet 4.6"
    },
    {
      "id": "anthropic/claude-opus-4.6",
      "name": "Claude Opus 4.6"
    },
    {
      "id": "openai/gpt-5.3-codex",
      "name": "GPT-5.3 Codex"
    },
    {
      "id": "openai/gpt-5.4",
      "name": "GPT-5.4"
    },
    {
      "id": "google/gemini-3.1-pro-preview",
      "name": "Gemini 3.1 Pro Preview"
    },
    {
      "id": "google/gemini-3.1-flash-lite-preview",
      "name": "Gemini 3.1 Flash Lite Preview"
    },
    {
      "id": "kwaipilot/kat-coder-pro",
      "name": "KAT Coder Pro"
    }
  ],
  "clinepass": [
    {
      "id": "cline-pass/glm-5.2",
      "name": "GLM-5.2 (ClinePass)"
    },
    {
      "id": "cline-pass/kimi-k2.7-code",
      "name": "Kimi K2.7 Code (ClinePass)"
    },
    {
      "id": "cline-pass/kimi-k2.6",
      "name": "Kimi K2.6 (ClinePass)"
    },
    {
      "id": "cline-pass/deepseek-v4-pro",
      "name": "DeepSeek V4 Pro (ClinePass)"
    },
    {
      "id": "cline-pass/deepseek-v4-flash",
      "name": "DeepSeek V4 Flash (ClinePass)"
    },
    {
      "id": "cline-pass/mimo-v2.5",
      "name": "MiMo-V2.5 (ClinePass)"
    },
    {
      "id": "cline-pass/mimo-v2.5-pro",
      "name": "MiMo-V2.5-Pro (ClinePass)"
    },
    {
      "id": "cline-pass/minimax-m3",
      "name": "MiniMax M3 (ClinePass)"
    },
    {
      "id": "cline-pass/qwen3.7-max",
      "name": "Qwen3.7 Max (ClinePass)"
    },
    {
      "id": "cline-pass/qwen3.7-plus",
      "name": "Qwen3.7 Plus (ClinePass)"
    }
  ],
  "cloudflare-ai": [
    {
      "id": "@cf/cloudflare/clef-flash",
      "name": "Clef Flash",
      "kind": "systemone"
    },
    {
      "id": "@cf/meta/llama-3.2-1b-instruct",
      "name": "Llama 3.2 1B Instruct"
    },
    {
      "id": "@cf/meta/llama-3.2-3b-instruct",
      "name": "Llama 3.2 3B Instruct"
    },
    {
      "id": "@cf/meta/llama-3.1-8b-instruct-fp8-fast",
      "name": "Llama 3.1 8B Instruct FP8 Fast"
    },
    {
      "id": "@cf/meta/llama-3.1-8b-instruct-awq",
      "name": "Llama 3.1 8B Instruct AWQ"
    },
    {
      "id": "@cf/mistralai/mistral-small-3.1-24b-instruct",
      "name": "Mistral Small 3.1 24B Instruct"
    },
    {
      "id": "@cf/meta/llama-3.1-70b-instruct-fp8-fast",
      "name": "Llama 3.1 70B Instruct FP8 Fast"
    },
    {
      "id": "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
      "name": "Llama 3.3 70B Instruct FP8 Fast"
    },
    {
      "id": "@cf/deepseek-ai/deepseek-r1-distill-qwen-32b",
      "name": "DeepSeek R1 Distill Qwen 32B"
    },
    {
      "id": "@cf/moonshotai/kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "@cf/moonshotai/kimi-k2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "@cf/zai-org/glm-4.7-flash",
      "name": "GLM 4.7 Flash"
    },
    {
      "id": "@cf/qwen/qwq-32b",
      "name": "QwQ 32B"
    },
    {
      "id": "@cf/qwen/qwen2.5-coder-32b-instruct",
      "name": "Qwen 2.5 Coder 32B Instruct"
    },
    {
      "id": "@cf/black-forest-labs/flux-2-klein-9b",
      "name": "FLUX.2 Klein 9B",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/black-forest-labs/flux-2-klein-4b",
      "name": "FLUX.2 Klein 4B",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/black-forest-labs/flux-2-dev",
      "name": "FLUX.2 Dev",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/leonardo/lucid-origin",
      "name": "Lucid Origin",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/leonardo/phoenix-1.0",
      "name": "Phoenix 1.0",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/black-forest-labs/flux-1-schnell",
      "name": "FLUX.1 Schnell",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/bytedance/stable-diffusion-xl-lightning",
      "name": "SDXL Lightning",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/lykon/dreamshaper-8-lcm",
      "name": "DreamShaper 8 LCM",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/runwayml/stable-diffusion-v1-5-img2img",
      "name": "Stable Diffusion v1.5 Img2Img",
      "params": [
        "size"
      ],
      "capabilities": [
        "edit"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/runwayml/stable-diffusion-v1-5-inpainting",
      "name": "Stable Diffusion v1.5 Inpainting",
      "params": [
        "size"
      ],
      "capabilities": [
        "edit",
        "mask"
      ],
      "kind": "image"
    },
    {
      "id": "@cf/stabilityai/stable-diffusion-xl-base-1.0",
      "name": "SDXL Base 1.0",
      "params": [
        "size"
      ],
      "kind": "image"
    }
  ],
  "cbcn": [
    {
      "id": "glm-5.2",
      "name": "GLM-5.2"
    },
    {
      "id": "glm-5.1",
      "name": "GLM-5.1"
    },
    {
      "id": "glm-5v-turbo",
      "name": "GLM-5v-Turbo"
    },
    {
      "id": "minimax-m3",
      "name": "MiniMax-M3"
    },
    {
      "id": "kimi-k2.7",
      "name": "Kimi-K2.7-Code"
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi-K2.6"
    },
    {
      "id": "hy3",
      "name": "Hy3"
    },
    {
      "id": "hy4-preview",
      "name": "Hy4-Preview"
    },
    {
      "id": "glm-5.3",
      "name": "GLM-5.3"
    },
    {
      "id": "glm-5.3-flash",
      "name": "GLM-5.3-Flash"
    },
    {
      "id": "kimi-k3-1",
      "name": "Kimi-K3"
    },
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek-V4-Pro"
    },
    {
      "id": "deepseek-v4.1-flash",
      "name": "DeepSeek-V4.1-Flash"
    }
  ],
  "cx": [
    {
      "id": "gpt-6.1-sol",
      "name": "GPT 6.1 Sol",
      "upstreamModelId": "gpt-6.1-sol"
    },
    {
      "id": "gpt-6-astra",
      "name": "GPT 6.0 Astra"
    },
    {
      "id": "gpt-6-astra[1m]",
      "name": "GPT 6.0 Astra (extended context)",
      "upstreamModelId": "gpt-6-astra"
    },
    {
      "id": "gpt-6-sol",
      "name": "GPT 6.0 Sol"
    },
    {
      "id": "gpt-6-sol[1m]",
      "name": "GPT 6.0 Sol (extended context)",
      "upstreamModelId": "gpt-6-sol"
    },
    {
      "id": "gpt-6-luna",
      "name": "GPT 6.0 Luna"
    },
    {
      "id": "gpt-6-luna[1m]",
      "name": "GPT 6.0 Luna (extended context)",
      "upstreamModelId": "gpt-6-luna"
    },
    {
      "id": "gpt-5.6-sol",
      "name": "GPT 5.6 Sol"
    },
    {
      "id": "gpt-5.6-sol[1m]",
      "name": "GPT 5.6 Sol (extended context)",
      "upstreamModelId": "gpt-5.6-sol"
    },
    {
      "id": "gpt-5.6-sol-review",
      "name": "GPT 5.6 Sol Review",
      "upstreamModelId": "gpt-5.6-sol"
    },
    {
      "id": "gpt-5.6-terra",
      "name": "GPT 5.6 Terra"
    },
    {
      "id": "gpt-5.6-terra[1m]",
      "name": "GPT 5.6 Terra (extended context)",
      "upstreamModelId": "gpt-5.6-terra"
    },
    {
      "id": "gpt-5.6-terra-review",
      "name": "GPT 5.6 Terra Review",
      "upstreamModelId": "gpt-5.6-terra"
    },
    {
      "id": "gpt-5.6-luna",
      "name": "GPT 5.6 Luna"
    },
    {
      "id": "gpt-5.6-luna[1m]",
      "name": "GPT 5.6 Luna (extended context)",
      "upstreamModelId": "gpt-5.6-luna"
    },
    {
      "id": "gpt-5.6-luna-review",
      "name": "GPT 5.6 Luna Review",
      "upstreamModelId": "gpt-5.6-luna"
    },
    {
      "id": "gpt-5.5",
      "name": "GPT 5.5"
    },
    {
      "id": "gpt-5.5-review",
      "name": "GPT 5.5 Review",
      "upstreamModelId": "gpt-5.5"
    },
    {
      "id": "gpt-daybreak-blue-latest",
      "name": "GPT Daybreak Blue"
    },
    {
      "id": "gpt-reserve",
      "name": "GPT Reserve"
    },
    {
      "id": "codex-auto-review",
      "name": "Codex Auto Review",
      "upstreamModelId": "codex-auto-review"
    },
    {
      "id": "gpt-image-2.5",
      "name": "GPT Image 2.5"
    },
    {
      "id": "gpt-image-2.5-flare",
      "name": "GPT Image 2.5 Flare"
    },
    {
      "id": "gpt-image-2.5-sunburst",
      "name": "GPT Image 2.5 Sunburst"
    },
    {
      "id": "gpt-image-2",
      "name": "GPT Image 2"
    },
    {
      "id": "gpt-image-1.5",
      "name": "GPT Image 1.5"
    },
    {
      "id": "gpt-5.6-sol-image",
      "name": "GPT 5.6 Sol Image"
    },
    {
      "id": "gpt-5.6-terra-image",
      "name": "GPT 5.6 Terra Image"
    },
    {
      "id": "gpt-5.6-luna-image",
      "name": "GPT 5.6 Luna Image"
    },
    {
      "id": "gpt-5.5-image",
      "name": "GPT 5.5 Image"
    },
    {
      "id": "gpt-5.3-image",
      "name": "GPT 5.3 Image"
    }
  ],
  "cohere": [
    {
      "id": "command-r-plus-08-2024",
      "name": "Command R+ (Aug 2024)"
    },
    {
      "id": "command-r-08-2024",
      "name": "Command R (Aug 2024)"
    },
    {
      "id": "command-a-03-2025",
      "name": "Command A (Mar 2025)"
    }
  ],
  "comfyui": [
    {
      "id": "flux-dev",
      "name": "FLUX Dev",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "sdxl",
      "name": "SDXL",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    }
  ],
  "coqui": [
    {
      "id": "tts_models/en/ljspeech/tacotron2-DDC",
      "name": "Tacotron2 DDC (LJSpeech)",
      "kind": "tts"
    }
  ],
  "tokenharbor": [
    {
      "id": "claude-opus-5.5",
      "name": "Claude Opus 5.5"
    },
    {
      "id": "claude-sonnet-5",
      "name": "Claude Sonnet 5"
    },
    {
      "id": "gpt-6-astra",
      "name": "GPT-6 Astra"
    },
    {
      "id": "gpt-6-sol",
      "name": "GPT-6 Sol"
    },
    {
      "id": "grok-4.7",
      "name": "Grok 4.7"
    },
    {
      "id": "mimo-v2.6-flash:free",
      "name": "MiMo V2.6 Flash (Free)"
    },
    {
      "id": "mimo-v2.5:free",
      "name": "MiMo V2.5 (Free)"
    },
    {
      "id": "qwen3.8-flash:free",
      "name": "Qwen 3.8 Flash (Free)"
    },
    {
      "id": "deepseek-v4.1-flash:free",
      "name": "DeepSeek V4.1 Flash (Free)"
    },
    {
      "id": "deepseek-v4-flash:free",
      "name": "DeepSeek V4 Flash (Free)"
    }
  ],
  "dahl": [
    {
      "id": "zai-org/GLM-5.3-Flash",
      "name": "GLM-5.3 Flash"
    },
    {
      "id": "deepseek-ai/DeepSeek-V4-Flash-0731",
      "name": "DeepSeek V4 Flash 0731"
    },
    {
      "id": "MiniMaxAI/MiniMax-M2.7",
      "name": "MiniMax M2.7"
    }
  ],
  "agnes": [
    {
      "id": "agnes-2.5-flash",
      "name": "Agnes 2.5 Flash"
    },
    {
      "id": "agnes-2.5-pro",
      "name": "Agnes 2.5 Pro"
    },
    {
      "id": "agnes-2.5-pro-beta",
      "name": "Agnes 2.5 Pro Beta"
    },
    {
      "id": "agnes-3.0-flash",
      "name": "Agnes 3.0 Flash"
    },
    {
      "id": "agnes-3.0-pro",
      "name": "Agnes 3.0 Pro"
    }
  ],
  "atria": [
    {
      "id": "Atria-Dawn-Preview",
      "name": "Atria Dawn Preview"
    }
  ],
  "commandcode": [
    {
      "id": "deepseek/deepseek-v4-pro",
      "name": "DeepSeek V4 Pro"
    },
    {
      "id": "deepseek/deepseek-v4-flash",
      "name": "DeepSeek V4 Flash"
    },
    {
      "id": "moonshotai/Kimi-K2.7-Code",
      "name": "Kimi K2.7 Code"
    },
    {
      "id": "moonshotai/Kimi-K2.7-Code-Highspeed",
      "name": "Kimi K2.7 Code HighSpeed"
    },
    {
      "id": "moonshotai/Kimi-K2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "moonshotai/Kimi-K2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "zai-org/GLM-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "zai-org/GLM-5.2-Fast",
      "name": "GLM 5.2 Fast"
    },
    {
      "id": "zai-org/GLM-5.1",
      "name": "GLM 5.1"
    },
    {
      "id": "zai-org/GLM-5",
      "name": "GLM 5"
    },
    {
      "id": "MiniMaxAI/MiniMax-M3",
      "name": "MiniMax M3"
    },
    {
      "id": "MiniMaxAI/MiniMax-M2.7",
      "name": "MiniMax M2.7"
    },
    {
      "id": "MiniMaxAI/MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "xiaomi/mimo-v2.5-pro",
      "name": "MiMo V2.5 Pro"
    },
    {
      "id": "xiaomi/mimo-v2.5",
      "name": "MiMo V2.5"
    },
    {
      "id": "Qwen/Qwen3.6-Max-Preview",
      "name": "Qwen 3.6 Max Preview"
    },
    {
      "id": "Qwen/Qwen3.6-Plus",
      "name": "Qwen 3.6 Plus"
    },
    {
      "id": "Qwen/Qwen3.7-Max",
      "name": "Qwen 3.7 Max"
    },
    {
      "id": "Qwen/Qwen3.7-Plus",
      "name": "Qwen 3.7 Plus"
    },
    {
      "id": "stepfun/Step-3.7-Flash",
      "name": "Step 3.7 Flash"
    },
    {
      "id": "stepfun/Step-3.5-Flash",
      "name": "Step 3.5 Flash"
    },
    {
      "id": "nvidia/nemotron-3-ultra-550b-a55b",
      "name": "Nemotron 3 Ultra"
    },
  ],
  "cu": [
    {
      "id": "default",
      "name": "Auto (Server Picks)"
    },
    {
      "id": "claude-4.5-opus-high-thinking",
      "name": "Claude 4.5 Opus High Thinking"
    },
    {
      "id": "claude-4.5-opus-high",
      "name": "Claude 4.5 Opus High"
    },
    {
      "id": "claude-4.5-sonnet-thinking",
      "name": "Claude 4.5 Sonnet Thinking"
    },
    {
      "id": "claude-4.5-sonnet",
      "name": "Claude 4.5 Sonnet"
    },
    {
      "id": "claude-4.5-haiku",
      "name": "Claude 4.5 Haiku"
    },
    {
      "id": "claude-4.5-opus",
      "name": "Claude 4.5 Opus"
    },
    {
      "id": "gpt-5.2-codex",
      "name": "GPT 5.2 Codex"
    },
    {
      "id": "claude-4.6-opus-max",
      "name": "Claude 4.6 Opus Max"
    },
    {
      "id": "claude-4.6-sonnet-medium-thinking",
      "name": "Claude 4.6 Sonnet Medium Thinking"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "gemini-3-flash-preview",
      "name": "Gemini 3 Flash Preview"
    },
    {
      "id": "gpt-5.2",
      "name": "GPT 5.2"
    },
    {
      "id": "gpt-5.3-codex",
      "name": "GPT 5.3 Codex"
    }
  ],
  "deepgram": [
    {
      "id": "nova-3",
      "name": "Nova 3",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "nova-2",
      "name": "Nova 2",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "whisper-large",
      "name": "Whisper Large",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "nova",
      "name": "Nova",
      "kind": "stt"
    }
  ],
  "deepseek": [
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek V4 Pro"
    },
    {
      "id": "deepseek-v4-pro-max",
      "name": "DeepSeek V4 Pro Max",
      "upstreamModelId": "deepseek-v4-pro"
    },
    {
      "id": "deepseek-v4-pro-none",
      "name": "DeepSeek V4 Pro No Thinking",
      "upstreamModelId": "deepseek-v4-pro"
    },
    {
      "id": "deepseek-v4.1-flash",
      "name": "DeepSeek V4.1 Flash"
    },
    {
      "id": "deepseek-v4-flash",
      "name": "DeepSeek V4 Flash"
    },
    {
      "id": "deepseek-v4-flash-vision-exp",
      "name": "DeepSeek V4 Flash Vision (Exp)"
    },
    {
      "id": "deepseek-chat",
      "name": "DeepSeek V3.2 Chat"
    },
    {
      "id": "deepseek-reasoner",
      "name": "DeepSeek V3.2 Reasoner"
    }
  ],
  "devin-cli": [
    {
      "id": "swe-1.6-fast",
      "name": "SWE 1.6 Fast"
    },
    {
      "id": "swe-1.6",
      "name": "SWE 1.6"
    },
    {
      "id": "swe-1.5-fast",
      "name": "SWE 1.5 Fast"
    },
    {
      "id": "swe-1.5",
      "name": "SWE 1.5"
    },
    {
      "id": "claude-opus-4.7-max",
      "name": "Claude Opus 4.7 Max"
    },
    {
      "id": "claude-opus-4.7-high",
      "name": "Claude Opus 4.7 High"
    },
    {
      "id": "claude-opus-4.7-medium",
      "name": "Claude Opus 4.7 Medium"
    },
    {
      "id": "claude-opus-4.7-low",
      "name": "Claude Opus 4.7 Low"
    },
    {
      "id": "claude-sonnet-4.6-thinking-1m",
      "name": "Claude Sonnet 4.6 Thinking 1M"
    },
    {
      "id": "claude-sonnet-4.6-thinking",
      "name": "Claude Sonnet 4.6 Thinking"
    },
    {
      "id": "claude-sonnet-4.6",
      "name": "Claude Sonnet 4.6"
    },
    {
      "id": "claude-opus-4.6-thinking",
      "name": "Claude Opus 4.6 Thinking"
    },
    {
      "id": "claude-opus-4.6",
      "name": "Claude Opus 4.6"
    },
    {
      "id": "claude-sonnet-4.5",
      "name": "Claude Sonnet 4.5"
    },
    {
      "id": "claude-haiku-4.5",
      "name": "Claude Haiku 4.5"
    },
    {
      "id": "gpt-5.5-xhigh",
      "name": "GPT 5.5 XHigh"
    },
    {
      "id": "gpt-5.5-high",
      "name": "GPT 5.5 High"
    },
    {
      "id": "gpt-5.5-medium",
      "name": "GPT 5.5 Medium"
    },
    {
      "id": "gpt-5.5-low",
      "name": "GPT 5.5 Low"
    },
    {
      "id": "gpt-5.4-high",
      "name": "GPT 5.4 High"
    },
    {
      "id": "gpt-5.4-medium",
      "name": "GPT 5.4 Medium"
    },
    {
      "id": "gpt-5.4-low",
      "name": "GPT 5.4 Low"
    },
    {
      "id": "gpt-5.3-codex-high",
      "name": "GPT 5.3 Codex High"
    },
    {
      "id": "gpt-5.3-codex-medium",
      "name": "GPT 5.3 Codex Medium"
    },
    {
      "id": "gpt-5.3-codex-low",
      "name": "GPT 5.3 Codex Low"
    },
    {
      "id": "gpt-5.2-high",
      "name": "GPT 5.2 High"
    },
    {
      "id": "gpt-5.2-medium",
      "name": "GPT 5.2 Medium"
    },
    {
      "id": "gpt-5.2-low",
      "name": "GPT 5.2 Low"
    },
    {
      "id": "gemini-3.1-pro-high",
      "name": "Gemini 3.1 Pro High"
    },
    {
      "id": "gemini-3.1-pro-low",
      "name": "Gemini 3.1 Pro Low"
    },
    {
      "id": "gemini-3.0-flash-high",
      "name": "Gemini 3.0 Flash High"
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro"
    },
    {
      "id": "deepseek-v4",
      "name": "DeepSeek V4"
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1"
    }
  ],
  "fal-ai": [
    {
      "id": "fal-ai/flux/schnell",
      "name": "FLUX Schnell",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "fal-ai/flux/dev",
      "name": "FLUX Dev",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "fal-ai/flux-pro/v1.1",
      "name": "FLUX Pro v1.1",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "fal-ai/flux-pro/v1.1-ultra",
      "name": "FLUX Pro v1.1 Ultra",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "fal-ai/recraft-v3",
      "name": "Recraft V3",
      "params": [
        "n",
        "size",
        "style"
      ],
      "kind": "image"
    },
    {
      "id": "fal-ai/ideogram/v2",
      "name": "Ideogram V2",
      "params": [
        "n",
        "size",
        "style"
      ],
      "kind": "image"
    },
    {
      "id": "fal-ai/stable-diffusion-v35-large",
      "name": "SD 3.5 Large",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    }
  ],
  "featherless": [
    {
      "id": "deepseek-ai/DeepSeek-V4-Pro",
      "name": "DeepSeek V4 Pro"
    },
    {
      "id": "deepseek-ai/DeepSeek-V4-Flash",
      "name": "DeepSeek V4 Flash"
    },
    {
      "id": "zai-org/GLM-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "zai-org/GLM-5.1",
      "name": "GLM 5.1"
    },
    {
      "id": "moonshotai/Kimi-K2.7-Code",
      "name": "Kimi K2.7 Code"
    },
    {
      "id": "moonshotai/Kimi-K2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "moonshotai/Kimi-K2.5",
      "name": "Kimi K2.5"
    }
  ],
  "fireworks": [
    {
      "id": "accounts/fireworks/models/deepseek-v3p1",
      "name": "DeepSeek V3.1"
    },
    {
      "id": "accounts/fireworks/models/llama-v3p3-70b-instruct",
      "name": "Llama 3.3 70B"
    },
    {
      "id": "accounts/fireworks/models/qwen3-235b-a22b",
      "name": "Qwen3 235B"
    },
    {
      "id": "nomic-ai/nomic-embed-text-v1.5",
      "name": "Nomic Embed Text v1.5",
      "kind": "embedding"
    }
  ],
  "fish-audio": [
    {
      "id": "s2.1-pro-free",
      "name": "S2.1 Pro Free",
      "kind": "tts"
    },
    {
      "id": "s2.1-pro",
      "name": "S2.1 Pro",
      "kind": "tts"
    },
    {
      "id": "s2-pro",
      "name": "S2 Pro",
      "kind": "tts"
    },
    {
      "id": "s1",
      "name": "S1",
      "kind": "tts"
    }
  ],
  "gc": [
    {
      "id": "gemini-3.1-pro-preview",
      "name": "Gemini 3.1 Pro Preview"
    },
    {
      "id": "gemini-3-pro-preview",
      "name": "Gemini 3 Pro Preview"
    },
    {
      "id": "gemini-3-flash-preview",
      "name": "Gemini 3 Flash Preview"
    },
    {
      "id": "gemini-3.1-flash-lite-preview",
      "name": "Gemini 3.1 Flash Lite Preview"
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro"
    },
    {
      "id": "gemini-2.5-flash",
      "name": "Gemini 2.5 Flash"
    },
    {
      "id": "gemini-2.5-flash-lite",
      "name": "Gemini 2.5 Flash Lite"
    }
  ],
  "gemini": [
    {
      "id": "gemini-3.8-flash",
      "name": "Gemini 3.8 Flash"
    },
    {
      "id": "gemini-3.7-flash",
      "name": "Gemini 3.7 Flash"
    },
    {
      "id": "gemini-3.6-flash",
      "name": "Gemini 3.6 Flash"
    },
    {
      "id": "gemini-3.5-flash-lite",
      "name": "Gemini 3.5 Flash Lite"
    },
    {
      "id": "gemini-3.1-pro-preview",
      "name": "Gemini 3.1 Pro Preview"
    },
    {
      "id": "gemini-3.1-flash-lite-preview",
      "name": "Gemini 3.1 Flash Lite Preview"
    },
    {
      "id": "gemini-3-flash-preview",
      "name": "Gemini 3 Flash Preview"
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro"
    },
    {
      "id": "gemini-2.5-flash",
      "name": "Gemini 2.5 Flash"
    },
    {
      "id": "gemini-2.5-flash-lite",
      "name": "Gemini 2.5 Flash Lite"
    },
    {
      "id": "gemma-4-31b-it",
      "name": "Gemma 4 31B IT"
    },
    {
      "id": "gemini-embedding-2-preview",
      "name": "Gemini Embedding 2 Preview",
      "kind": "embedding"
    },
    {
      "id": "gemini-embedding-001",
      "name": "Gemini Embedding 001",
      "kind": "embedding"
    },
    {
      "id": "text-embedding-005",
      "name": "Text Embedding 005",
      "kind": "embedding"
    },
    {
      "id": "text-embedding-004",
      "name": "Text Embedding 004 (Legacy)",
      "kind": "embedding"
    },
    {
      "id": "gemini-3.1-flash-image-preview",
      "name": "Gemini 3.1 Flash Image (Nano Banana 2)",
      "params": [],
      "kind": "image"
    },
    {
      "id": "gemini-3-pro-image-preview",
      "name": "Gemini 3 Pro Image (Nano Banana Pro)",
      "params": [],
      "kind": "image"
    },
    {
      "id": "gemini-2.5-flash-image",
      "name": "Gemini 2.5 Flash Image (Nano Banana)",
      "params": [],
      "kind": "image"
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro (Best)",
      "params": [
        "language",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "gemini-2.5-flash",
      "name": "Gemini 2.5 Flash",
      "params": [
        "language",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "gemini-2.5-flash-lite",
      "name": "Gemini 2.5 Flash Lite (Cheapest)",
      "params": [
        "language",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "gemini-2.0-flash",
      "name": "Gemini 2.0 Flash",
      "params": [
        "language",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "gemini-3.1-flash-tts-preview",
      "name": "Gemini 3.1 Flash TTS",
      "kind": "tts"
    },
    {
      "id": "gemini-2.5-flash-preview-tts",
      "name": "Gemini 2.5 Flash TTS",
      "kind": "tts"
    },
    {
      "id": "gemini-2.5-pro-preview-tts",
      "name": "Gemini 2.5 Pro TTS",
      "kind": "tts"
    },
    {
      "id": "embedding-001",
      "name": "Embedding 001",
      "dimensions": 768,
      "kind": "embedding"
    }
  ],
  "gh": [
    {
      "id": "gpt-5.2",
      "name": "GPT-5.2"
    },
    {
      "id": "gpt-5.2-codex",
      "name": "GPT-5.2 Codex"
    },
    {
      "id": "gpt-5.3-codex",
      "name": "GPT-5.3 Codex"
    },
    {
      "id": "gpt-5.4",
      "name": "GPT-5.4"
    },
    {
      "id": "gpt-5.4-mini",
      "name": "GPT-5.4 Mini"
    },
    {
      "id": "claude-haiku-4.5",
      "name": "Claude Haiku 4.5"
    },
    {
      "id": "claude-opus-4.5",
      "name": "Claude Opus 4.5"
    },
    {
      "id": "claude-sonnet-4.5",
      "name": "Claude Sonnet 4.5"
    },
    {
      "id": "claude-sonnet-4.6",
      "name": "Claude Sonnet 4.6"
    },
    {
      "id": "claude-opus-4.6",
      "name": "Claude Opus 4.6"
    },
    {
      "id": "claude-opus-4.7",
      "name": "Claude Opus 4.7"
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro"
    },
    {
      "id": "gemini-3-flash-preview",
      "name": "Gemini 3 Flash"
    },
    {
      "id": "gemini-3.1-pro-preview",
      "name": "Gemini 3.1 Pro"
    },
    {
      "id": "grok-code-fast-1",
      "name": "Grok Code Fast 1"
    },
    {
      "id": "oswe-vscode-prime",
      "name": "Raptor Mini"
    },
    {
      "id": "goldeneye-free-auto",
      "name": "GoldenEye"
    },
    {
      "id": "text-embedding-3-small",
      "name": "Text Embedding 3 Small (GitHub)",
      "kind": "embedding"
    },
    {
      "id": "text-embedding-3-large",
      "name": "Text Embedding 3 Large (GitHub)",
      "kind": "embedding"
    }
  ],
  "glm-cn": [
    {
      "id": "glm-5.3",
      "name": "GLM 5.3"
    },
    {
      "id": "glm-5.3-flash",
      "name": "GLM 5.3 Flash (Vision)"
    },
    {
      "id": "glm-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1"
    },
    {
      "id": "glm-5-turbo",
      "name": "GLM 5 Turbo"
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "glm-4.7",
      "name": "GLM-4.7"
    },
    {
      "id": "glm-4.6v",
      "name": "GLM 4.6V (Vision)"
    },
    {
      "id": "glm-4.6",
      "name": "GLM-4.6"
    },
    {
      "id": "glm-4.5-air",
      "name": "GLM-4.5-Air"
    }
  ],
  "glm": [
    {
      "id": "glm-5.3",
      "name": "GLM 5.3"
    },
    {
      "id": "glm-5.3-flash",
      "name": "GLM 5.3 Flash (Vision)"
    },
    {
      "id": "glm-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1"
    },
    {
      "id": "glm-5-turbo",
      "name": "GLM 5 Turbo"
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "glm-4.7",
      "name": "GLM 4.7"
    },
    {
      "id": "glm-4.6v",
      "name": "GLM 4.6V (Vision)"
    }
  ],
  "gcli": [
    {
      "id": "grok-build",
      "name": "Grok Build"
    },
    {
      "id": "grok-4.5",
      "name": "Grok 4.5"
    },
    {
      "id": "grok-4.5-high",
      "name": "Grok 4.5 (High)",
      "upstreamModelId": "grok-4.5"
    },
    {
      "id": "grok-4.5-medium",
      "name": "Grok 4.5 (Medium)",
      "upstreamModelId": "grok-4.5"
    },
    {
      "id": "grok-4.5-low",
      "name": "Grok 4.5 (Low)",
      "upstreamModelId": "grok-4.5"
    }
  ],
  "grok-web": [
    {
      "id": "grok-3",
      "name": "Grok 3"
    },
    {
      "id": "grok-3-mini",
      "name": "Grok 3 Mini (Thinking)"
    },
    {
      "id": "grok-3-thinking",
      "name": "Grok 3 Thinking"
    },
    {
      "id": "grok-4",
      "name": "Grok 4"
    },
    {
      "id": "grok-4-mini",
      "name": "Grok 4 Mini (Thinking)"
    },
    {
      "id": "grok-4-thinking",
      "name": "Grok 4 Thinking"
    },
    {
      "id": "grok-4-heavy",
      "name": "Grok 4 Heavy (SuperGrok)"
    },
    {
      "id": "grok-4.1-mini",
      "name": "Grok 4.1 Mini (Thinking)"
    },
    {
      "id": "grok-4.1-fast",
      "name": "Grok 4.1 Fast"
    },
    {
      "id": "grok-4.1-expert",
      "name": "Grok 4.1 Expert"
    },
    {
      "id": "grok-4.1-thinking",
      "name": "Grok 4.1 Thinking"
    },
    {
      "id": "grok-4.2",
      "name": "Grok 4.2 (4.20 Beta)"
    }
  ],
  "groq": [
    {
      "id": "llama-3.3-70b-versatile",
      "name": "Llama 3.3 70B"
    },
    {
      "id": "meta-llama/llama-4-maverick-17b-128e-instruct",
      "name": "Llama 4 Maverick"
    },
    {
      "id": "qwen/qwen3-32b",
      "name": "Qwen3 32B"
    },
    {
      "id": "openai/gpt-oss-120b",
      "name": "GPT-OSS 120B"
    },
    {
      "id": "whisper-large-v3",
      "name": "Whisper Large v3",
      "params": [
        "language",
        "response_format",
        "temperature",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "whisper-large-v3-turbo",
      "name": "Whisper Large v3 Turbo",
      "params": [
        "language",
        "response_format",
        "temperature",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "distil-whisper-large-v3-en",
      "name": "Distil Whisper Large v3 EN",
      "params": [
        "language",
        "response_format",
        "temperature",
        "prompt"
      ],
      "kind": "stt"
    }
  ],
  "huggingface": [
    {
      "id": "black-forest-labs/FLUX.1-schnell",
      "name": "FLUX.1 Schnell",
      "params": [],
      "kind": "image"
    },
    {
      "id": "stabilityai/stable-diffusion-xl-base-1.0",
      "name": "SDXL Base 1.0",
      "params": [],
      "kind": "image"
    },
    {
      "id": "openai/whisper-large-v3",
      "name": "Whisper Large v3 (HF)",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "openai/whisper-small",
      "name": "Whisper Small (HF)",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "black-forest-labs/FLUX.1-dev",
      "name": "FLUX.1 Dev",
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.1-Krea-dev",
      "name": "FLUX.1 Krea",
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.1-Kontext-dev",
      "name": "FLUX.1 Kontext",
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.2-dev",
      "name": "FLUX.2 Dev",
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.2-klein-9B",
      "name": "FLUX.2 Klein 9B",
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.2-klein-4B",
      "name": "FLUX.2 Klein 4B",
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.2-klein-base-9B",
      "name": "FLUX.2 Klein Base 9B",
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.2-klein-base-4B",
      "name": "FLUX.2 Klein Base 4B",
      "kind": "image"
    },
    {
      "id": "stabilityai/stable-diffusion-3.5-large",
      "name": "Stable Diffusion 3.5 Large",
      "kind": "image"
    },
    {
      "id": "stabilityai/stable-diffusion-3.5-large-turbo",
      "name": "Stable Diffusion 3.5 Large Turbo",
      "kind": "image"
    },
    {
      "id": "Qwen/Qwen-Image",
      "name": "Qwen Image",
      "kind": "image"
    },
    {
      "id": "Qwen/Qwen-Image-2512",
      "name": "Qwen Image 2512",
      "kind": "image"
    },
    {
      "id": "Qwen/Qwen-Image-Edit",
      "name": "Qwen Image Edit",
      "kind": "image"
    },
    {
      "id": "Qwen/Qwen-Image-Edit-2509",
      "name": "Qwen Image Edit 2509",
      "kind": "image"
    },
    {
      "id": "Qwen/Qwen-Image-Edit-2511",
      "name": "Qwen Image Edit 2511",
      "kind": "image"
    },
    {
      "id": "ideogram-ai/ideogram-4-fp8",
      "name": "Ideogram 4",
      "kind": "image"
    },
    {
      "id": "tencent/HunyuanImage-3.0",
      "name": "HunyuanImage 3.0",
      "kind": "image"
    },
    {
      "id": "Tongyi-MAI/Z-Image-Turbo",
      "name": "Z-Image Turbo",
      "kind": "image"
    },
    {
      "id": "krea/Krea-2-Turbo",
      "name": "Krea 2 Turbo",
      "kind": "image"
    },
    {
      "id": "HiDream-ai/HiDream-I1-Fast",
      "name": "HiDream I1 Fast",
      "kind": "image"
    },
    {
      "id": "playgroundai/playground-v2.5-1024px-aesthetic",
      "name": "Playground v2.5",
      "kind": "image"
    },
    {
      "id": "openai/whisper-large-v3-turbo",
      "name": "Whisper Large v3 Turbo (HF)",
      "kind": "stt"
    },
  ],
  "hyperbolic": [
    {
      "id": "Qwen/QwQ-32B",
      "name": "QwQ 32B"
    },
    {
      "id": "deepseek-ai/DeepSeek-R1",
      "name": "DeepSeek R1"
    },
    {
      "id": "deepseek-ai/DeepSeek-V3",
      "name": "DeepSeek V3"
    },
    {
      "id": "meta-llama/Llama-3.3-70B-Instruct",
      "name": "Llama 3.3 70B"
    },
    {
      "id": "meta-llama/Llama-3.2-3B-Instruct",
      "name": "Llama 3.2 3B"
    },
    {
      "id": "Qwen/Qwen2.5-72B-Instruct",
      "name": "Qwen 2.5 72B"
    },
    {
      "id": "Qwen/Qwen2.5-Coder-32B-Instruct",
      "name": "Qwen 2.5 Coder 32B"
    },
    {
      "id": "NousResearch/Hermes-3-Llama-3.1-70B",
      "name": "Hermes 3 70B"
    }
  ],
  "if": [
    {
      "id": "qwen3-coder-plus",
      "name": "Qwen3 Coder Plus"
    },
    {
      "id": "qwen3-max",
      "name": "Qwen3 Max"
    },
    {
      "id": "qwen3-vl-plus",
      "name": "Qwen3 VL Plus"
    },
    {
      "id": "qwen3-max-preview",
      "name": "Qwen3 Max Preview"
    },
    {
      "id": "qwen3-235b",
      "name": "Qwen3 235B A22B"
    },
    {
      "id": "qwen3-235b-a22b-instruct",
      "name": "Qwen3 235B A22B Instruct"
    },
    {
      "id": "qwen3-235b-a22b-thinking-2507",
      "name": "Qwen3 235B A22B Thinking"
    },
    {
      "id": "qwen3-32b",
      "name": "Qwen3 32B"
    },
    {
      "id": "kimi-k2",
      "name": "Kimi K2"
    },
    {
      "id": "deepseek-v3.2",
      "name": "DeepSeek V3.2 Exp"
    },
    {
      "id": "deepseek-v3.1",
      "name": "DeepSeek V3.1 Terminus"
    },
    {
      "id": "deepseek-v3",
      "name": "DeepSeek V3 671B"
    },
    {
      "id": "deepseek-r1",
      "name": "DeepSeek R1"
    },
    {
      "id": "glm-4.7",
      "name": "GLM 4.7"
    },
    {
      "id": "iflow-rome-30ba3b",
      "name": "iFlow ROME"
    }
  ],
  "inworld": [
    {
      "id": "inworld-tts-1.5-mini",
      "name": "Inworld TTS 1.5 Mini",
      "kind": "tts"
    },
    {
      "id": "inworld-tts-1.5-max",
      "name": "Inworld TTS 1.5 Max",
      "kind": "tts"
    }
  ],
  "jina-ai": [
    {
      "id": "jina-embeddings-v3",
      "name": "Jina Embeddings V3",
      "kind": "embedding"
    },
    {
      "id": "jina-embeddings-v2-base-en",
      "name": "Jina Embeddings V2 Base EN",
      "kind": "embedding"
    },
    {
      "id": "jina-embeddings-v2-base-code",
      "name": "Jina Embeddings V2 Base Code",
      "kind": "embedding"
    }
  ],
  "kc": [
    {
      "id": "anthropic/claude-sonnet-4-20250514",
      "name": "Claude Sonnet 4"
    },
    {
      "id": "anthropic/claude-opus-4-20250514",
      "name": "Claude Opus 4"
    },
    {
      "id": "google/gemini-2.5-pro",
      "name": "Gemini 2.5 Pro"
    },
    {
      "id": "google/gemini-2.5-flash",
      "name": "Gemini 2.5 Flash"
    },
    {
      "id": "openai/gpt-4.1",
      "name": "GPT-4.1"
    },
    {
      "id": "openai/o3",
      "name": "o3"
    },
    {
      "id": "deepseek/deepseek-chat",
      "name": "DeepSeek Chat"
    },
    {
      "id": "deepseek/deepseek-reasoner",
      "name": "DeepSeek Reasoner"
    }
  ],
  "kimchi": [
    {
      "id": "minimax-m3",
      "name": "MiniMax-M3"
    },
    {
      "id": "kimi-k2.7",
      "name": "Kimi-K2.7"
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi-K2.6"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi-K2.5"
    },
    {
      "id": "nemotron-3-ultra-fp4",
      "name": "Nemotron 3 Ultra FP4"
    },
    {
      "id": "minimax-m2.7",
      "name": "MiniMax-M2.7"
    },
    {
      "id": "claude-opus-4-6",
      "name": "Claude Opus 4.6"
    },
    {
      "id": "claude-sonnet-4-6",
      "name": "Claude Sonnet 4.6"
    }
  ],
  "kimi": [
    {
      "id": "kimi-k3",
      "name": "Kimi K3"
    },
    {
      "id": "k3",
      "name": "Kimi K3 (Code)"
    },
    {
      "id": "kimi-for-coding",
      "name": "Kimi for Coding"
    },
    {
      "id": "kimi-for-coding-highspeed",
      "name": "Kimi for Coding Highspeed"
    },
    {
      "id": "kimi-k2.7-code",
      "name": "Kimi K2.7 Code"
    },
    {
      "id": "kimi-k2.7-code-highspeed",
      "name": "Kimi K2.7 Code Highspeed"
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "kimi-k2.5-thinking",
      "name": "Kimi K2.5 Thinking"
    },
    {
      "id": "kimi-latest",
      "name": "Kimi Latest"
    }
  ],
  "kr": [
    {
      "id": "claude-opus-5-5",
      "name": "Claude Opus 5.5"
    },
    {
      "id": "claude-opus-5",
      "name": "Claude Opus 5"
    },
    {
      "id": "claude-opus-5-thinking",
      "name": "Claude Opus 5 (Thinking)"
    },
    {
      "id": "claude-opus-5-agentic",
      "name": "Claude Opus 5 (Agentic)"
    },
    {
      "id": "claude-opus-5-thinking-agentic",
      "name": "Claude Opus 5 (Thinking + Agentic)"
    },
    {
      "id": "claude-opus-4.8",
      "name": "Claude Opus 4.8"
    },
    {
      "id": "claude-opus-4.8-thinking",
      "name": "Claude Opus 4.8 (Thinking)"
    },
    {
      "id": "claude-opus-4.8-agentic",
      "name": "Claude Opus 4.8 (Agentic)"
    },
    {
      "id": "claude-opus-4.8-thinking-agentic",
      "name": "Claude Opus 4.8 (Thinking + Agentic)"
    },
    {
      "id": "claude-opus-4.7",
      "name": "Claude Opus 4.7"
    },
    {
      "id": "claude-opus-4.7-thinking",
      "name": "Claude Opus 4.7 (Thinking)"
    },
    {
      "id": "claude-opus-4.7-agentic",
      "name": "Claude Opus 4.7 (Agentic)"
    },
    {
      "id": "claude-opus-4.7-thinking-agentic",
      "name": "Claude Opus 4.7 (Thinking + Agentic)"
    },
    {
      "id": "claude-opus-4.5",
      "name": "Claude Opus 4.5"
    },
    {
      "id": "claude-opus-4.5-thinking",
      "name": "Claude Opus 4.5 (Thinking)"
    },
    {
      "id": "claude-opus-4.5-agentic",
      "name": "Claude Opus 4.5 (Agentic)"
    },
    {
      "id": "claude-opus-4.5-thinking-agentic",
      "name": "Claude Opus 4.5 (Thinking + Agentic)"
    },
    {
      "id": "claude-sonnet-5",
      "name": "Claude Sonnet 5"
    },
    {
      "id": "claude-sonnet-4.5",
      "name": "Claude Sonnet 4.5"
    },
    {
      "id": "claude-haiku-4.5",
      "name": "Claude Haiku 4.5"
    },
    {
      "id": "deepseek-3.2",
      "name": "DeepSeek 3.2",
      "strip": [
        "image",
        "audio"
      ]
    },
    {
      "id": "qwen3-coder-next",
      "name": "Qwen3 Coder Next",
      "strip": [
        "image",
        "audio"
      ]
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "gpt-5.6-sol",
      "name": "GPT 5.6 Sol",
      "contextLength": 272000,
      "rateMultiplier": 2.4,
      "upstreamModelId": "gpt-5.6-sol",
      "description": "Experimental preview of OpenAI GPT 5.6 Sol with 272k context window"
    },
    {
      "id": "gpt-5.6-terra",
      "name": "GPT 5.6 Terra",
      "contextLength": 272000,
      "rateMultiplier": 1.2,
      "upstreamModelId": "gpt-5.6-terra",
      "description": "Experimental preview of OpenAI GPT 5.6 Terra with 272k context window"
    },
    {
      "id": "gpt-5.6-luna",
      "name": "GPT 5.6 Luna",
      "contextLength": 272000,
      "rateMultiplier": 0.6,
      "upstreamModelId": "gpt-5.6-luna",
      "description": "Experimental preview of OpenAI GPT 5.6 Luna with 272k context window"
    },
    {
      "id": "claude-sonnet-5-thinking",
      "name": "Claude Sonnet 5 (Thinking)"
    },
    {
      "id": "claude-sonnet-4.5-thinking",
      "name": "Claude Sonnet 4.5 (Thinking)"
    },
    {
      "id": "claude-haiku-4.5-thinking",
      "name": "Claude Haiku 4.5 (Thinking)"
    },
    {
      "id": "gpt-5.6-sol-thinking",
      "name": "GPT 5.6 Sol (Thinking)",
      "contextLength": 272000,
      "rateMultiplier": 2.4,
      "upstreamModelId": "gpt-5.6-sol",
      "description": "Experimental preview of OpenAI GPT 5.6 Sol with 272k context window"
    },
    {
      "id": "gpt-5.6-terra-thinking",
      "name": "GPT 5.6 Terra (Thinking)",
      "contextLength": 272000,
      "rateMultiplier": 1.2,
      "upstreamModelId": "gpt-5.6-terra",
      "description": "Experimental preview of OpenAI GPT 5.6 Terra with 272k context window"
    },
    {
      "id": "gpt-5.6-luna-thinking",
      "name": "GPT 5.6 Luna (Thinking)",
      "contextLength": 272000,
      "rateMultiplier": 0.6,
      "upstreamModelId": "gpt-5.6-luna",
      "description": "Experimental preview of OpenAI GPT 5.6 Luna with 272k context window"
    },
    {
      "id": "claude-sonnet-5-agentic",
      "name": "Claude Sonnet 5 (Agentic)"
    },
    {
      "id": "claude-sonnet-4.5-agentic",
      "name": "Claude Sonnet 4.5 (Agentic)"
    },
    {
      "id": "claude-haiku-4.5-agentic",
      "name": "Claude Haiku 4.5 (Agentic)"
    },
    {
      "id": "gpt-5.6-sol-agentic",
      "name": "GPT 5.6 Sol (Agentic)",
      "contextLength": 272000,
      "rateMultiplier": 2.4,
      "upstreamModelId": "gpt-5.6-sol",
      "description": "Experimental preview of OpenAI GPT 5.6 Sol with 272k context window"
    },
    {
      "id": "gpt-5.6-terra-agentic",
      "name": "GPT 5.6 Terra (Agentic)",
      "contextLength": 272000,
      "rateMultiplier": 1.2,
      "upstreamModelId": "gpt-5.6-terra",
      "description": "Experimental preview of OpenAI GPT 5.6 Terra with 272k context window"
    },
    {
      "id": "gpt-5.6-luna-agentic",
      "name": "GPT 5.6 Luna (Agentic)",
      "contextLength": 272000,
      "rateMultiplier": 0.6,
      "upstreamModelId": "gpt-5.6-luna",
      "description": "Experimental preview of OpenAI GPT 5.6 Luna with 272k context window"
    },
    {
      "id": "claude-sonnet-5-thinking-agentic",
      "name": "Claude Sonnet 5 (Thinking + Agentic)"
    },
    {
      "id": "claude-sonnet-4.5-thinking-agentic",
      "name": "Claude Sonnet 4.5 (Thinking + Agentic)"
    },
    {
      "id": "claude-haiku-4.5-thinking-agentic",
      "name": "Claude Haiku 4.5 (Thinking + Agentic)"
    },
    {
      "id": "gpt-5.6-sol-thinking-agentic",
      "name": "GPT 5.6 Sol (Thinking + Agentic)",
      "contextLength": 272000,
      "rateMultiplier": 2.4,
      "upstreamModelId": "gpt-5.6-sol",
      "description": "Experimental preview of OpenAI GPT 5.6 Sol with 272k context window"
    },
    {
      "id": "gpt-5.6-terra-thinking-agentic",
      "name": "GPT 5.6 Terra (Thinking + Agentic)",
      "contextLength": 272000,
      "rateMultiplier": 1.2,
      "upstreamModelId": "gpt-5.6-terra",
      "description": "Experimental preview of OpenAI GPT 5.6 Terra with 272k context window"
    },
    {
      "id": "gpt-5.6-luna-thinking-agentic",
      "name": "GPT 5.6 Luna (Thinking + Agentic)",
      "contextLength": 272000,
      "rateMultiplier": 0.6,
      "upstreamModelId": "gpt-5.6-luna",
      "description": "Experimental preview of OpenAI GPT 5.6 Luna with 272k context window"
    }
  ],
  "mmf": [
    {
      "id": "mimo-auto",
      "name": "MiMo Auto"
    }
  ],
  "muse": [
    {
      "id": "muse-spark-1.3",
      "name": "Muse Spark 1.3",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "muse-spark-1.2",
      "name": "Muse Spark 1.2",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "muse-spark-1.1",
      "name": "Muse Spark 1.1",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "muse-spark-1.3-contributor",
      "name": "Muse Spark 1.3 Contributor",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "muse-spark-1.2-contributor",
      "name": "Muse Spark 1.2 Contributor",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    }
  ],
  "minimax-cn": [
    {
      "id": "MiniMax-M3",
      "name": "MiniMax M3",
      "targetFormat": "claude"
    },
    {
      "id": "MiniMax-M2.7",
      "name": "MiniMax M2.7"
    },
    {
      "id": "MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "MiniMax-M2.1",
      "name": "MiniMax M2.1"
    },
    {
      "id": "speech-2.8-hd",
      "name": "Speech 2.8 HD",
      "kind": "tts"
    },
    {
      "id": "speech-2.8-turbo",
      "name": "Speech 2.8 Turbo",
      "kind": "tts"
    },
    {
      "id": "speech-2.6-hd",
      "name": "Speech 2.6 HD",
      "kind": "tts"
    },
    {
      "id": "speech-2.6-turbo",
      "name": "Speech 2.6 Turbo",
      "kind": "tts"
    },
    {
      "id": "speech-02-hd",
      "name": "Speech 02 HD",
      "kind": "tts"
    },
    {
      "id": "speech-02-turbo",
      "name": "Speech 02 Turbo",
      "kind": "tts"
    },
    {
      "id": "speech-01-hd",
      "name": "Speech 01 HD",
      "kind": "tts"
    },
    {
      "id": "speech-01-turbo",
      "name": "Speech 01 Turbo",
      "kind": "tts"
    }
  ],
  "minimax": [
    {
      "id": "MiniMax-M3",
      "name": "MiniMax M3",
      "targetFormat": "claude"
    },
    {
      "id": "MiniMax-M2.7",
      "name": "MiniMax M2.7"
    },
    {
      "id": "MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "MiniMax-M2.1",
      "name": "MiniMax M2.1"
    },
    {
      "id": "minimax-image-01",
      "name": "MiniMax Image 01",
      "params": [
        "n",
        "size",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "speech-2.8-hd",
      "name": "Speech 2.8 HD",
      "kind": "tts"
    },
    {
      "id": "speech-2.8-turbo",
      "name": "Speech 2.8 Turbo",
      "kind": "tts"
    },
    {
      "id": "speech-2.6-hd",
      "name": "Speech 2.6 HD",
      "kind": "tts"
    },
    {
      "id": "speech-2.6-turbo",
      "name": "Speech 2.6 Turbo",
      "kind": "tts"
    },
    {
      "id": "speech-02-hd",
      "name": "Speech 02 HD",
      "kind": "tts"
    },
    {
      "id": "speech-02-turbo",
      "name": "Speech 02 Turbo",
      "kind": "tts"
    },
    {
      "id": "speech-01-hd",
      "name": "Speech 01 HD",
      "kind": "tts"
    },
    {
      "id": "speech-01-turbo",
      "name": "Speech 01 Turbo",
      "kind": "tts"
    }
  ],
  "minimax-code": [
    {
      "id": "MiniMax-M3.1-Flash-Preview",
      "name": "MiniMax M3.1 Flash Preview",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    },
    {
      "id": "MiniMax-M3",
      "name": "MiniMax M3",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    },
    {
      "id": "MiniMax-M2.7",
      "name": "MiniMax M2.7",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    },
    {
      "id": "MiniMax-M2.7-highspeed",
      "name": "MiniMax M2.7 Highspeed",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    }
  ],
  "minimax-code-global": [
    {
      "id": "MiniMax-M3.1-Flash-Preview",
      "name": "MiniMax M3.1 Flash Preview",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    },
    {
      "id": "MiniMax-M3",
      "name": "MiniMax M3",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    },
    {
      "id": "MiniMax-M2.7",
      "name": "MiniMax M2.7",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    },
    {
      "id": "MiniMax-M2.7-highspeed",
      "name": "MiniMax M2.7 Highspeed",
      "targetFormat": "claude",
      "supportedFormats": ["claude"]
    }
  ],
  "mistral": [
    {
      "id": "mistral-large-latest",
      "name": "Mistral Large 3"
    },
    {
      "id": "codestral-latest",
      "name": "Codestral"
    },
    {
      "id": "mistral-medium-latest",
      "name": "Mistral Medium 3"
    },
    {
      "id": "mistral-embed",
      "name": "Mistral Embed",
      "kind": "embedding"
    }
  ],
  "nanobanana": [
    {
      "id": "nanobanana-flash",
      "name": "NanoBanana Flash",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "nanobanana-pro",
      "name": "NanoBanana Pro",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    }
  ],
  "nebius": [
    {
      "id": "meta-llama/Llama-3.3-70B-Instruct",
      "name": "Llama 3.3 70B Instruct"
    },
    {
      "id": "Qwen/Qwen3-Embedding-8B",
      "name": "Qwen3 Embedding 8B",
      "kind": "embedding"
    }
  ],
  "nvidia": [
    {
      "id": "minimaxai/minimax-m2.7",
      "name": "MiniMax M2.7"
    },
    {
      "id": "minimaxai/minimax-m3",
      "name": "MiniMax M3"
    },
    {
      "id": "z-ai/glm-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "deepseek-ai/deepseek-v4-pro",
      "name": "DeepSeek V4 Pro"
    },
    {
      "id": "deepseek-ai/deepseek-v4-flash",
      "name": "DeepSeek V4 Flash"
    },
    {
      "id": "moonshotai/kimi-k2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "nvidia/nemotron-3-ultra-550b-a55b",
      "name": "Nemotron 3 Ultra"
    },
    {
      "id": "nvidia/nv-embedqa-e5-v5",
      "name": "NV EmbedQA E5 v5",
      "kind": "embedding"
    },
    {
      "id": "nvidia/parakeet-ctc-1.1b-asr",
      "name": "Parakeet CTC 1.1B",
      "params": [
        "language"
      ],
      "kind": "stt"
    },
    {
      "id": "fastpitch",
      "name": "FastPitch",
      "kind": "tts"
    },
    {
      "id": "tacotron2",
      "name": "Tacotron2",
      "kind": "tts"
    }
  ],
  "ollama": [
    {
      "id": "gpt-oss:120b",
      "name": "GPT OSS 120B"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "minimax-m2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "glm-4.7-flash",
      "name": "GLM 4.7 Flash"
    },
    {
      "id": "qwen3.5",
      "name": "Qwen3.5"
    },
    {
      "id": "minimax-m3",
      "name": "MiniMax M3"
    },
    {
      "id": "deepseek-v4.1-flash:cloud",
      "name": "DeepSeek V4.1 Flash (Cloud)"
    }
  ],
  "openai": [
    {
      "id": "gpt-5.5",
      "name": "GPT-5.5"
    },
    {
      "id": "gpt-5.4",
      "name": "GPT-5.4"
    },
    {
      "id": "gpt-5.4-mini",
      "name": "GPT-5.4 Mini"
    },
    {
      "id": "gpt-5.4-nano",
      "name": "GPT-5.4 Nano"
    },
    {
      "id": "gpt-5.2",
      "name": "GPT-5.2"
    },
    {
      "id": "gpt-5.1",
      "name": "GPT-5.1"
    },
    {
      "id": "gpt-5",
      "name": "GPT-5"
    },
    {
      "id": "gpt-5-mini",
      "name": "GPT-5 Mini"
    },
    {
      "id": "gpt-5-nano",
      "name": "GPT-5 Nano"
    },
    {
      "id": "gpt-4o",
      "name": "GPT-4o"
    },
    {
      "id": "gpt-4o-mini",
      "name": "GPT-4o Mini"
    },
    {
      "id": "gpt-4-turbo",
      "name": "GPT-4 Turbo"
    },
    {
      "id": "gpt-4.1",
      "name": "GPT-4.1"
    },
    {
      "id": "gpt-4.1-mini",
      "name": "GPT-4.1 Mini"
    },
    {
      "id": "gpt-4.1-nano",
      "name": "GPT-4.1 Nano"
    },
    {
      "id": "o3",
      "name": "O3"
    },
    {
      "id": "o3-mini",
      "name": "O3 Mini"
    },
    {
      "id": "o3-pro",
      "name": "O3 Pro"
    },
    {
      "id": "o4-mini",
      "name": "O4 Mini"
    },
    {
      "id": "o1",
      "name": "O1"
    },
    {
      "id": "o1-mini",
      "name": "O1 Mini"
    },
    {
      "id": "text-embedding-3-large",
      "name": "Text Embedding 3 Large",
      "kind": "embedding"
    },
    {
      "id": "text-embedding-3-small",
      "name": "Text Embedding 3 Small",
      "kind": "embedding"
    },
    {
      "id": "text-embedding-ada-002",
      "name": "Text Embedding Ada 002",
      "kind": "embedding"
    },
    {
      "id": "tts-1",
      "name": "TTS-1",
      "kind": "tts"
    },
    {
      "id": "tts-1-hd",
      "name": "TTS-1 HD",
      "kind": "tts"
    },
    {
      "id": "gpt-4o-mini-tts",
      "name": "GPT-4o Mini TTS",
      "kind": "tts"
    },
    {
      "id": "whisper-1",
      "name": "Whisper 1",
      "params": [
        "language",
        "response_format",
        "temperature",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "gpt-4o-transcribe",
      "name": "GPT-4o Transcribe",
      "params": [
        "language",
        "response_format",
        "temperature",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "gpt-4o-mini-transcribe",
      "name": "GPT-4o Mini Transcribe",
      "params": [
        "language",
        "response_format",
        "temperature",
        "prompt"
      ],
      "kind": "stt"
    },
    {
      "id": "gpt-image-2.5",
      "name": "GPT Image 2.5",
      "params": [
        "n",
        "size",
        "quality",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "gpt-image-2.5-flare",
      "name": "GPT Image 2.5 Flare",
      "params": [
        "n",
        "size",
        "quality",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "gpt-image-2.5-sunburst",
      "name": "GPT Image 2.5 Sunburst",
      "params": [
        "n",
        "size",
        "quality",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "gpt-image-1",
      "name": "GPT Image 1",
      "params": [
        "n",
        "size",
        "quality",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "dall-e-3",
      "name": "DALL-E 3",
      "params": [
        "size",
        "quality",
        "style",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "dall-e-2",
      "name": "DALL-E 2",
      "params": [
        "n",
        "size",
        "response_format"
      ],
      "kind": "image"
    }
  ],
  "opencode-go": [
    {
      "id": "deepseek-flash",
      "name": "DeepSeek Flash"
    },
    {
      "id": "glm-5.3-flash",
      "name": "GLM 5.3 Flash (Vision)"
    },
    {
      "id": "glm-5.3",
      "name": "GLM 5.3"
    },
    {
      "id": "glm-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1"
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "kimi-k2.7-code",
      "name": "Kimi K2.7 Code"
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "kimi-k3",
      "name": "Kimi K3"
    },
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek V4 Pro"
    },
    {
      "id": "deepseek-v4-flash",
      "name": "DeepSeek V4 Flash"
    },
    {
      "id": "deepseek-v4-flash-vision-exp",
      "name": "DeepSeek V4 Flash Vision (Exp)"
    },
    {
      "id": "deepseek-v4.1-flash",
      "name": "DeepSeek V4.1 Flash"
    },
    {
      "id": "longcat-2.0",
      "name": "LongCat 2.0"
    },
    {
      "id": "mimo-v2.6-flash",
      "name": "MiMo V2.6 Flash"
    },
    {
      "id": "mimo-v2.6-pro",
      "name": "MiMo V2.6 Pro"
    },
    {
      "id": "mimo-v2.5",
      "name": "MiMo V2.5"
    },
    {
      "id": "mimo-v2.5-pro",
      "name": "MiMo V2.5 Pro"
    },
    {
      "id": "mimo-v2-pro",
      "name": "MiMo V2 Pro"
    },
    {
      "id": "mimo-v2-omni",
      "name": "MiMo V2 Omni"
    },
    {
      "id": "minimax-m3",
      "name": "MiniMax M3"
    },
    {
      "id": "minimax-m2.7",
      "name": "MiniMax M2.7"
    },
    {
      "id": "minimax-m2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "space-bunny-free",
      "name": "Space Bunny Free"
    },
    {
      "id": "qwen3.8-max",
      "name": "Qwen 3.8 Max"
    },
    {
      "id": "qwen3.8-flash",
      "name": "Qwen 3.8 Flash"
    },
    {
      "id": "qwen3.7-max",
      "name": "Qwen 3.7 Max"
    },
    {
      "id": "qwen3.7-plus",
      "name": "Qwen 3.7 Plus"
    },
    {
      "id": "qwen3.6-plus",
      "name": "Qwen 3.6 Plus"
    },
    {
      "id": "qwen3.5-plus",
      "name": "Qwen 3.5 Plus"
    },
    {
      "id": "hy4-preview",
      "name": "Hy4 Preview"
    },
    {
      "id": "hy3",
      "name": "Hy3"
    },
    {
      "id": "hy3-preview",
      "name": "Hy3 Preview"
    },
    {
      "id": "omen-alpha",
      "name": "Omen Alpha"
    },
    {
      "id": "grok-4.7",
      "name": "Grok 4.7"
    },
    {
      "id": "grok-4.6",
      "name": "Grok 4.6"
    },
    {
      "id": "grok-4.5",
      "name": "Grok 4.5"
    },
    {
      "id": "gpt-5.6-luna",
      "name": "GPT-5.6 Luna"
    },
    {
      "id": "gpt-6-luna",
      "name": "GPT-6 Luna"
    },
    {
      "id": "muse-spark-1.2-contributor",
      "name": "Muse Spark 1.2 Contributor"
    },
    {
      "id": "muse-spark-1.3-contributor",
      "name": "Muse Spark 1.3 Contributor"
    }
  ],
  "oc": [
    {
      "id": "mimo-v2.5-free",
      "name": "mimo-v2.5-free",
      "supportedFormats": ["openai"],
      "capabilities": ["vision"]
    },
    {
      "id": "nemotron-3-ultra-free",
      "name": "nemotron-3-ultra-free",
      "supportedFormats": ["openai"],
      "capabilities": ["reasoning"]
    },
    {
      "id": "big-pickle",
      "name": "big-pickle",
      "supportedFormats": ["openai"]
    },
    {
      "id": "nemotron-3.5-lightning-free",
      "name": "nemotron-3.5-lightning-free",
      "supportedFormats": ["openai"],
      "capabilities": ["reasoning"]
    },
    {
      "id": "muse-spark-1.2-contributor-free",
      "name": "Muse Spark 1.2 Contributor Free",
      "supportedFormats": ["openai"],
      "capabilities": ["vision", "reasoning"]
    },
    {
      "id": "muse-spark-1.3-contributor-free",
      "name": "Muse Spark 1.3 Contributor Free",
      "supportedFormats": ["openai"],
      "capabilities": ["vision", "reasoning"]
    },
    {
      "id": "jev-1.13-free",
      "name": "Jev 1.13 Free",
      "kind": "systemone"
    },
  ],
  "ocz": [
    {
      "id": "claude-fable-5",
      "name": "Claude Fable 5",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-fable-5-1",
      "name": "Claude Fable 5.1",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-opus-5",
      "name": "Claude Opus 5",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-opus-4-8",
      "name": "Claude Opus 4.8",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-opus-4-7",
      "name": "Claude Opus 4.7",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-opus-4-6",
      "name": "Claude Opus 4.6",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-opus-4-5",
      "name": "Claude Opus 4.5",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-sonnet-5",
      "name": "Claude Sonnet 5",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-sonnet-4-6",
      "name": "Claude Sonnet 4.6",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-sonnet-4-5",
      "name": "Claude Sonnet 4.5",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-sonnet-4",
      "name": "Claude Sonnet 4",
      "supportedFormats": ["claude"]
    },
    {
      "id": "claude-haiku-4-5",
      "name": "Claude Haiku 4.5",
      "supportedFormats": ["claude"]
    },
    {
      "id": "gemini-3.6-flash",
      "name": "Gemini 3.6 Flash",
      "supportedFormats": ["openai"]
    },
    {
      "id": "gemini-3.8-flash",
      "name": "Gemini 3.8 Flash",
      "supportedFormats": ["openai"]
    },
    {
      "id": "gemini-3.7-flash",
      "name": "Gemini 3.7 Flash",
      "supportedFormats": ["openai"]
    },
    {
      "id": "gemini-3.5-flash-lite",
      "name": "Gemini 3.5 Flash Lite",
      "supportedFormats": ["openai"]
    },
    {
      "id": "gemini-3.5-flash",
      "name": "Gemini 3.5 Flash",
      "supportedFormats": ["openai"]
    },
    {
      "id": "gemini-3.1-pro",
      "name": "Gemini 3.1 Pro",
      "supportedFormats": ["openai"]
    },
    {
      "id": "gemini-3-flash",
      "name": "Gemini 3 Flash",
      "supportedFormats": ["openai"]
    },
    {
      "id": "gpt-6-astra",
      "name": "GPT 6 Astra",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.6-sol",
      "name": "GPT 5.6 Sol",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.6-terra",
      "name": "GPT 5.6 Terra",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.6-luna",
      "name": "GPT 5.6 Luna",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.5",
      "name": "GPT 5.5",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.5-pro",
      "name": "GPT 5.5 Pro",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.4",
      "name": "GPT 5.4",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.4-pro",
      "name": "GPT 5.4 Pro",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.4-mini",
      "name": "GPT 5.4 Mini",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.4-nano",
      "name": "GPT 5.4 Nano",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.3-codex-spark",
      "name": "GPT 5.3 Codex Spark",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.3-codex",
      "name": "GPT 5.3 Codex",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.2",
      "name": "GPT 5.2",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.2-codex",
      "name": "GPT 5.2 Codex",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.1",
      "name": "GPT 5.1",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.1-codex-max",
      "name": "GPT 5.1 Codex Max",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.1-codex",
      "name": "GPT 5.1 Codex",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5.1-codex-mini",
      "name": "GPT 5.1 Codex Mini",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5",
      "name": "GPT 5",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5-codex",
      "name": "GPT 5 Codex",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "gpt-5-nano",
      "name": "GPT 5 Nano",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "grok-build-0.1",
      "name": "Grok Build 0.1",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "grok-4.6",
      "name": "Grok 4.6",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "grok-4.5",
      "name": "Grok 4.5",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "muse-spark-1.3",
      "name": "Muse Spark 1.3",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "muse-spark-1.2",
      "name": "Muse Spark 1.2",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "qwen3.6-plus",
      "name": "Qwen 3.6 Plus",
      "supportedFormats": ["claude"]
    },
    {
      "id": "qwen3.5-plus",
      "name": "Qwen 3.5 Plus",
      "supportedFormats": ["claude"]
    },
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek V4 Pro",
      "supportedFormats": ["openai"]
    },
    {
      "id": "deepseek-v4-flash",
      "name": "DeepSeek V4 Flash",
      "supportedFormats": ["openai"]
    },
    {
      "id": "deepseek-v4-flash-vision-exp",
      "name": "DeepSeek V4 Flash Vision Exp",
      "supportedFormats": ["openai"]
    },
    {
      "id": "glm-5.3-flash",
      "name": "GLM 5.3 Flash (Vision)",
      "supportedFormats": ["openai"]
    },
    {
      "id": "glm-5.3",
      "name": "GLM 5.3",
      "supportedFormats": ["openai"]
    },
    {
      "id": "glm-5.2",
      "name": "GLM 5.2",
      "supportedFormats": ["openai"]
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1",
      "supportedFormats": ["openai"]
    },
    {
      "id": "glm-5",
      "name": "GLM 5",
      "supportedFormats": ["openai"]
    },
    {
      "id": "minimax-m3",
      "name": "MiniMax M3",
      "supportedFormats": ["openai"]
    },
    {
      "id": "minimax-m2.7",
      "name": "MiniMax M2.7",
      "supportedFormats": ["openai"]
    },
    {
      "id": "minimax-m2.5",
      "name": "MiniMax M2.5",
      "supportedFormats": ["openai"]
    },
    {
      "id": "kimi-k3",
      "name": "Kimi K3",
      "supportedFormats": ["openai"]
    },
    {
      "id": "kimi-k2.7-code",
      "name": "Kimi K2.7 Code",
      "supportedFormats": ["openai"]
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi K2.6",
      "supportedFormats": ["openai"]
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5",
      "supportedFormats": ["openai"]
    },
    {
      "id": "big-pickle",
      "name": "Big Pickle",
      "supportedFormats": ["openai"]
    },
    {
      "id": "union-alpha",
      "name": "Union Alpha",
      "supportedFormats": ["claude"]
    },
    {
      "id": "deepseek-v4-flash-free",
      "name": "DeepSeek V4 Flash Free",
      "supportedFormats": ["openai"]
    },
    {
      "id": "mimo-v2.6-flash-free",
      "name": "MiMo V2.6 Flash Free",
      "supportedFormats": ["openai"]
    },
    {
      "id": "mimo-v2.5-free",
      "name": "MiMo V2.5 Free",
      "supportedFormats": ["openai"]
    },
    {
      "id": "ling-3.0-flash-fin-free",
      "name": "Ling 3.0 Flash Fin Free",
      "supportedFormats": ["openai"]
    },
    {
      "id": "nemotron-3-ultra-free",
      "name": "Nemotron 3 Ultra Free",
      "supportedFormats": ["openai"]
    },
    {
      "id": "nemotron-3.5-lightning-free",
      "name": "Nemotron 3.5 Lightning Free",
      "supportedFormats": ["openai"]
    },
    {
      "id": "muse-spark-1.3-contributor-free",
      "name": "Muse Spark 1.3 Contributor Free",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "muse-spark-1.2-contributor-free",
      "name": "Muse Spark 1.2 Contributor Free",
      "targetFormat": "openai-responses",
      "supportedFormats": ["openai-responses"]
    },
    {
      "id": "jev-1.13",
      "name": "Jev 1.13",
      "kind": "systemone"
    },
    {
      "id": "jev-1.13-free",
      "name": "Jev 1.13 Free",
      "kind": "systemone"
    }
  ],
  "openrouter": [
    {
      "id": "openai/text-embedding-3-large",
      "name": "OpenAI Text Embedding 3 Large",
      "kind": "embedding"
    },
    {
      "id": "openai/text-embedding-3-small",
      "name": "OpenAI Text Embedding 3 Small",
      "kind": "embedding"
    },
    {
      "id": "openai/text-embedding-ada-002",
      "name": "OpenAI Text Embedding Ada 002",
      "kind": "embedding"
    },
    {
      "id": "qwen/qwen3-embedding-8b",
      "name": "Qwen3 Embedding 8B",
      "kind": "embedding"
    },
    {
      "id": "perplexity/pplx-embed-v1-4b",
      "name": "Perplexity Embed V1 4B",
      "kind": "embedding"
    },
    {
      "id": "perplexity/pplx-embed-v1-0.6b",
      "name": "Perplexity Embed V1 0.6B",
      "kind": "embedding"
    },
    {
      "id": "nvidia/llama-nemotron-embed-vl-1b-v2:free",
      "name": "NVIDIA Nemotron Embed VL 1B V2 (Free)",
      "kind": "embedding"
    },
    {
      "id": "openai/gpt-4o-mini-tts",
      "name": "GPT-4o Mini TTS",
      "kind": "tts"
    },
    {
      "id": "openai/tts-1-hd",
      "name": "TTS-1 HD",
      "kind": "tts"
    },
    {
      "id": "openai/tts-1",
      "name": "TTS-1",
      "kind": "tts"
    },
    {
      "id": "openai/dall-e-3",
      "name": "DALL-E 3 (via OpenRouter)",
      "params": [
        "size",
        "quality",
        "style",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "openai/gpt-image-1",
      "name": "GPT Image 1 (via OpenRouter)",
      "params": [
        "n",
        "size",
        "quality",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "google/imagen-3.0-generate-002",
      "name": "Imagen 3 (via OpenRouter)",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "black-forest-labs/FLUX.1-schnell",
      "name": "FLUX.1 Schnell (via OpenRouter)",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "google/veo-3.1",
      "name": "Veo 3.1 (via OpenRouter)",
      "params": [
        "duration",
        "aspect_ratio",
        "resolution"
      ],
      "kind": "video"
    },
    {
      "id": "openai/sora-2-pro",
      "name": "Sora 2 Pro (via OpenRouter)",
      "params": [
        "duration",
        "aspect_ratio",
        "resolution"
      ],
      "kind": "video"
    },
    {
      "id": "bytedance/seedance-2.0",
      "name": "Seedance 2.0 (via OpenRouter)",
      "params": [
        "duration",
        "aspect_ratio",
        "resolution"
      ],
      "kind": "video"
    },
    {
      "id": "typesafe/jev-1.13",
      "name": "Jev 1.13",
      "kind": "systemone"
    }
  ],
  "perplexity-web": [
    {
      "id": "pplx-auto",
      "name": "Perplexity Auto (Free)"
    },
    {
      "id": "pplx-sonar",
      "name": "Perplexity Sonar"
    },
    {
      "id": "pplx-gpt",
      "name": "GPT-5.4 (via Perplexity)"
    },
    {
      "id": "pplx-gemini",
      "name": "Gemini 3.1 Pro (via Perplexity)"
    },
    {
      "id": "pplx-sonnet",
      "name": "Claude Sonnet 4.6 (via Perplexity)"
    },
    {
      "id": "pplx-opus",
      "name": "Claude Opus 4.6 (via Perplexity)"
    },
    {
      "id": "pplx-nemotron",
      "name": "Nemotron 3 Super (via Perplexity)"
    }
  ],
  "perplexity": [
    {
      "id": "sonar-pro",
      "name": "Sonar Pro"
    },
    {
      "id": "sonar",
      "name": "Sonar"
    }
  ],
  "perplexity-agent": [
    {
      "id": "perplexity/sonar",
      "name": "Perplexity Sonar"
    },
    {
      "id": "openai/gpt-5.5",
      "name": "GPT-5.5"
    },
    {
      "id": "openai/gpt-5.4",
      "name": "GPT-5.4"
    },
    {
      "id": "openai/gpt-5.4-mini",
      "name": "GPT-5.4 Mini"
    },
    {
      "id": "anthropic/claude-sonnet-4-6",
      "name": "Claude Sonnet 4.6"
    },
    {
      "id": "anthropic/claude-opus-4-8",
      "name": "Claude Opus 4.8"
    },
    {
      "id": "google/gemini-3.1-pro-preview",
      "name": "Gemini 3.1 Pro"
    },
    {
      "id": "xai/grok-4.20-reasoning",
      "name": "Grok 4.20 Reasoning"
    },
    {
      "id": "perplexity/glm-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "perplexity/kimi-k2.7-code",
      "name": "Kimi K2.7 Code"
    },
    {
      "id": "nvidia/nemotron-3-super-120b-a12b",
      "name": "Nemotron 3 Super 120B"
    }
  ],
  "playht": [
    {
      "id": "PlayDialog",
      "name": "PlayDialog",
      "kind": "tts"
    },
    {
      "id": "Play3.0-mini",
      "name": "Play3.0 Mini",
      "kind": "tts"
    }
  ],
  "qoder-cn": [
    {
      "id": "ultimate",
      "name": "Ultimate"
    },
    {
      "id": "auto",
      "name": "Auto"
    },
    {
      "id": "performance",
      "name": "Performance"
    },
    {
      "id": "efficient",
      "name": "Efficient"
    },
    {
      "id": "lite",
      "name": "Lite"
    },
    {
      "id": "qmodel_38max",
      "name": "Qwen3.8-Max"
    },
    {
      "id": "qmodel_latest",
      "name": "Qwen3.7-Max"
    },
    {
      "id": "qmodel",
      "name": "Qwen3.7-Plus"
    },
    {
      "id": "qfmodel",
      "name": "Qwen3.8-Flash"
    },
    {
      "id": "kmodel_latest",
      "name": "Kimi-K3"
    },
    {
      "id": "kmodel",
      "name": "Kimi-K2.7-Code"
    },
    {
      "id": "gmodel",
      "name": "GLM-5.3"
    },
    {
      "id": "gfmodel",
      "name": "GLM-5.3-Flash"
    },
    {
      "id": "dmodel",
      "name": "DeepSeek-V4-Pro"
    },
    {
      "id": "dfmodel",
      "name": "DeepSeek-V4-Flash"
    },
    {
      "id": "mmodel",
      "name": "MiniMax-M3"
    },
  ],
  "qd": [
    {
      "id": "ultimate",
      "name": "Ultimate"
    },
    {
      "id": "auto",
      "name": "Auto"
    },
    {
      "id": "performance",
      "name": "Performance"
    },
    {
      "id": "efficient",
      "name": "Efficient"
    },
    {
      "id": "lite",
      "name": "Lite"
    },
    {
      "id": "qmodel_38max",
      "name": "Qwen3.8-Max"
    },
    {
      "id": "qmodel_latest",
      "name": "Qwen3.7-Max"
    },
    {
      "id": "qmodel",
      "name": "Qwen3.7-Plus"
    },
    {
      "id": "qfmodel",
      "name": "Qwen3.8-Flash"
    },
    {
      "id": "kmodel_latest",
      "name": "Kimi-K3"
    },
    {
      "id": "kmodel",
      "name": "Kimi-K2.7-Code"
    },
    {
      "id": "gmodel",
      "name": "GLM-5.3"
    },
    {
      "id": "gfmodel",
      "name": "GLM-5.3-Flash"
    },
    {
      "id": "dmodel",
      "name": "DeepSeek-V4-Pro"
    },
    {
      "id": "dfmodel",
      "name": "DeepSeek-V4-Flash"
    },
    {
      "id": "mmodel",
      "name": "MiniMax-M3"
    }
  ],
  "recraft": [
    {
      "id": "recraftv3",
      "name": "Recraft V3",
      "params": [
        "n",
        "size",
        "style"
      ],
      "kind": "image"
    },
    {
      "id": "recraftv2",
      "name": "Recraft V2",
      "params": [
        "n",
        "size",
        "style"
      ],
      "kind": "image"
    }
  ],
  "runwayml": [
    {
      "id": "gen4_image",
      "name": "Gen-4 Image",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "gen4_image_turbo",
      "name": "Gen-4 Image Turbo",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "gen4_turbo",
      "name": "Gen-4 Turbo",
      "params": [],
      "kind": "video"
    },
    {
      "id": "gen3a_turbo",
      "name": "Gen-3 Alpha Turbo",
      "params": [],
      "kind": "video"
    }
  ],
  "sdwebui": [
    {
      "id": "stable-diffusion-v1-5",
      "name": "Stable Diffusion v1.5",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "sdxl-base-1.0",
      "name": "SDXL Base 1.0",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    }
  ],
  "siliconflow": [
    {
      "id": "deepseek-ai/DeepSeek-V4-Pro",
      "name": "DeepSeek V4 Pro"
    },
    {
      "id": "deepseek-ai/DeepSeek-V4-Flash",
      "name": "DeepSeek V4 Flash"
    },
    {
      "id": "deepseek-ai/DeepSeek-V3.2",
      "name": "DeepSeek V3.2"
    },
    {
      "id": "deepseek-ai/DeepSeek-V3.2-Exp",
      "name": "DeepSeek V3.2 Exp"
    },
    {
      "id": "deepseek-ai/DeepSeek-V3.1",
      "name": "DeepSeek V3.1"
    },
    {
      "id": "deepseek-ai/DeepSeek-V3.1-Terminus",
      "name": "DeepSeek V3.1 Terminus"
    },
    {
      "id": "deepseek-ai/DeepSeek-R1",
      "name": "DeepSeek R1"
    },
    {
      "id": "Qwen/Qwen3.5-397B-A17B",
      "name": "Qwen 3.5 397B A17B"
    },
    {
      "id": "Qwen/Qwen3.5-122B-A10B",
      "name": "Qwen 3.5 122B A10B"
    },
    {
      "id": "zai-org/GLM-5.1",
      "name": "GLM 5.1"
    },
    {
      "id": "zai-org/GLM-5",
      "name": "GLM 5"
    },
    {
      "id": "moonshotai/Kimi-K2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "moonshotai/Kimi-K2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "openai/gpt-oss-120b",
      "name": "GPT OSS 120B"
    },
    {
      "id": "MiniMaxAI/MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "inclusionAI/Ling-flash-2.0",
      "name": "Ling Flash 2.0"
    }
  ],
  "stability-ai": [
    {
      "id": "stable-image-ultra",
      "name": "Stable Image Ultra",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "stable-image-core",
      "name": "Stable Image Core",
      "params": [
        "size",
        "style"
      ],
      "kind": "image"
    },
    {
      "id": "sd3.5-large",
      "name": "Stable Diffusion 3.5 Large",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "sd3.5-large-turbo",
      "name": "Stable Diffusion 3.5 Large Turbo",
      "params": [
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "sd3.5-medium",
      "name": "Stable Diffusion 3.5 Medium",
      "params": [
        "size"
      ],
      "kind": "image"
    }
  ],
  "together": [
    {
      "id": "meta-llama/Llama-3.3-70B-Instruct-Turbo",
      "name": "Llama 3.3 70B Turbo"
    },
    {
      "id": "deepseek-ai/DeepSeek-R1",
      "name": "DeepSeek R1"
    },
    {
      "id": "Qwen/Qwen3-235B-A22B",
      "name": "Qwen3 235B"
    },
    {
      "id": "meta-llama/Llama-4-Maverick-17B-128E-Instruct-FP8",
      "name": "Llama 4 Maverick"
    },
    {
      "id": "BAAI/bge-large-en-v1.5",
      "name": "BGE Large EN v1.5",
      "kind": "embedding"
    },
    {
      "id": "togethercomputer/m2-bert-80M-8k-retrieval",
      "name": "M2 BERT 80M 8K",
      "kind": "embedding"
    }
  ],
  "tortoise": [
    {
      "id": "tortoise-v2",
      "name": "Tortoise V2",
      "kind": "tts"
    }
  ],
  "venice": [
    {
      "id": "venice-uncensored-1-2",
      "name": "Venice Uncensored 1.2"
    },
    {
      "id": "zai-org-glm-5",
      "name": "GLM-5"
    },
    {
      "id": "qwen3-235b-a22b-instruct-2507",
      "name": "Qwen3 235B A22B Instruct"
    },
    {
      "id": "qwen3-coder-480b-a35b-instruct-turbo",
      "name": "Qwen3 Coder 480B A35B Turbo"
    },
    {
      "id": "qwen3-vl-235b-a22b",
      "name": "Qwen3 VL 235B A22B"
    },
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek V4 Pro"
    },
    {
      "id": "llama-3.3-70b",
      "name": "Llama 3.3 70B"
    },
    {
      "id": "hermes-3-llama-3.1-405b",
      "name": "Hermes 3 Llama 3.1 405B"
    },
    {
      "id": "mistral-small-3-2-24b-instruct",
      "name": "Mistral Small 3.2 24B"
    },
    {
      "id": "text-embedding-3-large",
      "name": "Text Embedding 3 Large",
      "kind": "embedding"
    },
    {
      "id": "text-embedding-bge-m3",
      "name": "BGE-M3 Embedding",
      "kind": "embedding"
    },
    {
      "id": "text-embedding-qwen3-8b",
      "name": "Qwen3 8B Embedding",
      "kind": "embedding"
    },
    {
      "id": "venice-sd35",
      "name": "Venice SD3.5",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "flux-2-pro",
      "name": "FLUX.2 Pro",
      "params": [
        "n",
        "size"
      ],
      "kind": "image"
    },
    {
      "id": "gpt-image-2",
      "name": "GPT Image 2 (via Venice)",
      "params": [
        "n",
        "size",
        "quality"
      ],
      "kind": "image"
    }
  ],
  "vertex-partner": [
    {
      "id": "deepseek-ai/deepseek-v3.2-maas",
      "name": "DeepSeek V3.2 (Vertex)"
    },
    {
      "id": "qwen/qwen3-next-80b-a3b-thinking-maas",
      "name": "Qwen3 Next 80B Thinking (Vertex)"
    },
    {
      "id": "qwen/qwen3-next-80b-a3b-instruct-maas",
      "name": "Qwen3 Next 80B Instruct (Vertex)"
    },
    {
      "id": "zai-org/glm-5-maas",
      "name": "GLM-5 (Vertex)"
    }
  ],
  "vertex": [
    {
      "id": "gemini-3.1-pro-preview",
      "name": "Gemini 3.1 Pro Preview"
    },
    {
      "id": "gemini-3.1-flash-lite-preview",
      "name": "Gemini 3.1 Flash Lite Preview"
    },
    {
      "id": "gemini-3-flash-preview",
      "name": "Gemini 3 Flash Preview"
    },
    {
      "id": "gemini-2.5-flash",
      "name": "Gemini 2.5 Flash"
    },
    {
      "id": "veo-3.1-generate-preview",
      "name": "Veo 3.1 (Preview)",
      "params": [
        "duration",
        "aspect_ratio",
        "resolution",
        "negative_prompt",
        "seed",
        "storage_uri",
        "generate_audio"
      ],
      "kind": "video"
    },
    {
      "id": "veo-3.1-fast-generate-preview",
      "name": "Veo 3.1 Fast (Preview)",
      "params": [
        "duration",
        "aspect_ratio",
        "resolution",
        "negative_prompt",
        "seed",
        "storage_uri",
        "generate_audio"
      ],
      "kind": "video"
    },
    {
      "id": "veo-3.0-generate-001",
      "name": "Veo 3",
      "params": [
        "duration",
        "aspect_ratio",
        "resolution",
        "negative_prompt",
        "seed",
        "storage_uri",
        "generate_audio"
      ],
      "kind": "video"
    },
    {
      "id": "veo-2.0-generate-001",
      "name": "Veo 2",
      "params": [
        "duration",
        "aspect_ratio",
        "negative_prompt",
        "seed",
        "storage_uri"
      ],
      "kind": "video"
    }
  ],
  "volcengine-ark": [
    {
      "id": "Doubao-Seed-2.0-Code",
      "name": "Doubao-Seed-2.0-Code"
    },
    {
      "id": "Doubao-Seed-2.0-pro",
      "name": "Doubao-Seed-2.0-pro"
    },
    {
      "id": "Doubao-Seed-2.0-lite",
      "name": "Doubao-Seed-2.0-lite"
    },
    {
      "id": "Doubao-Seed-Code",
      "name": "Doubao-Seed-Code"
    },
    {
      "id": "DeepSeek-V4-Flash",
      "name": "DeepSeek-V4-Flash"
    },
    {
      "id": "DeepSeek-V4-Pro",
      "name": "DeepSeek-V4-Pro"
    },
    {
      "id": "GLM-5.1",
      "name": "GLM-5.1"
    },
    {
      "id": "MiniMax-M2.7",
      "name": "MiniMax-M2.7"
    },
    {
      "id": "Kimi-K2.6",
      "name": "Kimi-K2.6"
    }
  ],
  "v1m": [
    {
      "id": "rev-latest",
      "name": "v1m Rev Latest (Calibrated)",
      "kind": "systemone"
    },
    {
      "id": "v1m-decision-engine",
      "name": "v1m Decision Engine",
      "kind": "systemone"
    }
  ],
  "voyage-ai": [
    {
      "id": "voyage-3-large",
      "name": "Voyage 3 Large",
      "kind": "embedding"
    },
    {
      "id": "voyage-3.5",
      "name": "Voyage 3.5",
      "kind": "embedding"
    },
    {
      "id": "voyage-3.5-lite",
      "name": "Voyage 3.5 Lite",
      "kind": "embedding"
    },
    {
      "id": "voyage-code-3",
      "name": "Voyage Code 3",
      "kind": "embedding"
    },
    {
      "id": "voyage-finance-2",
      "name": "Voyage Finance 2",
      "kind": "embedding"
    },
    {
      "id": "voyage-law-2",
      "name": "Voyage Law 2",
      "kind": "embedding"
    },
    {
      "id": "voyage-multilingual-2",
      "name": "Voyage Multilingual 2",
      "kind": "embedding"
    }
  ],
  "windsurf": [
    {
      "id": "swe-1.6-fast",
      "name": "SWE 1.6 Fast"
    },
    {
      "id": "swe-1.6",
      "name": "SWE 1.6"
    },
    {
      "id": "swe-1.5-fast",
      "name": "SWE 1.5 Fast"
    },
    {
      "id": "swe-1.5",
      "name": "SWE 1.5"
    },
    {
      "id": "claude-opus-4.7-max",
      "name": "Claude Opus 4.7 Max"
    },
    {
      "id": "claude-opus-4.7-xhigh",
      "name": "Claude Opus 4.7 XHigh"
    },
    {
      "id": "claude-opus-4.7-high",
      "name": "Claude Opus 4.7 High"
    },
    {
      "id": "claude-opus-4.7-medium",
      "name": "Claude Opus 4.7 Medium"
    },
    {
      "id": "claude-opus-4.7-low",
      "name": "Claude Opus 4.7 Low"
    },
    {
      "id": "claude-opus-4.7-review",
      "name": "Claude Opus 4.7 Review"
    },
    {
      "id": "claude-sonnet-4.6-thinking-1m",
      "name": "Claude Sonnet 4.6 Thinking 1M"
    },
    {
      "id": "claude-sonnet-4.6-1m",
      "name": "Claude Sonnet 4.6 1M"
    },
    {
      "id": "claude-sonnet-4.6-thinking",
      "name": "Claude Sonnet 4.6 Thinking"
    },
    {
      "id": "claude-sonnet-4.6",
      "name": "Claude Sonnet 4.6"
    },
    {
      "id": "claude-opus-4.6-thinking",
      "name": "Claude Opus 4.6 Thinking"
    },
    {
      "id": "claude-opus-4.6",
      "name": "Claude Opus 4.6"
    },
    {
      "id": "claude-opus-4.5-thinking",
      "name": "Claude Opus 4.5 Thinking"
    },
    {
      "id": "claude-opus-4.5",
      "name": "Claude Opus 4.5"
    },
    {
      "id": "claude-sonnet-4.5-thinking",
      "name": "Claude Sonnet 4.5 Thinking"
    },
    {
      "id": "claude-sonnet-4.5",
      "name": "Claude Sonnet 4.5"
    },
    {
      "id": "claude-haiku-4.5",
      "name": "Claude Haiku 4.5"
    },
    {
      "id": "gpt-5.5-xhigh-fast",
      "name": "GPT 5.5 XHigh Fast"
    },
    {
      "id": "gpt-5.5-xhigh",
      "name": "GPT 5.5 XHigh"
    },
    {
      "id": "gpt-5.5-high-fast",
      "name": "GPT 5.5 High Fast"
    },
    {
      "id": "gpt-5.5-high",
      "name": "GPT 5.5 High"
    },
    {
      "id": "gpt-5.5-medium-fast",
      "name": "GPT 5.5 Medium Fast"
    },
    {
      "id": "gpt-5.5-medium",
      "name": "GPT 5.5 Medium"
    },
    {
      "id": "gpt-5.5-low-fast",
      "name": "GPT 5.5 Low Fast"
    },
    {
      "id": "gpt-5.5-low",
      "name": "GPT 5.5 Low"
    },
    {
      "id": "gpt-5.5-none-fast",
      "name": "GPT 5.5 None Fast"
    },
    {
      "id": "gpt-5.5-none",
      "name": "GPT 5.5 None"
    },
    {
      "id": "gpt-5.4-xhigh-fast",
      "name": "GPT 5.4 XHigh Fast"
    },
    {
      "id": "gpt-5.4-xhigh",
      "name": "GPT 5.4 XHigh"
    },
    {
      "id": "gpt-5.4-high-fast",
      "name": "GPT 5.4 High Fast"
    },
    {
      "id": "gpt-5.4-high",
      "name": "GPT 5.4 High"
    },
    {
      "id": "gpt-5.4-medium-fast",
      "name": "GPT 5.4 Medium Fast"
    },
    {
      "id": "gpt-5.4-medium",
      "name": "GPT 5.4 Medium"
    },
    {
      "id": "gpt-5.4-low-fast",
      "name": "GPT 5.4 Low Fast"
    },
    {
      "id": "gpt-5.4-low",
      "name": "GPT 5.4 Low"
    },
    {
      "id": "gpt-5.4-none-fast",
      "name": "GPT 5.4 None Fast"
    },
    {
      "id": "gpt-5.4-none",
      "name": "GPT 5.4 None"
    },
    {
      "id": "gpt-5.4-mini-xhigh",
      "name": "GPT 5.4 Mini XHigh"
    },
    {
      "id": "gpt-5.4-mini-high",
      "name": "GPT 5.4 Mini High"
    },
    {
      "id": "gpt-5.4-mini-medium",
      "name": "GPT 5.4 Mini Medium"
    },
    {
      "id": "gpt-5.4-mini-low",
      "name": "GPT 5.4 Mini Low"
    },
    {
      "id": "gpt-5.3-codex-xhigh-fast",
      "name": "GPT 5.3 Codex XHigh Fast"
    },
    {
      "id": "gpt-5.3-codex-xhigh",
      "name": "GPT 5.3 Codex XHigh"
    },
    {
      "id": "gpt-5.3-codex-high-fast",
      "name": "GPT 5.3 Codex High Fast"
    },
    {
      "id": "gpt-5.3-codex-high",
      "name": "GPT 5.3 Codex High"
    },
    {
      "id": "gpt-5.3-codex-medium-fast",
      "name": "GPT 5.3 Codex Medium Fast"
    },
    {
      "id": "gpt-5.3-codex-medium",
      "name": "GPT 5.3 Codex Medium"
    },
    {
      "id": "gpt-5.3-codex-low-fast",
      "name": "GPT 5.3 Codex Low Fast"
    },
    {
      "id": "gpt-5.3-codex-low",
      "name": "GPT 5.3 Codex Low"
    },
    {
      "id": "gpt-5.2-xhigh",
      "name": "GPT 5.2 XHigh"
    },
    {
      "id": "gpt-5.2-high",
      "name": "GPT 5.2 High"
    },
    {
      "id": "gpt-5.2-medium",
      "name": "GPT 5.2 Medium"
    },
    {
      "id": "gpt-5.2-low",
      "name": "GPT 5.2 Low"
    },
    {
      "id": "gpt-5.2-none",
      "name": "GPT 5.2 None"
    },
    {
      "id": "gpt-5",
      "name": "GPT 5"
    },
    {
      "id": "gpt-4.1",
      "name": "GPT 4.1"
    },
    {
      "id": "gpt-4.1-mini",
      "name": "GPT 4.1 Mini"
    },
    {
      "id": "gpt-4.1-nano",
      "name": "GPT-4.1 Nano"
    },
    {
      "id": "gpt-4o",
      "name": "GPT-4o"
    },
    {
      "id": "gpt-4o-mini",
      "name": "GPT-4o Mini"
    },
    {
      "id": "gemini-3.1-pro-high",
      "name": "Gemini 3.1 Pro High"
    },
    {
      "id": "gemini-3.1-pro-low",
      "name": "Gemini 3.1 Pro Low"
    },
    {
      "id": "gemini-3.0-flash-high",
      "name": "Gemini 3.0 Flash High"
    },
    {
      "id": "gemini-3.0-flash-medium",
      "name": "Gemini 3.0 Flash Medium"
    },
    {
      "id": "gemini-3.0-flash-low",
      "name": "Gemini 3.0 Flash Low"
    },
    {
      "id": "gemini-3.0-flash-minimal",
      "name": "Gemini 3.0 Flash Minimal"
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro"
    },
    {
      "id": "deepseek-v4",
      "name": "DeepSeek V4"
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi K2.6"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1"
    }
  ],
  "xai": [
    {
      "id": "grok-4.6",
      "name": "Grok 4.6"
    },
    {
      "id": "grok-4.5",
      "name": "Grok 4.5"
    },
    {
      "id": "grok-4",
      "name": "Grok 4"
    },
    {
      "id": "grok-4-fast-reasoning",
      "name": "Grok 4 Fast Reasoning"
    },
    {
      "id": "grok-code-fast-1",
      "name": "Grok Code Fast"
    },
    {
      "id": "grok-3",
      "name": "Grok 3"
    },
    {
      "id": "grok-2-image-1212",
      "name": "Grok 2 Image",
      "params": [
        "n",
        "response_format"
      ],
      "kind": "image"
    },
    {
      "id": "grok-imagine-video",
      "name": "Grok Imagine Video",
      "params": [
        "duration",
        "aspect_ratio",
        "resolution"
      ],
      "kind": "video"
    }
  ],
  "xiaomi-mimo": [
    {
      "id": "mimo-x-pro-preview",
      "name": "MiMo-X-Pro-Preview",
      "upstreamModelId": "xiaomi/mimo-x-pro-preview",
      "supportedFormats": [
        "openai"
      ]
    },
    {
      "id": "mimo-x-flash-preview",
      "name": "MiMo-X-Flash-Preview",
      "upstreamModelId": "xiaomi/mimo-x-flash-preview",
      "supportedFormats": [
        "openai"
      ]
    },
    {
      "id": "mimo-v2.5-pro",
      "name": "MiMo V2.5 Pro"
    },
    {
      "id": "mimo-v2.5",
      "name": "MiMo V2.5"
    },
    {
      "id": "mimo-v2-omni",
      "name": "MiMo V2 Omni"
    },
    {
      "id": "mimo-v2-flash",
      "name": "MiMo V2 Flash"
    },
    {
      "id": "mimo-v2.5-tts",
      "name": "MiMo V2.5 TTS",
      "kind": "tts"
    }
  ],
  "xiaomi-tokenplan": [
    {
      "id": "mimo-v2.5-pro",
      "name": "MiMo V2.5 Pro"
    },
    {
      "id": "mimo-v2.5-pro-claude",
      "name": "MiMo V2.5 Pro (Claude Native)",
      "targetFormat": "claude",
      "upstreamModelId": "mimo-v2.5-pro"
    },
    {
      "id": "mimo-v2.5",
      "name": "MiMo V2.5"
    },
    {
      "id": "mimo-v2-pro",
      "name": "MiMo V2 Pro"
    },
    {
      "id": "mimo-v2-omni",
      "name": "MiMo V2 Omni"
    },
    {
      "id": "mimo-v2-tts",
      "name": "MiMo V2 TTS"
    },
    {
      "id": "mimo-v2.5-tts",
      "name": "MiMo V2.5 TTS"
    },
    {
      "id": "mimo-v2.5-tts-voiceclone",
      "name": "MiMo V2.5 TTS Voice Clone"
    },
    {
      "id": "mimo-v2.5-tts-voicedesign",
      "name": "MiMo V2.5 TTS Voice Design"
    }
  ],
  "alims-intl": [
    {
      "id": "qwen3.5-plus",
      "name": "Qwen3.5 Plus"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "glm-5",
      "name": "GLM 5"
    },
    {
      "id": "MiniMax-M2.5",
      "name": "MiniMax M2.5"
    },
    {
      "id": "qwen3-coder-next",
      "name": "Qwen3 Coder Next"
    },
    {
      "id": "qwen3-coder-plus",
      "name": "Qwen3 Coder Plus"
    },
    {
      "id": "glm-4.7",
      "name": "GLM 4.7"
    }
  ],
  "cbai": [
    {
      "id": "glm-5.2",
      "name": "GLM-5.2"
    },
    {
      "id": "glm-5.1",
      "name": "GLM-5.1"
    },
    {
      "id": "glm-5.0",
      "name": "GLM-5.0"
    },
    {
      "id": "glm-5.0-turbo",
      "name": "GLM-5.0-Turbo"
    },
    {
      "id": "glm-5v-turbo",
      "name": "GLM-5v-Turbo"
    },
    {
      "id": "glm-4.7",
      "name": "GLM-4.7"
    },
    {
      "id": "minimax-m3",
      "name": "MiniMax-M3"
    },
    {
      "id": "minimax-m2.7",
      "name": "MiniMax-M2.7"
    },
    {
      "id": "kimi-k2.7",
      "name": "Kimi-K2.7-Code"
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi-K2.6"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi-K2.5"
    },
    {
      "id": "hy3-preview",
      "name": "Hy3 Preview"
    },
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek-V4-Pro"
    },
    {
      "id": "deepseek-v4.1-flash",
      "name": "DeepSeek-V4.1-Flash"
    },
    {
      "id": "deepseek-v3-2-volc",
      "name": "DeepSeek-V3.2"
    }
  ],
  "zd": [],
  "af": [
    {
      "id": "gpt-oss-120b",
      "name": "GPT-OSS 120B (Free)",
      "contextLength": 131072
    },
    {
      "id": "gpt-oss-20b",
      "name": "GPT-OSS 20B (Free)",
      "contextLength": 131072
    },
    {
      "id": "kimi-k2.7-code",
      "name": "Kimi K2.7 Code (Free)",
      "contextLength": 262144
    }
  ],
  "qianfan": [
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek V4 Pro",
      "contextLength": 1048576
    },
    {
      "id": "deepseek-v4-flash",
      "name": "DeepSeek V4 Flash",
      "contextLength": 1048576
    },
    {
      "id": "glm-5.2",
      "name": "GLM 5.2",
      "contextLength": 512000
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1",
      "contextLength": 198000
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi K2.6",
      "contextLength": 262144
    },
    {
      "id": "qwen3.5-397b-a17b",
      "name": "Qwen 3.5 397B A17B",
      "contextLength": 262144
    },
    {
      "id": "qwen3.5-27b",
      "name": "Qwen 3.5 27B",
      "contextLength": 262144
    }
  ],
  "bzl": [
    {
      "id": "auto:free",
      "name": "Auto Free (Zero Cost)"
    },
    {
      "id": "claude-opus-4.7",
      "name": "Claude Opus 4.7",
      "contextLength": 1000000
    },
    {
      "id": "claude-sonnet-4.6",
      "name": "Claude Sonnet 4.6",
      "contextLength": 1000000
    },
    {
      "id": "claude-haiku-4.5",
      "name": "Claude Haiku 4.5",
      "contextLength": 200000
    },
    {
      "id": "gpt-5.5",
      "name": "GPT-5.5",
      "contextLength": 1050000
    },
    {
      "id": "gpt-5.4",
      "name": "GPT-5.4",
      "contextLength": 1050000
    },
    {
      "id": "gpt-5.4-mini",
      "name": "GPT-5.4 Mini",
      "contextLength": 400000
    },
    {
      "id": "gpt-5.4-nano",
      "name": "GPT-5.4 Nano",
      "contextLength": 400000
    },
    {
      "id": "grok-4.3",
      "name": "Grok 4.3",
      "contextLength": 1000000
    },
    {
      "id": "grok-4.20",
      "name": "Grok 4.20",
      "contextLength": 2000000
    },
    {
      "id": "gemini-3.1-pro-preview",
      "name": "Gemini 3.1 Pro",
      "contextLength": 1048576
    },
    {
      "id": "gemini-3-flash-preview",
      "name": "Gemini 3 Flash",
      "contextLength": 1048576
    },
    {
      "id": "gemini-3.1-flash-lite-preview",
      "name": "Gemini 3.1 Flash Lite",
      "contextLength": 1048576
    },
    {
      "id": "kimi-k2.6",
      "name": "Kimi K2.6",
      "contextLength": 262144
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5",
      "contextLength": 262144
    },
    {
      "id": "glm-5.1",
      "name": "GLM 5.1",
      "contextLength": 204800
    },
    {
      "id": "glm-5",
      "name": "GLM 5",
      "contextLength": 204800
    },
    {
      "id": "mimo-v2.5-pro",
      "name": "MiMo-V2.5-Pro",
      "contextLength": 1050000
    },
    {
      "id": "mimo-v2.5",
      "name": "MiMo-V2.5",
      "contextLength": 1050000
    },
    {
      "id": "minimax-m3",
      "name": "MiniMax M3",
      "contextLength": 1048576
    },
    {
      "id": "minimax-m2.7",
      "name": "MiniMax M2.7",
      "contextLength": 204800
    },
    {
      "id": "minimax-m2.5",
      "name": "MiniMax M2.5",
      "contextLength": 204800
    },
    {
      "id": "qwen3.6-plus",
      "name": "Qwen 3.6 Plus",
      "contextLength": 1000000
    },
    {
      "id": "nemotron-3-super-120b-a12b",
      "name": "Nemotron 3 Super",
      "contextLength": 1000000
    }
  ],
  "bm": [
    {
      "id": "gpt-4.1",
      "name": "GPT-4.1",
      "contextLength": 1048576
    },
    {
      "id": "gpt-4.1-mini",
      "name": "GPT-4.1 Mini",
      "contextLength": 1048576
    },
    {
      "id": "gpt-4.1-nano",
      "name": "GPT-4.1 Nano",
      "contextLength": 1048576
    },
    {
      "id": "claude-sonnet-4-5",
      "name": "Claude Sonnet 4.5",
      "contextLength": 200000
    },
    {
      "id": "claude-haiku-4-5",
      "name": "Claude Haiku 4.5",
      "contextLength": 200000
    },
    {
      "id": "gemini-2.0-flash",
      "name": "Gemini 2.0 Flash",
      "contextLength": 1048576
    },
    {
      "id": "gemini-2.0-flash-exp",
      "name": "Gemini 2.0 Flash (Exp)",
      "contextLength": 1048576
    },
    {
      "id": "qwen-turbo",
      "name": "Qwen Turbo",
      "contextLength": 1000000
    },
    {
      "id": "kimi-k2",
      "name": "Kimi K2",
      "contextLength": 262144
    },
    {
      "id": "kimi-k2-thinking",
      "name": "Kimi K2 Thinking",
      "contextLength": 262144
    },
    {
      "id": "glm-4.7",
      "name": "GLM 4.7",
      "contextLength": 204800
    },
    {
      "id": "minimax-m2.5",
      "name": "MiniMax M2.5",
      "contextLength": 204800
    },
    {
      "id": "claude-opus-4-5",
      "name": "Claude Opus 4.5 (VIP)",
      "contextLength": 200000
    },
    {
      "id": "gemini-2.5-pro",
      "name": "Gemini 2.5 Pro (VIP)",
      "contextLength": 1048576
    }
  ],
  "kgw": [
    {
      "id": "kilo-auto/free",
      "name": "Kilo Auto Free",
      "contextLength": 256000
    },
    {
      "id": "nvidia/nemotron-3-super-120b-a12b:free",
      "name": "Nemotron 3 Super 120B (Free)",
      "contextLength": 262144
    },
    {
      "id": "nvidia/nemotron-3-ultra-550b-a55b:free",
      "name": "Nemotron 3 Ultra 550B (Free)",
      "contextLength": 1000000
    },
    {
      "id": "kwaipilot/kat-coder-pro-v2.5:free",
      "name": "Kat Coder Pro v2.5 (Free)",
      "contextLength": 256000
    },
    {
      "id": "kilo-auto/frontier",
      "name": "Kilo Auto Frontier",
      "contextLength": 1000000
    },
    {
      "id": "kilo-auto/balanced",
      "name": "Kilo Auto Balanced",
      "contextLength": 1000000
    }
  ],
  "llm7": [
    {
      "id": "gpt-5.5",
      "name": "GPT-5.5 (LLM7)",
      "contextLength": 1050000
    },
    {
      "id": "claude-opus-5",
      "name": "Claude Opus 5 (LLM7)",
      "contextLength": 1000000
    },
    {
      "id": "deepseek-v4-flash",
      "name": "DeepSeek V4 Flash (LLM7)",
      "contextLength": 1000000
    },
    {
      "id": "grok-4.5",
      "name": "Grok 4.5 (LLM7)",
      "contextLength": 500000
    },
    {
      "id": "kimi-k3",
      "name": "Kimi K3 (LLM7)",
      "contextLength": 1000000
    }
  ],
  "samba": [
    {
      "id": "MiniMax-M2.7",
      "name": "MiniMax M2.7",
      "contextLength": 196608
    }
  ],
  "hunyuan": [
    {
      "id": "hunyuan-turbos-latest",
      "name": "Hunyuan TurboS Latest",
      "contextLength": 200000
    },
    {
      "id": "hunyuan-t1-latest",
      "name": "Hunyuan T1 Latest",
      "contextLength": 256000
    }
  ],
  "morph": [
    {
      "id": "morph-v3-large",
      "name": "Morph v3 Large"
    },
    {
      "id": "morph-v3-fast",
      "name": "Morph v3 Fast"
    },
    {
      "id": "morph-qwen35-397b",
      "name": "Qwen 3.5 397B (Morph)",
      "contextLength": 262144
    },
    {
      "id": "morph-minimax27-230b",
      "name": "MiniMax M2.7 (Morph)",
      "contextLength": 200704
    },
    {
      "id": "morph-qwen36-27b",
      "name": "Qwen 3.6 27B (Morph)",
      "contextLength": 262144
    },
    {
      "id": "morph-dsv4flash",
      "name": "DeepSeek V4 Flash (Morph)",
      "contextLength": 1048576
    }
  ],
  "poolside": [
    {
      "id": "poolside/laguna-s-2.1",
      "name": "Laguna S 2.1"
    },
    {
      "id": "poolside/laguna-xs-2.1",
      "name": "Laguna XS 2.1"
    }
  ],
  "tokenrouter": [
    {
      "id": "anthropic/claude-haiku-4.5",
      "name": "Claude Haiku 4.5"
    },
    {
      "id": "anthropic/claude-sonnet-4.6",
      "name": "Claude Sonnet 4.6"
    },
    {
      "id": "anthropic/claude-opus-4.8",
      "name": "Claude Opus 4.8"
    },
    {
      "id": "anthropic/claude-opus-4.8-fast",
      "name": "Claude Opus 4.8 Fast"
    },
    {
      "id": "openai/gpt-5.4",
      "name": "Gpt 5.4"
    },
    {
      "id": "openai/gpt-5.4-mini",
      "name": "Gpt 5.4 Mini"
    },
    {
      "id": "openai/gpt-5.4-pro",
      "name": "Gpt 5.4 Pro"
    },
    {
      "id": "openai/gpt-5.5",
      "name": "Gpt 5.5"
    },
    {
      "id": "openai/gpt-5.6-sol",
      "name": "Gpt 5.6 Sol"
    },
    {
      "id": "google/gemini-3.5-flash",
      "name": "Gemini 3.5 Flash"
    },
    {
      "id": "google/gemini-3.6-flash",
      "name": "Gemini 3.6 Flash"
    },
    {
      "id": "deepseek/deepseek-v4-flash",
      "name": "Deepseek V4 Flash"
    },
    {
      "id": "deepseek/deepseek-v4-pro",
      "name": "Deepseek V4 Pro"
    },
    {
      "id": "qwen/qwen3-coder-next",
      "name": "Qwen3 Coder Next"
    },
    {
      "id": "qwen/qwen3.7-max",
      "name": "Qwen3.7 Max"
    },
    {
      "id": "qwen/qwen3.8-max",
      "name": "Qwen3.8 Max"
    },
    {
      "id": "moonshotai/kimi-k2.7-code",
      "name": "Kimi K2.7 Code"
    },
    {
      "id": "moonshotai/kimi-k3-free",
      "name": "Kimi K3 Free"
    },
    {
      "id": "z-ai/glm-5.3-free",
      "name": "Glm 5.3 Free"
    },
    {
      "id": "z-ai/glm-5.2",
      "name": "Glm 5.2"
    },
    {
      "id": "z-ai/glm-5-turbo",
      "name": "Glm 5 Turbo"
    },
    {
      "id": "x-ai/grok-4.5",
      "name": "Grok 4.5"
    }
  ],
  "trae": [
    {
      "id": "auto",
      "name": "Auto"
    },
    {
      "id": "work",
      "name": "Work"
    },
    {
      "id": "gemini-3.1-pro",
      "name": "Gemini 3.1 Pro"
    },
    {
      "id": "gemini-3-flash-solo",
      "name": "Gemini 3 Flash Solo"
    },
    {
      "id": "minimax-m3",
      "name": "Minimax M3"
    },
    {
      "id": "minimax-m2.7",
      "name": "Minimax M2.7"
    },
    {
      "id": "kimi-k2.5",
      "name": "Kimi K2.5"
    },
    {
      "id": "gpt-5.4",
      "name": "GPT 5.4"
    },
    {
      "id": "gpt-5.2",
      "name": "GPT 5.2"
    }
  ],
  "selfhosted-stt": [
    {
      "id": "whisper-1",
      "name": "Whisper (self-hosted)",
      "params": [
        "language",
        "response_format",
        "temperature",
        "prompt"
      ],
      "kind": "stt"
    }
  ],
  "selfhosted-tts": [
    {
      "id": "kokoro",
      "name": "Kokoro (self-hosted)",
      "params": [
        "voice",
        "response_format",
        "speed"
      ],
      "kind": "tts"
    }
  ],
  "selfhosted-embedding": [
    {
      "id": "embedding",
      "name": "Self-hosted embedding model",
      "kind": "embedding"
    }
  ],
  "alitp-intl": [
    {
      "id": "qwen3.8-max-preview",
      "name": "Qwen3.8 Max Preview"
    },
    {
      "id": "qwen3.7-max",
      "name": "Qwen3.7 Max"
    },
    {
      "id": "qwen3.7-plus",
      "name": "Qwen3.7 Plus"
    },
    {
      "id": "qwen3.6-flash",
      "name": "Qwen3.6 Flash"
    },
    {
      "id": "glm-5.2",
      "name": "GLM 5.2"
    },
    {
      "id": "deepseek-v4-pro",
      "name": "DeepSeek V4 Pro"
    }
  ],
  "openai-tts-models": [
    {
      "id": "gpt-4o-mini-tts",
      "name": "GPT-4o Mini TTS",
      "type": "tts"
    },
    {
      "id": "tts-1-hd",
      "name": "TTS-1 HD",
      "type": "tts"
    },
    {
      "id": "tts-1",
      "name": "TTS-1",
      "type": "tts"
    }
  ],
  "openai-tts-voices": [
    {
      "id": "alloy",
      "name": "Alloy",
      "type": "tts"
    },
    {
      "id": "ash",
      "name": "Ash",
      "type": "tts"
    },
    {
      "id": "ballad",
      "name": "Ballad",
      "type": "tts"
    },
    {
      "id": "cedar",
      "name": "Cedar",
      "type": "tts"
    },
    {
      "id": "coral",
      "name": "Coral",
      "type": "tts"
    },
    {
      "id": "echo",
      "name": "Echo",
      "type": "tts"
    },
    {
      "id": "fable",
      "name": "Fable",
      "type": "tts"
    },
    {
      "id": "marin",
      "name": "Marin",
      "type": "tts"
    },
    {
      "id": "nova",
      "name": "Nova",
      "type": "tts"
    },
    {
      "id": "onyx",
      "name": "Onyx",
      "type": "tts"
    },
    {
      "id": "sage",
      "name": "Sage",
      "type": "tts"
    },
    {
      "id": "shimmer",
      "name": "Shimmer",
      "type": "tts"
    },
    {
      "id": "verse",
      "name": "Verse",
      "type": "tts"
    }
  ],
  "openrouter-tts-models": [
    {
      "id": "openai/gpt-4o-mini-tts",
      "name": "GPT-4o Mini TTS",
      "type": "tts"
    },
    {
      "id": "openai/tts-1-hd",
      "name": "TTS-1 HD",
      "type": "tts"
    },
    {
      "id": "openai/tts-1",
      "name": "TTS-1",
      "type": "tts"
    }
  ],
  "openrouter-tts-voices": [
    {
      "id": "alloy",
      "name": "Alloy",
      "type": "tts"
    },
    {
      "id": "ash",
      "name": "Ash",
      "type": "tts"
    },
    {
      "id": "ballad",
      "name": "Ballad",
      "type": "tts"
    },
    {
      "id": "cedar",
      "name": "Cedar",
      "type": "tts"
    },
    {
      "id": "coral",
      "name": "Coral",
      "type": "tts"
    },
    {
      "id": "echo",
      "name": "Echo",
      "type": "tts"
    },
    {
      "id": "fable",
      "name": "Fable",
      "type": "tts"
    },
    {
      "id": "marin",
      "name": "Marin",
      "type": "tts"
    },
    {
      "id": "nova",
      "name": "Nova",
      "type": "tts"
    },
    {
      "id": "onyx",
      "name": "Onyx",
      "type": "tts"
    },
    {
      "id": "sage",
      "name": "Sage",
      "type": "tts"
    },
    {
      "id": "shimmer",
      "name": "Shimmer",
      "type": "tts"
    },
    {
      "id": "verse",
      "name": "Verse",
      "type": "tts"
    }
  ],
  "elevenlabs": [
    {
      "id": "eleven_multilingual_v2",
      "name": "Eleven Multilingual v2",
      "kind": "tts"
    },
    {
      "id": "eleven_turbo_v2_5",
      "name": "Eleven Turbo v2.5",
      "kind": "tts"
    }
  ],
  "elevenlabs-tts-models": [
    {
      "id": "eleven_flash_v2_5",
      "name": "Flash v2.5 (Fastest)",
      "type": "tts"
    },
    {
      "id": "eleven_turbo_v2_5",
      "name": "Turbo v2.5 (Fast)",
      "type": "tts"
    },
    {
      "id": "eleven_multilingual_v2",
      "name": "Multilingual v2 (Quality)",
      "type": "tts"
    },
    {
      "id": "eleven_monolingual_v1",
      "name": "Monolingual v1 (English)",
      "type": "tts"
    }
  ],
  "edge-tts": [
    {
      "id": "en-US-AriaNeural",
      "name": "Aria (en-US)",
      "type": "tts"
    },
    {
      "id": "en-US-GuyNeural",
      "name": "Guy (en-US)",
      "type": "tts"
    },
    {
      "id": "en-GB-SoniaNeural",
      "name": "Sonia (en-GB)",
      "type": "tts"
    },
    {
      "id": "vi-VN-HoaiMyNeural",
      "name": "Hoai My (vi-VN)",
      "type": "tts"
    },
    {
      "id": "vi-VN-NamMinhNeural",
      "name": "Nam Minh (vi-VN)",
      "type": "tts"
    },
    {
      "id": "zh-CN-XiaoxiaoNeural",
      "name": "Xiaoxiao (zh-CN)",
      "type": "tts"
    },
    {
      "id": "zh-CN-YunxiNeural",
      "name": "Yunxi (zh-CN)",
      "type": "tts"
    },
    {
      "id": "fr-FR-DeniseNeural",
      "name": "Denise (fr-FR)",
      "type": "tts"
    },
    {
      "id": "de-DE-KatjaNeural",
      "name": "Katja (de-DE)",
      "type": "tts"
    },
    {
      "id": "ja-JP-NanamiNeural",
      "name": "Nanami (ja-JP)",
      "type": "tts"
    },
    {
      "id": "ko-KR-SunHiNeural",
      "name": "SunHi (ko-KR)",
      "type": "tts"
    }
  ],
  "local-device": [
    {
      "id": "default",
      "name": "System Default Voice",
      "type": "tts"
    }
  ],
  "google-tts": [
    {
      "id": "af",
      "name": "Afrikaans",
      "type": "tts"
    },
    {
      "id": "ar",
      "name": "Arabic",
      "type": "tts"
    },
    {
      "id": "bg",
      "name": "Bulgarian",
      "type": "tts"
    },
    {
      "id": "bn",
      "name": "Bengali",
      "type": "tts"
    },
    {
      "id": "bs",
      "name": "Bosnian",
      "type": "tts"
    },
    {
      "id": "ca",
      "name": "Catalan",
      "type": "tts"
    },
    {
      "id": "cs",
      "name": "Czech",
      "type": "tts"
    },
    {
      "id": "cy",
      "name": "Welsh",
      "type": "tts"
    },
    {
      "id": "da",
      "name": "Danish",
      "type": "tts"
    },
    {
      "id": "de",
      "name": "German",
      "type": "tts"
    },
    {
      "id": "el",
      "name": "Greek",
      "type": "tts"
    },
    {
      "id": "en",
      "name": "English",
      "type": "tts"
    },
    {
      "id": "eo",
      "name": "Esperanto",
      "type": "tts"
    },
    {
      "id": "es",
      "name": "Spanish",
      "type": "tts"
    },
    {
      "id": "et",
      "name": "Estonian",
      "type": "tts"
    },
    {
      "id": "fi",
      "name": "Finnish",
      "type": "tts"
    },
    {
      "id": "fr",
      "name": "French",
      "type": "tts"
    },
    {
      "id": "gu",
      "name": "Gujarati",
      "type": "tts"
    },
    {
      "id": "hi",
      "name": "Hindi",
      "type": "tts"
    },
    {
      "id": "hr",
      "name": "Croatian",
      "type": "tts"
    },
    {
      "id": "hu",
      "name": "Hungarian",
      "type": "tts"
    },
    {
      "id": "hy",
      "name": "Armenian",
      "type": "tts"
    },
    {
      "id": "id",
      "name": "Indonesian",
      "type": "tts"
    },
    {
      "id": "is",
      "name": "Icelandic",
      "type": "tts"
    },
    {
      "id": "it",
      "name": "Italian",
      "type": "tts"
    },
    {
      "id": "ja",
      "name": "Japanese",
      "type": "tts"
    },
    {
      "id": "jw",
      "name": "Javanese",
      "type": "tts"
    },
    {
      "id": "km",
      "name": "Khmer",
      "type": "tts"
    },
    {
      "id": "kn",
      "name": "Kannada",
      "type": "tts"
    },
    {
      "id": "ko",
      "name": "Korean",
      "type": "tts"
    },
    {
      "id": "la",
      "name": "Latin",
      "type": "tts"
    },
    {
      "id": "lv",
      "name": "Latvian",
      "type": "tts"
    },
    {
      "id": "mk",
      "name": "Macedonian",
      "type": "tts"
    },
    {
      "id": "ml",
      "name": "Malayalam",
      "type": "tts"
    },
    {
      "id": "mr",
      "name": "Marathi",
      "type": "tts"
    },
    {
      "id": "my",
      "name": "Myanmar (Burmese)",
      "type": "tts"
    },
    {
      "id": "ne",
      "name": "Nepali",
      "type": "tts"
    },
    {
      "id": "nl",
      "name": "Dutch",
      "type": "tts"
    },
    {
      "id": "no",
      "name": "Norwegian",
      "type": "tts"
    },
    {
      "id": "pl",
      "name": "Polish",
      "type": "tts"
    },
    {
      "id": "pt",
      "name": "Portuguese",
      "type": "tts"
    },
    {
      "id": "ro",
      "name": "Romanian",
      "type": "tts"
    },
    {
      "id": "ru",
      "name": "Russian",
      "type": "tts"
    },
    {
      "id": "si",
      "name": "Sinhala",
      "type": "tts"
    },
    {
      "id": "sk",
      "name": "Slovak",
      "type": "tts"
    },
    {
      "id": "sq",
      "name": "Albanian",
      "type": "tts"
    },
    {
      "id": "sr",
      "name": "Serbian",
      "type": "tts"
    },
    {
      "id": "su",
      "name": "Sundanese",
      "type": "tts"
    },
    {
      "id": "sv",
      "name": "Swedish",
      "type": "tts"
    },
    {
      "id": "sw",
      "name": "Swahili",
      "type": "tts"
    },
    {
      "id": "ta",
      "name": "Tamil",
      "type": "tts"
    },
    {
      "id": "te",
      "name": "Telugu",
      "type": "tts"
    },
    {
      "id": "th",
      "name": "Thai",
      "type": "tts"
    },
    {
      "id": "tl",
      "name": "Filipino",
      "type": "tts"
    },
    {
      "id": "tr",
      "name": "Turkish",
      "type": "tts"
    },
    {
      "id": "uk",
      "name": "Ukrainian",
      "type": "tts"
    },
    {
      "id": "ur",
      "name": "Urdu",
      "type": "tts"
    },
    {
      "id": "vi",
      "name": "Vietnamese",
      "type": "tts"
    },
    {
      "id": "zh-CN",
      "name": "Chinese (Simplified)",
      "type": "tts"
    },
    {
      "id": "zh-TW",
      "name": "Chinese (Traditional)",
      "type": "tts"
    }
  ],
  "gemini-tts-models": [
    {
      "id": "gemini-3.1-flash-tts-preview",
      "name": "Gemini 3.1 Flash TTS",
      "type": "tts"
    },
    {
      "id": "gemini-2.5-flash-preview-tts",
      "name": "Gemini 2.5 Flash TTS",
      "type": "tts"
    },
    {
      "id": "gemini-2.5-pro-preview-tts",
      "name": "Gemini 2.5 Pro TTS",
      "type": "tts"
    }
  ],
  "gemini-tts-voices": [
    {
      "id": "Zephyr",
      "name": "Zephyr",
      "type": "tts"
    },
    {
      "id": "Puck",
      "name": "Puck",
      "type": "tts"
    },
    {
      "id": "Charon",
      "name": "Charon",
      "type": "tts"
    },
    {
      "id": "Kore",
      "name": "Kore",
      "type": "tts"
    },
    {
      "id": "Fenrir",
      "name": "Fenrir",
      "type": "tts"
    },
    {
      "id": "Leda",
      "name": "Leda",
      "type": "tts"
    },
    {
      "id": "Orus",
      "name": "Orus",
      "type": "tts"
    },
    {
      "id": "Aoede",
      "name": "Aoede",
      "type": "tts"
    },
    {
      "id": "Callirrhoe",
      "name": "Callirrhoe",
      "type": "tts"
    },
    {
      "id": "Autonoe",
      "name": "Autonoe",
      "type": "tts"
    },
    {
      "id": "Enceladus",
      "name": "Enceladus",
      "type": "tts"
    },
    {
      "id": "Iapetus",
      "name": "Iapetus",
      "type": "tts"
    },
    {
      "id": "Umbriel",
      "name": "Umbriel",
      "type": "tts"
    },
    {
      "id": "Algieba",
      "name": "Algieba",
      "type": "tts"
    },
    {
      "id": "Despina",
      "name": "Despina",
      "type": "tts"
    },
    {
      "id": "Erinome",
      "name": "Erinome",
      "type": "tts"
    },
    {
      "id": "Algenib",
      "name": "Algenib",
      "type": "tts"
    },
    {
      "id": "Rasalgethi",
      "name": "Rasalgethi",
      "type": "tts"
    },
    {
      "id": "Laomedeia",
      "name": "Laomedeia",
      "type": "tts"
    },
    {
      "id": "Achernar",
      "name": "Achernar",
      "type": "tts"
    },
    {
      "id": "Alnilam",
      "name": "Alnilam",
      "type": "tts"
    },
    {
      "id": "Schedar",
      "name": "Schedar",
      "type": "tts"
    },
    {
      "id": "Gacrux",
      "name": "Gacrux",
      "type": "tts"
    },
    {
      "id": "Pulcherrima",
      "name": "Pulcherrima",
      "type": "tts"
    },
    {
      "id": "Achird",
      "name": "Achird",
      "type": "tts"
    },
    {
      "id": "Zubenelgenubi",
      "name": "Zubenelgenubi",
      "type": "tts"
    },
    {
      "id": "Vindemiatrix",
      "name": "Vindemiatrix",
      "type": "tts"
    },
    {
      "id": "Sadachbia",
      "name": "Sadachbia",
      "type": "tts"
    },
    {
      "id": "Sadaltager",
      "name": "Sadaltager",
      "type": "tts"
    },
    {
      "id": "Sulafat",
      "name": "Sulafat",
      "type": "tts"
    }
  ],
  "xiaomi-mimo-tts-models": [
    {
      "id": "mimo-v2.5-tts",
      "name": "MiMo V2.5 TTS",
      "type": "tts"
    }
  ],
};
export const PROVIDER_MODELS = BUILTIN_MODELS_BY_PROVIDER;

export function getModelsByProviderId(providerId: string): ProviderModel[] {
  const alias = PROVIDER_ID_TO_ALIAS[providerId] || providerId;
  // PROVIDER_MODELS is keyed by the provider id (upstream keys it by the registry
  // `alias || id`, which equals the id for every non-OAuth entry), while
  // PROVIDER_ID_TO_ALIAS holds the short *display* prefix (uiAlias) used to render
  // `cmc/<model>`. The two only coincide for some providers, so resolve by id first
  // and fall back to the alias instead of returning an empty catalog.
  return PROVIDER_MODELS[providerId] || PROVIDER_MODELS[alias] || [];
}

export function getModelKind(m: unknown, fallback = "llm"): string {
  if (m && typeof m === "object") {
    const obj = m as { kind?: unknown; type?: unknown };
    if (typeof obj.kind === "string" && obj.kind) return obj.kind;
    if (typeof obj.type === "string" && obj.type) return obj.type;
  }
  return fallback;
}

export function getModelCaps(modelId: string, modelObj?: unknown): { vision: boolean; audioInput: boolean; reasoning: boolean } {
  const idLower = (modelId || "").toLowerCase();
  const obj = modelObj && typeof modelObj === "object" ? (modelObj as Record<string, unknown>) : null;
  const nameLower = typeof obj?.name === "string" ? obj.name.toLowerCase() : "";
  const rawCaps = Array.isArray(obj?.capabilities) ? obj.capabilities : [];
  const caps: string[] = rawCaps.filter((c): c is string => typeof c === "string");
  const upstreamLower = typeof obj?.upstreamModelId === "string" ? obj.upstreamModelId.toLowerCase() : "";
  const kind = typeof obj?.kind === "string" ? obj.kind : "";
  const type = typeof obj?.type === "string" ? obj.type : "";

  // Words that indicate non-vision/media generation or embedding (upstream visionPatterns.js)
  const isNotVision =
    idLower.includes("stable-image") ||
    idLower.includes("nanobanana") ||
    idLower.includes("flux") ||
    idLower.includes("dall") ||
    idLower.includes("sdxl") ||
    idLower.includes("diffusion") ||
    idLower.includes("embed") ||
    idLower.includes("rerank") ||
    idLower.includes("tts") ||
    idLower.includes("stt") ||
    idLower.includes("whisper") ||
    kind === "image" ||
    kind === "tts" ||
    kind === "stt" ||
    kind === "embedding";

  // Vision detection mirroring upstream capabilities.js & visionPatterns.js
  // (bare "flash" is speed-only, not a vision flag!)
  let vision = false;
  if (!isNotVision) {
    vision =
      caps.includes("vision") ||
      caps.includes("image") ||
      idLower.includes("vision") ||
      idLower.includes("vl") ||
      idLower.includes("vlm") ||
      idLower.includes("multimodal") ||
      idLower.includes("omni") ||
      idLower.includes("visual") ||
      idLower.includes("gemini") ||
      idLower.includes("gemma") ||
      idLower.includes("4o") ||
      idLower.includes("gpt-4.1") ||
      idLower.includes("gpt-5") ||
      idLower.includes("gpt-6") ||
      idLower.includes("opus") ||
      idLower.includes("sonnet") ||
      idLower.includes("haiku-4.5") ||
      idLower.includes("fable") ||
      idLower.includes("kimi-k2") ||
      idLower.includes("kimi-k3") ||
      idLower.includes("minimax-m2.7") ||
      idLower.includes("minimax-m3") ||
      idLower.includes("mimo-v2.5") ||
      idLower.includes("mimo-v2.6") ||
      idLower.includes("mimo-omni") ||
      idLower.includes("qwen3.5") ||
      idLower.includes("qwen3.7") ||
      idLower.includes("qwen3.8") ||
      idLower.includes("grok") ||
      idLower.includes("llama-4") ||
      idLower.includes("muse-spark");
  }

  let audioInput =
    caps.includes("audio") ||
    caps.includes("audioInput") ||
    idLower.includes("audio-in") ||
    idLower.includes("audioinput") ||
    kind === "stt" ||
    type === "stt";

  let reasoning =
    caps.includes("reasoning") ||
    caps.includes("thinking") ||
    obj?.thinking === true ||
    idLower.includes("thinking") ||
    idLower.includes("reasoning") ||
    idLower.includes("r1") ||
    idLower.includes("o1") ||
    idLower.includes("o3") ||
    idLower.includes("tiered") ||
    idLower.includes("high") ||
    idLower.includes("medium") ||
    idLower.includes("low") ||
    nameLower.includes("thinking") ||
    nameLower.includes("high") ||
    nameLower.includes("medium") ||
    nameLower.includes("low") ||
    upstreamLower.includes("tiered") ||
    upstreamLower.includes("high") ||
    upstreamLower.includes("medium") ||
    upstreamLower.includes("low");

  if (obj?.thinking === false) {
    reasoning = false;
  }

  // Explicit capability flags saved from the Add Custom Model modal
  // (upstream `caps` object) override the id/name heuristics above.
  const objCaps = obj?.caps && typeof obj.caps === "object" ? (obj.caps as Record<string, unknown>) : null;
  if (objCaps) {
    if (objCaps.vision === true) vision = true;
    if (objCaps.vision === false) vision = false;
    if (objCaps.audioInput === true) audioInput = true;
    if (objCaps.audioInput === false) audioInput = false;
    if (objCaps.reasoning === true) reasoning = true;
    if (objCaps.reasoning === false) reasoning = false;
  }
  return { vision, audioInput, reasoning };
}
