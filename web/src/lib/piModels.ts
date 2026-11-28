// Pi model entries for the `9router` provider in ~/.pi/agent/models.json.
//
// Port of upstream `src/shared/utils/piModels.js`.

export const PI_PROVIDER_KEY = '9router'

export const PI_CONTEXT_WINDOWS = [200000, 400000, 600000, 1000000]
export const PI_MAX_TOKENS = [16000, 32000, 64000, 128000]

export const PI_THINKING_LEVELS = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
export const PI_ALL_THINKING_LEVELS = PI_THINKING_LEVELS

export const PI_DEFAULT_THINKING_LEVELS = ['off', 'minimal', 'low', 'medium', 'high']

export const PI_LEVEL_VALUES: Record<string, string> = { off: 'none' }

export const piLevelValue = (level: string) => PI_LEVEL_VALUES[level] ?? level

export const PI_INPUT_VALUES = ['text', 'image']

export const PI_DEFAULT_CAPS = {
  contextWindow: 200000,
  maxTokens: 64000,
  input: ['text'],
  reasoning: false,
  thinkingLevels: null as string[] | null,
}
export const PI_CAP_LIMITS = { contextWindow: 2000000, maxTokens: 200000 }

export const PI_DEFAULT_CONTEXT_WINDOW = PI_DEFAULT_CAPS.contextWindow
export const PI_DEFAULT_MAX_TOKENS = PI_DEFAULT_CAPS.maxTokens
export const PI_MAX_CONTEXT_WINDOW = PI_CAP_LIMITS.contextWindow
export const PI_MAX_MAX_TOKENS = PI_CAP_LIMITS.maxTokens

export const formatTokens = (n: number) =>
  n >= 1000000 ? `${+(n / 1000000).toFixed(2)}M` : `${Math.round(n / 1000)}k`

export const sanitizeCap = (v: unknown, fallback: number, max: number) => {
  const n = Number(v)
  return Number.isFinite(n) && n > 0 && n <= max ? n : fallback
}

export const capOptions = (values: number[], current: number) =>
  values.includes(current) ? values : [...values, current].sort((a, b) => a - b)

export const piModelCaps = (caps: unknown) => ({ ...PI_DEFAULT_CAPS, ...((typeof caps === "object" && caps ? caps : {}) as Record<string, unknown>) })

export const readThinkingLevels = (map: unknown): string[] => {
  if (!map || typeof map !== 'object' || Array.isArray(map)) return [...PI_DEFAULT_THINKING_LEVELS]

  const m = map as Record<string, unknown>
  return PI_THINKING_LEVELS.filter((level) => {
    const mapped = m[level]
    if (mapped === null) return false
    if (level === 'xhigh' || level === 'max') return mapped !== undefined
    return true
  })
}

export const levelsFromServer = (raw: unknown): string[] | null => {
  if (!Array.isArray(raw) || raw.length === 0) return null

  const levels: string[] = []
  for (const value of raw) {
    if (typeof value !== 'string') return null
    const level = value === 'none' ? 'off' : value
    if (!PI_THINKING_LEVELS.includes(level)) return null
    levels.push(level)
  }

  return PI_THINKING_LEVELS.filter((level) => levels.includes(level))
}

export const thinkingLevelMapFromLevels = (levels: string[]) =>
  Object.fromEntries(
    PI_THINKING_LEVELS.map((level) => [level, levels.includes(level) ? piLevelValue(level) : null])
  )

export const capsFromModelInfo = (info: unknown) => {
  const i = (info ?? {}) as Record<string, unknown>
  return {
    ...PI_DEFAULT_CAPS,
    thinkingLevels: levelsFromServer(i.thinking_levels),
    contextWindow: sanitizeCap(i.contextWindow, PI_DEFAULT_CAPS.contextWindow, PI_MAX_CONTEXT_WINDOW),
    maxTokens: sanitizeCap(i.maxOutput, PI_DEFAULT_CAPS.maxTokens, PI_MAX_MAX_TOKENS),
    input: i.vision ? ['text', 'image'] : ['text'],
    reasoning: i.reasoning === true,
  }
}

export const piInputHasImage = (caps: unknown) => {
  const c = (caps ?? {}) as Record<string, unknown>
  return Array.isArray(c.input) && (c.input as unknown[]).includes('image')
}

export const buildPiModelEntry = (id: string, caps: unknown = {}) => {
  const merged = piModelCaps(caps) as Record<string, unknown>
  const entry: Record<string, unknown> = {
    id,
    name: id,
    contextWindow: merged.contextWindow,
    maxTokens: merged.maxTokens,
    input: piInputHasImage(merged) ? ['text', 'image'] : ['text'],
    reasoning: merged.reasoning === true,
  }

  if (merged.reasoning !== true) {
    entry.thinkingLevelMap = {}
  } else if (Array.isArray(merged.thinkingLevels)) {
    entry.thinkingLevelMap = thinkingLevelMapFromLevels(merged.thinkingLevels)
  }

  return entry
}

export const readPiModels = (cfg: unknown) => {
  const c = (cfg ?? {}) as Record<string, unknown>
  const providers = (c.providers ?? {}) as Record<string, unknown>
  const block = (providers[PI_PROVIDER_KEY] ?? {}) as Record<string, unknown>
  const list = block.models
  if (!Array.isArray(list) || list.length === 0) return null

  const caps: Record<string, unknown> = {}
  const ids = list.map((entry: unknown) => {
    const e = (typeof entry === 'string' ? null : entry) as Record<string, unknown> | null
    const id = typeof entry === 'string' ? entry : e?.id
    if (typeof entry !== 'string') {
      caps[id as string] = {
        contextWindow: sanitizeCap(e?.contextWindow, PI_DEFAULT_CONTEXT_WINDOW, PI_MAX_CONTEXT_WINDOW),
        maxTokens: sanitizeCap(e?.maxTokens, PI_DEFAULT_MAX_TOKENS, PI_MAX_MAX_TOKENS),
        input: piInputHasImage(e) ? ['text', 'image'] : ['text'],
        reasoning: e?.reasoning === true,
        thinkingLevels: readThinkingLevels(e?.thinkingLevelMap),
      }
    }
    return id
  })

  return { ids, caps }
}

export const sanitizePiInput = (input: unknown) => {
  if (!Array.isArray(input)) return undefined
  const values = [...new Set(input.filter((value) => typeof value === "string" && PI_INPUT_VALUES.includes(value)))]
  return values.length > 0 ? values : undefined
}

export const sanitizePiThinkingLevelMap = (map: unknown) => {
  if (!map || typeof map !== 'object' || Array.isArray(map)) return undefined

  const m = map as Record<string, unknown>
  const clean: Record<string, unknown> = {}
  for (const level of PI_ALL_THINKING_LEVELS) {
    const value = m[level]
    if (typeof value === 'string' || value === null) clean[level] = value
  }

  return Object.keys(clean).length > 0 ? clean : undefined
}

export const sanitizePiModelCaps = (raw: unknown) => {
  const r = (raw ?? {}) as Record<string, unknown>
  const clean: Record<string, unknown> = {}

  const input = sanitizePiInput(r.input)
  if (input) clean.input = input

  if (typeof r.reasoning === 'boolean') clean.reasoning = r.reasoning

  const thinkingLevelMap = sanitizePiThinkingLevelMap(r.thinkingLevelMap)
  if (thinkingLevelMap) clean.thinkingLevelMap = thinkingLevelMap

  return clean
}
