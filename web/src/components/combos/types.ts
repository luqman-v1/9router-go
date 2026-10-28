import type { Combo, SmartRule, SmartRulePreview } from '../../api/client'

export interface ComboStrategyInfo {
  fallbackStrategy?: string
  judgeModel?: string
  /** Smart Router policy: smart | strict | per-turn | custom. */
  smartPolicy?: string
  /** Explicit false forces per-turn regardless of smartPolicy. */
  sessionSticky?: boolean
  /** 0-1 confidence a System One choice must reach to switch models. */
  confidenceThreshold?: number
  /** System One model that evaluates the route; empty means the free default. */
  routerModel?: string
  /**
   * Skip the rotation strategy and walk the combo's stored model list in
   * order, so the ordering arranged in the editor is the fallback chain.
   * The capability reorder still runs: a request that needs vision must
   * float a capable model to the front. No effect on the smart strategy,
   * which orders the models itself.
   */
  respectStoredOrder?: boolean
  /** Time-to-first-token timeout in ms for intermediate combo candidates. */
  firstTokenTimeoutMs?: number
}

/**
 * Every strategy a combo card can render. A value the server sends that has
 * no matching <option> leaves the <select> blank while still opening on click,
 * so this list has to cover everything `combo.strategy` can hold — which
 * includes the global routing mode, not just the per-combo overrides.
 */
export const COMBO_STRATEGIES = [
  { value: 'inherit', label: 'Inherit global strategy' },
  { value: 'fallback', label: 'Fallback — try in order' },
  { value: 'round-robin', label: 'Round Robin — rotate' },
  { value: 'sticky', label: 'Sticky — stay until the limit' },
  { value: 'capacity', label: 'Capacity — prefer free tiers' },
  { value: 'smart', label: 'Smart Router — System One' },
  { value: 'fusion', label: 'Fusion — panel + judge' }
] as const

/**
 * The only value that removes a per-combo override. Upstream #4576: the UI
 * used to prune an entry whenever the chosen strategy equalled the default
 * `fallback`, so picking "Fallback" on a combo whose global mode was
 * round-robin looked saved and was not — the combo silently inherited
 * round-robin again. Removal now has to be an explicit choice.
 */
export const INHERIT_STRATEGY = 'inherit'

// The global "Combo Routing Mode" calls try-in-order `first-model`, while a
// combo card calls it `fallback`. applyComboStrategy treats an unrecognised
// value as try-in-order, so the two names are the same behaviour and
// `first-model` resolves to the card's own label rather than a fourth option.
const STRATEGY_ALIASES: Record<string, string> = { 'first-model': 'fallback' }

/** Coerce a server-supplied strategy into one the card can actually display. */
export function resolveComboStrategy(value: string | undefined | null): string {
  if (!value) return 'fallback'
  const resolved = STRATEGY_ALIASES[value] ?? value
  return COMBO_STRATEGIES.some((s) => s.value === resolved) ? resolved : 'fallback'
}

/**
 * The options a strategy <select> renders, with the inherit entry naming the
 * global strategy it would fall back to. Without the name, "Inherit" reads as
 * a sixth routing mode instead of "whatever Combo Routing Mode says".
 */
export function comboStrategyOptions(globalStrategy?: string | null): { value: string; label: string }[] {
  const globalLabel = resolveComboStrategy(globalStrategy)
  return COMBO_STRATEGIES.map((s) =>
    s.value === INHERIT_STRATEGY
      ? { value: s.value, label: `Inherit global — ${globalLabel}` }
      : { value: s.value, label: s.label }
  )
}

/**
 * The strategy a combo actually routes with: its own override when it has one,
 * otherwise the global mode. The server resolves the same precedence (a
 * non-empty per-combo strategy first, the global one second), so the card
 * needs the global value to answer "is this combo fusing?" for an inheriting
 * combo — otherwise a globally-fusing combo shows no judge picker at all.
 */
export function effectiveComboStrategy(
  fallbackStrategy: string | undefined | null,
  globalStrategy?: string | null
): string {
  if (fallbackStrategy) return resolveComboStrategy(fallbackStrategy)
  return resolveComboStrategy(globalStrategy)
}

export interface AdapterPool {
  enabled: boolean
  roundRobin: boolean
  models: string[]
}

export interface CapacityAdapterState {
  vision: AdapterPool
  audioInput: AdapterPool
}


export function hasVision(model: string): boolean {
  const m = model.toLowerCase()
  return (
    m.includes('vision') ||
    m.includes('gemini') ||
    m.includes('claude-3') ||
    m.includes('claude-sonnet') ||
    m.includes('claude-opus') ||
    m.includes('gpt-4o') ||
    m.includes('spark') ||
    m.includes('vl') ||
    m.includes('flash')
  )
}

export function hasReasoning(model: string): boolean {
  const m = model.toLowerCase()
  return (
    m.includes('reason') ||
    m.includes('think') ||
    m.includes('r1') ||
    m.includes('deepseek') ||
    m.includes('spark') ||
    m.includes('high') ||
    m.includes('o1') ||
    m.includes('o3') ||
    m.includes('pro-agent')
  )
}

/** Machine-generated by an "Auto …" header action (auto-free, auto-family-*). */
export function isAutoGenerated(c: Combo): boolean {
  return typeof c.kind === 'string' && c.kind.startsWith('auto-')
}

export function getComboModels(c: Combo): string[] {
  if (Array.isArray(c.models)) return c.models
  if (typeof c.models === 'string') {
    try {
      const parsed = JSON.parse(c.models)
      if (Array.isArray(parsed)) return parsed
      return [c.models]
    } catch {
      return c.models ? [c.models] : []
    }
  }
  return []
}


/**
 * Merge a per-combo strategy patch into `settings.comboStrategies`. Upstream
 * #4576: this used to drop the entry whenever the chosen value was the
 * default `fallback`, which silently threw away an explicit choice — the
 * combo then resolved to the global strategy instead. Only `inherit` (or an
 * empty judge-clearing case, see clearJudgeModel) removes an override now.
 */
export function updateComboStrategy(
  currentStrategies: Record<string, ComboStrategyInfo>,
  comboName: string,
  newStrategy: string
): Record<string, ComboStrategyInfo> {
  const updated = { ...currentStrategies }
  if (newStrategy === INHERIT_STRATEGY) {
    const rest = { ...(updated[comboName] || {}) }
    delete rest.fallbackStrategy
    if (Object.keys(rest).length === 0) delete updated[comboName]
    else updated[comboName] = rest
    return updated
  }
  updated[comboName] = { ...(updated[comboName] || {}), fallbackStrategy: newStrategy }
  return updated
}

export function updateJudgeModel(
  currentStrategies: Record<string, ComboStrategyInfo>,
  comboName: string,
  judgeModel: string
): Record<string, ComboStrategyInfo> {
  const updated = { ...currentStrategies }
  const current = updated[comboName] || {}
  updated[comboName] = { ...current, judgeModel }
  return updated
}

export function clearJudgeModel(
  currentStrategies: Record<string, ComboStrategyInfo>,
  comboName: string
): Record<string, ComboStrategyInfo> {
  const updated = { ...currentStrategies }
  if (updated[comboName]) {
    const { judgeModel: _, ...rest } = updated[comboName]
    // Only an inheriting combo loses its entry here: a combo that names
    // `fallback` explicitly keeps it (upstream #4576).
    if (rest.fallbackStrategy && rest.fallbackStrategy !== INHERIT_STRATEGY) {
      updated[comboName] = rest
    } else {
      delete updated[comboName]
    }
  }
  return updated
}

export function parseCapacityAdapterSettings(ca: Record<string, unknown> | undefined): CapacityAdapterState {
  const v = ca?.vision as Record<string, unknown> | undefined
  const a = ca?.audioInput as Record<string, unknown> | undefined
  return {
    vision: {
      enabled: v?.enabled !== false,
      roundRobin: !!v?.roundRobin,
      models: Array.isArray(v?.models) ? (v.models as string[]) : [],
    },
    audioInput: {
      enabled: a?.enabled !== false,
      roundRobin: !!a?.roundRobin,
      models: Array.isArray(a?.models) ? (a.models as string[]) : [],
    },
  }
}

/**
 * Every matching mode a rule can carry, with the hint the editor shows beside
 * the pattern. The hints are the contract: a glob's `*` spans `/`, a keyword is
 * a substring, and a regex needs its `re:` prefix.
 */
export const SMART_RULE_MODES = [
  {
    value: 'glob',
    label: 'Glob',
    hint: 'Whole id, case-insensitive. * spans "/" — kenari/deepseek*'
  },
  { value: 'keyword', label: 'Keyword', hint: 'Case-insensitive substring — deepseek' },
  { value: 'regex', label: 'Regex', hint: 'Prefix with re:, matched whole — re:^kenari/deepseek-v\\d+$' }
] as const

/** Mirrors internal/combos/smart.DefaultMaxMembers. */
export const DEFAULT_SMART_RULE_MAX_MEMBERS = 20

/**
 * Read a combo's stored rules. An absent, malformed or non-array column reads
 * as no rules: the combo still routes on its stored models, and a broken column
 * must not take the editor down with it.
 */
export function parseSmartRules(raw: string | undefined | null): SmartRule[] {
  if (!raw) return []
  let decoded: unknown
  try {
    decoded = JSON.parse(raw)
  } catch {
    return []
  }
  if (!Array.isArray(decoded)) return []
  const out: SmartRule[] = []
  decoded.forEach((entry, i) => {
    const r = entry as Record<string, unknown>
    if (!r || typeof r !== 'object' || typeof r.pattern !== 'string' || !r.pattern.trim()) return
    out.push({
      id: typeof r.id === 'string' && r.id ? r.id : `r${i + 1}`,
      pattern: r.pattern,
      mode: r.mode === 'keyword' || r.mode === 'regex' ? r.mode : 'glob',
      effect: r.effect === 'exclude' ? 'exclude' : 'include',
      enabled: r.enabled !== false,
      maxMembers: typeof r.maxMembers === 'number' && r.maxMembers >= 0 ? r.maxMembers : DEFAULT_SMART_RULE_MAX_MEMBERS,
      allKinds: r.allKinds === true,
      resolved: Array.isArray(r.resolved) ? (r.resolved as string[]) : [],
      excluded: Array.isArray(r.excluded) ? (r.excluded as string[]) : []
    })
  })
  return out
}

/** One line describing what a rule does, and what the preview measured. */
export function smartRuleSummary(rule: SmartRule, preview?: SmartRulePreview): string {
  const verb = rule.effect === 'exclude' ? 'Removes' : 'Adds'
  const cap = rule.maxMembers > 0 ? `max ${rule.maxMembers}` : 'no cap'
  if (!preview) return `${verb} · ${cap}`
  const matches = `${preview.applied} of ${preview.matched} match${preview.matched === 1 ? '' : 'es'}`
  return preview.capped ? `${verb} ${matches} · capped at ${rule.maxMembers}` : `${verb} ${matches} · ${cap}`
}

/** How many rules a combo carries, for the card badge. */
export function smartRuleCount(combo: Combo): number {
  return parseSmartRules(combo.matchRules).length
}

/**
 * Group a combo's members by provider, keeping the order inside each group.
 *
 * A combo is a fallback chain, so the ordering is the operator's to arrange —
 * this is a one-shot tool, not a standing sort: the operator presses it, sees
 * the grouped order, and can drag anything back. The provider key is the
 * segment before the first "/", and a member without one keeps its own
 * position in a trailing group rather than being folded into the first
 * provider's block.
 *
 * Providers are ordered by first appearance, not alphabetically: the operator
 * arranged the first member of each provider deliberately, and re-sorting the
 * providers themselves would discard that.
 */
export function sortMembersByProvider(models: string[]): string[] {
  const groups = new Map<string, string[]>()
  const order: string[] = []
  const bare: string[] = []
  for (const m of models) {
    const slash = m.indexOf('/')
    if (slash <= 0) {
      bare.push(m)
      continue
    }
    const provider = m.slice(0, slash)
    let group = groups.get(provider)
    if (!group) {
      group = []
      groups.set(provider, group)
      order.push(provider)
    }
    group.push(m)
  }
  const out: string[] = []
  for (const provider of order) out.push(...(groups.get(provider) ?? []))
  out.push(...bare)
  return out
}

/** One entry in the combo editor's sort dropdown. */
export interface ComboSortOption {
  value: ComboSortKey
  label: string
  /** Shown as the control's title, so the trade-off is stated where it is used. */
  hint: string
}

export type ComboSortKey =
  | 'manual'
  | 'group-provider'
  | 'alphabetical'
  | 'alphabetical-desc'
  | 'latency'
  | 'shuffle'
  | 'context-window'
  | 'capabilities'
  | 'kind'

/**
 * Every sort the combo editor offers. "Manual" is first because it is the
 * default state — the order the operator arranged by dragging — and naming it
 * keeps the dropdown honest about what the other options replace.
 */
export const COMBO_SORT_OPTIONS: ComboSortOption[] = [
  {
    value: 'manual',
    label: 'Manual order',
    hint: 'Keep the order you arranged by dragging. This is the default.',
  },
  {
    value: 'group-provider',
    label: 'Group by provider',
    hint: 'Group members by provider, keeping the order inside each group.',
  },
  {
    value: 'alphabetical',
    label: 'Alphabetical',
    hint: 'Sort by the full "provider/model" id, ascending.',
  },
  {
    value: 'alphabetical-desc',
    label: 'Alphabetical (descending)',
    hint: 'Sort by the full "provider/model" id, descending.',
  },
  {
    value: 'latency',
    label: 'Latency (fastest first)',
    hint: 'Sort by observed mean time-to-first-token, then total latency. Models with no traffic yet go last — they are unproven, not slow.',
  },
  {
    value: 'shuffle',
    label: 'Shuffle once',
    hint: 'Randomise the order once and keep it. The result is deterministic afterwards, so the fallback chain stays debuggable.',
  },
  {
    value: 'context-window',
    label: 'Context window (largest first)',
    hint: 'Sort by declared context window, descending. Models with no declared window go last.',
  },
  {
    value: 'capabilities',
    label: 'Capabilities (vision, reasoning first)',
    hint: 'Float models with vision and reasoning to the front. Stable, so the order inside each tier is kept.',
  },
  {
    value: 'kind',
    label: 'Kind (chat first)',
    hint: 'Chat models first, then the media kinds. Useful when a rule was set to match all kinds.',
  },
]

/**
 * Per-model metadata keyed by "provider|model", as GET /api/models/meta
 * returns. Both spellings of a node are present — the node id the latency ring
 * records and the display prefix a combo member is spelled with — so a lookup
 * by the member's own spelling always hits.
 */
export type ModelMetaMap = Record<
  string,
  {
    provider: string
    model: string
    ttftMs: number
    totalMs: number
    samples: number
    contextWindow: number
    kind: string
    vision: boolean
    reasoning: boolean
    audioInput: boolean
  }
>

// metaLookup splits a "provider/model" member into the key the map uses. A
// member without a provider segment is looked up bare, so a hand-typed id
// still finds its row when the server published one.
function metaLookup(model: string, meta?: ModelMetaMap): ModelMetaMap[string] | undefined {
  if (!meta) return undefined
  const slash = model.indexOf('/')
  if (slash <= 0) return meta[model]
  return meta[`${model.slice(0, slash)}|${model.slice(slash + 1)}`]
}

// The capability score is a small ordinal rather than a boolean sum: a model
// with both vision and reasoning outranks one with either alone, and the tiers
// are what the operator asked for ("vision, reasoning first") rather than a
// claim that more flags always means a better fallback candidate.
function capabilityScore(model: string, meta?: ModelMetaMap): number {
  const entry = metaLookup(model, meta)
  if (!entry) return 0
  let score = 0
  if (entry.vision) score += 2
  if (entry.reasoning) score += 2
  if (entry.audioInput) score += 1
  return score
}

// The kind score puts chat first and the media kinds after it, in the order a
// fallback chain would rather not reach them. An unknown kind sorts with chat
// rather than last: a custom model whose kind the catalog has not learned yet
// is still a chat model in practice.
const KIND_ORDER: Record<string, number> = {
  llm: 0,
  chat: 0,
  image: 3,
  tts: 4,
  stt: 4,
  embedding: 5,
  video: 6
}

function kindScore(model: string, meta?: ModelMetaMap): number {
  const kind = metaLookup(model, meta)?.kind ?? ''
  return KIND_ORDER[kind] ?? 0
}

function contextWindowOf(model: string, meta?: ModelMetaMap): number {
  return metaLookup(model, meta)?.contextWindow ?? 0
}

/**
 * Apply one sort to a combo's member list. Every option is a one-shot: the
 * result is written back to the combo and the operator can drag anything
 * afterwards, so none of these is a standing order the gateway re-applies.
 *
 * `meta` is the map from GET /api/models/meta; the sorts that need it fall back
 * to a no-op ordering when it is absent, so a failed fetch degrades to "leave
 * the list alone" rather than to a wrong order.
 */
export function sortComboMembers(
  models: string[],
  key: ComboSortKey,
  meta?: ModelMetaMap
): string[] {
  const out = [...models]
  switch (key) {
    case 'manual':
      return out
    case 'group-provider':
      return sortMembersByProvider(out)
    case 'alphabetical':
      return out.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
    case 'alphabetical-desc':
      return out.sort((a, b) => (b < a ? -1 : b > a ? 1 : 0))
    case 'latency': {
      const score = (m: string) => {
        const entry = metaLookup(m, meta)
        if (!entry || entry.samples <= 0) return null
        // TTFT leads because it is what the caller waits for before anything
        // appears; total latency breaks a tie between models that start
        // equally fast but finish differently.
        return entry.ttftMs > 0 ? entry.ttftMs : entry.totalMs
      }
      return out.sort((a, b) => {
        const sa = score(a)
        const sb = score(b)
        if (sa === null && sb === null) return 0
        if (sa === null) return 1
        if (sb === null) return -1
        return sa - sb
      })
    }
    case 'shuffle': {
      // Fisher-Yates over a copy. The shuffle happens once, on click, and the
      // result is stored — a chain that re-shuffled on every reconcile could
      // not be reasoned about after the fact.
      for (let i = out.length - 1; i > 0; i--) {
        const j = Math.floor(Math.random() * (i + 1))
        ;[out[i], out[j]] = [out[j], out[i]]
      }
      return out
    }
    case 'context-window':
      return out.sort((a, b) => contextWindowOf(b, meta) - contextWindowOf(a, meta))
    case 'capabilities':
      return out.sort((a, b) => capabilityScore(b, meta) - capabilityScore(a, meta))
    case 'kind':
      return out.sort((a, b) => kindScore(a, meta) - kindScore(b, meta))
    default:
      return out
  }
}

/**
 * Whether a list is already in the order a given sort would produce. The
 * dropdown uses it to show "already sorted" so an operator does not press the
 * same option repeatedly waiting for something to happen.
 *
 * `shuffle` is never "already sorted": any order is a valid shuffle, and
 * claiming otherwise would make the control look broken on the one option
 * whose whole point is that the result is arbitrary.
 */
export function isComboSorted(
  models: string[],
  key: ComboSortKey,
  meta?: ModelMetaMap
): boolean {
  if (key === 'manual' || key === 'shuffle' || models.length < 2) return true
  const sorted = sortComboMembers(models, key, meta)
  return sorted.every((m, i) => m === models[i])
}

/** Whether a member is owned by a rule rather than pinned by the operator. */
export function isAutoMember(combo: Combo, model: string): boolean {
  return Array.isArray(combo.autoMembers) && combo.autoMembers.includes(model)
}

/**
 * Merge a Smart Router policy patch into `settings.comboStrategies`. The
 * policy carries its own stickiness: per-turn is the one policy without a
 * session pin, so it is recorded as sessionSticky:false alongside the name
 * (upstream SmartPolicyModal's onSave payload).
 */
export function updateSmartPolicy(
  currentStrategies: Record<string, ComboStrategyInfo>,
  comboName: string,
  cfg: { smartPolicy: string; sessionSticky: boolean; confidenceThreshold: number }
): Record<string, ComboStrategyInfo> {
  const updated = { ...currentStrategies }
  updated[comboName] = { ...(updated[comboName] || {}), ...cfg }
  return updated
}

/** Merge a router-model override into `settings.comboStrategies`. */
export function updateRouterModel(
  currentStrategies: Record<string, ComboStrategyInfo>,
  comboName: string,
  routerModel: string
): Record<string, ComboStrategyInfo> {
  const updated = { ...currentStrategies }
  const rest = { ...(updated[comboName] || {}) }
  if (routerModel) rest.routerModel = routerModel
  else delete rest.routerModel
  updated[comboName] = rest
  return updated
}

/** The policy chip label a smart combo card shows (upstream getPolicyBadgeLabel). */
export function smartPolicyLabel(info: ComboStrategyInfo): string {
  const policy = info.smartPolicy || (info.sessionSticky === false ? 'per-turn' : 'smart')
  if (policy === 'strict') return 'Strict Lock'
  if (policy === 'per-turn') return 'Per-Turn'
  if (policy === 'custom') {
    const raw = info.confidenceThreshold
    const pct = Math.round((raw || 0.8) * (raw && raw <= 1 ? 100 : 1))
    return `Custom (≥${pct || 80}%)`
  }
  return 'Smart (≥80%)'
}
