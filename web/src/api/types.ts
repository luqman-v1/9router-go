export interface DatabaseStats {
  databaseSizeBytes: number
  reclaimableBytes: number
  requestDetailsCount: number
  usageHistoryCount: number
  usageHistoryOldestTimestamp?: string
  usageHistoryNewestTimestamp?: string
}

export interface PruneUsageResult {
  success: boolean
  dryRun: boolean
  wouldDelete?: number
  pruned?: number
  carriedTokens?: number
  cutoff: string
}
// Typed API client for 9router-go Native Dashboard

export interface ProviderConnection {
  id: string
  provider: string
  authType: string
  name: string | null
  email: string | null
  priority: number | null
  isActive: number // 1 or 0
  data: string // JSON string
  createdAt: string
  updatedAt: string
  /** Masked hint of the stored credential; the raw secret is never listed. */
  apiKeyMasked?: string
  testStatus?: string | null
  lastError?: string | null
  lastErrorModel?: string | null
  lastErrorSource?: 'chat' | null
  displayName?: string | null
  assignedModel?: string | null
  mediaEnabled?: boolean
  isMediaActive?: boolean
  disabledKinds?: string[]
  providerSpecificData?: {
    assignedModel?: string | null
    disabledKinds?: string[]
    mediaEnabled?: boolean
    isMediaActive?: boolean
    [key: string]: unknown
  }
}

export interface Combo {
  id: string
  name: string
  kind: string | null
  models: string // JSON array string
  strategy: string
  createdAt: string
  updatedAt: string
  /** Raw JSON of the combo's smart rules. Absent means the combo has none. */
  matchRules?: string
  /** Combo-specific system prompt; unset falls back to the global custom prompt. */
  customPrompt?: string
  /** Derived server-side: the members the rules currently own. */
  autoMembers?: string[]
}

/** One smart-select rule: a pattern plus the membership it currently owns. */
export interface SmartRule {
  id: string
  pattern: string
  mode: 'glob' | 'keyword' | 'regex'
  effect: 'include' | 'exclude'
  enabled: boolean
  /** 0 means unlimited. */
  maxMembers: number
  allKinds: boolean
  resolved?: string[]
  excluded?: string[]
}

/** What one rule would contribute, as the preview endpoint reports it. */
export interface SmartRulePreview {
  id: string
  matched: number
  applied: number
  capped: boolean
}

/** The dry run of a rule list against the live model set. */
export interface ComboRulesPreview {
  combo: string
  candidates: number
  models: string[]
  added: string[]
  removed: string[]
  rules: SmartRulePreview[]
}
/** Resolved server-side capabilities for one catalog model. */
export interface ModelCaps {
  vision: boolean
  search: boolean
  reasoning: boolean
  /** Uncensored / unrestricted — accepts prompts a filtered model refuses. */
  jailbreak: boolean
  contextWindow: number
  maxOutput: number
  /** Selectable thinking levels, or empty for a model without reasoning. */
  thinkingLevels: string[]
  free?: boolean
}

/**
 * A model the gateway has confirmed is gone upstream.
 *
 * `status` is "gone" when a live request drew an HTTP 410 naming the model,
 * "retired" when a catalogue sync found it absent from a complete list, and
 * "cooldown" when a live failure marked it temporarily unavailable for a
 * fixed window (5 minutes). A cooldown is transient: it never overwrites a
 * permanent "gone" or "retired" record, and it lapses on its own.
 */
export interface ModelDeprecation {
  provider: string
  model: string
  status: 'gone' | 'retired' | 'cooldown' | ''
  message?: string
  /** The upstream's own replacement, when it named one. */
  successor?: string
  detectedAt?: string
  /** RFC3339 UTC. Only set for status "cooldown"; the badge lapses after it. */
  cooldownUntil?: string
}

/** Deprecations keyed "<provider>/<model>" — the same key a combo entry uses. */
export type ModelDeprecationMap = Record<string, ModelDeprecation>

export interface APIKey {
  id: string
  /**
   * The stored secret. Empty for a caller authenticated with an engine client
   * key, and for rows created before #199, whose plaintext was hashed away and
   * cannot be recovered — the UI falls back to `keyDisplay` in both cases.
   */
  key: string
  name: string | null
  machineId: string | null
  isActive: number
  createdAt: string
  /** Masked secret, same length as the key itself, e.g. `sk-6••008f`. Always present. */
  keyDisplay?: string
  /** Rate limits. 0 means unlimited. */
  rateLimitRpm?: number
  rateLimitRph?: number
  rateLimitRpd?: number
  rateLimitTpm?: number
  rateLimitConcurrency?: number
  /** Lifetime prompt+completion token budget. 0 means unlimited. */
  tokenQuota?: number
  /** Context window one request from this key may use, in tokens. 0 means the model's own window. */
  contextLimit?: number
  /** Spend recorded against tokenQuota since the budget was last reset (#365). */
  usedTokens?: number
  /** How long a request waits for a free concurrency slot, in ms. 0 uses the server default. */
  queueTimeoutMs?: number
  /** RFC3339 UTC. Empty means the key never expires. */
  expiresAt?: string
  lastUsedAt?: string
  usedCount?: number
  /** Free-form JSON object, e.g. { customerId, priceCents }. */
  metadata?: string
  /** Plan template this key inherits from. Empty means no plan. */
  planId?: string
  /** Per-period cost budget, in integer USD micros (1e-6 of a dollar). 0 means no cost limit. */
  limitMicros?: number
  /** Per-period token budget. 0 means no token limit. */
  limitTokens?: number
  /** Window the budget resets on. Empty means monthly — the engine's default. */
  budgetPeriod?: PlanPeriod
  /** Warn at this percentage of the period budget, 0-100. 0 means no warning. */
  alertPct?: number
  /** True when exhausted requests are refused with 402; false when they only carry a warning header. */
  hardCutoff?: boolean
}

export interface APIKeyPolicy {
  rateLimitRpm?: number
  rateLimitRph?: number
  rateLimitRpd?: number
  rateLimitTpm?: number
  rateLimitConcurrency?: number
  /** Lifetime token budget. 0 means unlimited. */
  tokenQuota?: number
  /** Context window one request from this key may use, in tokens. 0 means the model's own window. */
  contextLimit?: number
  /** Concurrency queue timeout in ms. 0 uses the server default. */
  queueTimeoutMs?: number
  expiresAt?: string
  metadata?: string
  /** Renames the key. An empty string clears the stored name. */
  name?: string
  /** Plan template to inherit. An empty string detaches the key. */
  planId?: string
/**
   * Per-period cost budget, in integer USD micros (1e-6 of a dollar). Always send
   * a number: the server treats an omitted field as "keep stored value" and an
   * explicit 0 as "disable", so 0 is how the operator clears a cost limit.
   */
  limitMicros?: number
  /** Per-period token budget. 0 means no token limit. */
  limitTokens?: number
  /** Window the budget resets on: "daily" | "weekly" | "monthly" | "total". Always sent; empty means monthly. */
  budgetPeriod?: string
  /** Warn at this percentage of the period budget, 0-100. 0 means no warning. */
  alertPct?: number
  /** True when exhausted requests are refused with 402; false when they only carry a warning header. */
  hardCutoff?: boolean
  /** Model allowlist, in the shape the policy route writes it whole.
   *  `restricted` accompanies `allow`: an omitted flag means "unrestricted", so
   *  sending only `allow` does not restrict a key. */
  access?: { restricted?: boolean; allow: string[] }
}

/**
 * A key's period budget as the budget engine sees it: the ceiling, what has
 * already been spent against it in the open window, and where the two stand
 * relative to each other.
 */
export interface ApiKeyBudget {
  id: string
  /** Cost ceiling for the open window, integer USD micros. 0 means no cost limit. */
  limitMicros: number
  /** Token ceiling for the open window. 0 means no token limit. */
  limitTokens: number
  /** Window the numbers reset on. */
  period: PlanPeriod
  /** Warning threshold, 0-100. 0 means no warning. */
  alertPct: number
  /** True when exhausted requests are refused with 402; false when they only carry a warning header. */
  hardCutoff: boolean
  /** A budget is armed when at least one axis has a nonzero ceiling. */
  armed: boolean
  /** Spend recorded in the open window, integer USD micros. */
  spentMicros: number
  /** Spend recorded in the open window, tokens. */
  spentTokens: number
  /** What is left on each axis before the window resets. */
  remainingMicros: number
  remainingTokens: number
  /** RFC3339 UTC. Empty for the "total" period, which never resets. */
  resetAt: string
  /** "ok", "approaching" (past alertPct) or "exhausted". */
  status: 'ok' | 'approaching' | 'exhausted'
  /** Present when spend cannot be measured — e.g. a key whose secret was hashed away. */
  note?: string
}
/** A reusable policy template (F-3). Keys inherit its limits as defaults. */
export interface Plan {
  id: string
  name: string
  description: string
  limitMicros: number
  limitTokens: number
  rpmLimit: number
  tpmLimit: number
  rphLimit: number
  rpdLimit: number
  concurrencyLimit: number
  period: PlanPeriod
  alertPct: number
  hardCutoff: boolean
  allowedModels: string[]
  createdAt: string
  updatedAt: string
}

export type PlanPeriod = 'daily' | 'weekly' | 'monthly' | 'total'

/** A plan write. Omitted fields keep their stored value on update. */
export type PlanInput = Partial<Omit<Plan, 'id' | 'createdAt' | 'updatedAt'>>

export interface GuardrailPolicy {
  id: string
  tenantId: string
  scope: string
  scopeId: string
  name: string
  enabled: boolean
  config: string
  createdAt: string
  updatedAt: string
}

export interface GuardrailLog {
  id: string
  tenantId: string
  apiKeyId?: string
  provider?: string
  model?: string
  detector: string
  direction: string
  action: string
  severity?: string
  reason?: string
  findings?: string
  createdAt: string
}

export interface VaultStatus {
  enabled: boolean
  sealedCount: number
  plaintextCount: number
}

export interface ProviderStrategyConfig {
  proxyPoolId?: string
  rotateStrategy?: string
  strictModelAssignment?: boolean
  [key: string]: unknown
}

export interface Settings {
  appName?: string
  appLogoUrl?: string
  requireApiKey?: boolean
  appDescription?: string
  appTagline?: string
  loginLogo?: string
  trayAppName?: string
  trayIcon?: string
  tunnelDashboardAccess?: boolean
  tunnelWatchdogEnabled?: boolean
  tailscaleWatchdogEnabled?: boolean
  hideUnusedTunnel?: boolean
  tunnelWorkerUrl?: string
  rtkEnabled?: boolean
  rtkMode?: 'simple' | 'advance' | string
  rtkIntensity?: 'minimal' | 'standard' | 'aggressive' | string
  rtkMaxLines?: number
  rtkMaxChars?: number
  rtkDeduplicate?: boolean
  rtkCategories?: Record<string, boolean>
  rtkFilters?: Record<string, boolean>
  rtkRawRetention?: 'never' | 'failures' | 'always' | string
  cavemanEnabled?: boolean
  cavemanMode?: 'simple' | 'advance' | string
  cavemanLevel?: string
  cavemanAutoClarity?: boolean
  cavemanLanguage?: string
  cavemanInputMode?: boolean
  cavemanPreserveKeywords?: string
  ponytailEnabled?: boolean
  ponytailLevel?: string
  adhdEnabled?: boolean
  adhdLevel?: string
  headroomEnabled?: boolean
  headroomAutostart?: boolean
  headroomEngine?: 'builtin' | 'external'
  headroomUrl?: string
  headroomTimeoutMs?: number
  headroomKompress?: boolean
  /** Qoder / Qoder CN daily-credit check-in. */
  qoderCheckin?: boolean
  /** MiniMax Code daily check-in (每日签到). */
  minimaxCheckin?: boolean
  semanticCacheEnabled?: boolean
  semanticCacheTTL?: number
  // Prompt-cache window 9router pins on Claude head anchors: "5m" (default) or "1h".
  claudeCacheTtl?: '5m' | '1h'
  semanticCacheMaxEntries?: number
  autoUpdate?: boolean
  /** Dashboard security & SSO (profile page, Next parity). */
  requireLogin?: boolean
  hasPassword?: boolean
  authMode?: string
  ssoType?: string
  oidcIssuerUrl?: string
  oidcClientId?: string
  oidcScopes?: string
  oidcLoginLabel?: string
  samlEntryPoint?: string
  samlIssuer?: string
  samlCert?: string
  samlLoginLabel?: string
  samlAttributeEmail?: string
  samlAttributeName?: string
  /** Routing and network preferences (profile page). */
  fallbackStrategy?: string
  comboStrategy?: string
  stickyRoundRobinLimit?: number
  comboStickyRoundRobinLimit?: number
  /** Serve requests from the account that frees up first when all are cooling down. */
  forceFallback?: boolean
  enableObservability?: boolean
  /** Global system prompt: prefix leads the system prompt, suffix ends it. */
  systemPromptEnabled?: boolean
  systemPromptPrefix?: string
  systemPromptSuffix?: string
  outboundProxyEnabled?: boolean
  outboundProxyUrl?: string
  outboundNoProxy?: string
  providerStrategies?: Record<string, ProviderStrategyConfig>
  /**
   * Sidebar layout: the entry ids the operator hid, and the arrangement they
   * moved the entries into. The canonical id list lives in web/src/lib/nav.ts;
   * the server only checks the shape
   * (internal/handlers/dashboard/navvisibility.go).
   */
  navVisibility?: { hidden?: string[]; order?: string[] }
  /**
   * Display currency for every cost surface. Costs themselves are stored in USD;
   * see web/src/lib/currency.ts for the catalog and the formatting rules.
   */
  displayCurrency?: {
    code?: string
    rates?: Record<string, number>
    showBoth?: boolean
    updatedAt?: string
  }
  [key: string]: unknown
}

/**
 * One provider's outbound header overrides. `builtinHeaders` is what the
 * registry already sends on this provider's requests; `blockedHeaders` is the
 * set an override may never carry (credentials, framing, destination), so the
 * UI can disable those fields instead of discovering them through a 400.
 */
export interface ProviderOverridesResponse {
  provider: string
  headers: Record<string, string>
  builtinHeaders: Record<string, string>
  blockedHeaders: string[]
}

/** One editable capability field, as the server describes it. */
export interface ModelCapsField {
  key: string
  label: string
  desc: string
  type: 'boolean' | 'number'
}

/**
 * The per-model capability editor's payload. `capsOverride` holds only what the
 * operator pinned (absent key = Auto); `detected` is what the gateway currently
 * resolves, so an Auto field shows the real number rather than a placeholder.
 */
export interface ModelCapsOverrideResponse {
  provider: string
  model: string
  capsOverride: Record<string, boolean | number>
  detected: Record<string, unknown>
  fields: ModelCapsField[]
  maxTokens: number
}

export interface TunnelStatusResponse {
  tunnel?: {
    enabled?: boolean
    settingsEnabled?: boolean
    tunnelUrl?: string
    publicUrl?: string
    running?: boolean
    state?: string
    mode?: string
    shortId?: string
    customDomain?: string
    token?: string
    cfApiToken?: string
    previousUrl?: string
    rotatedAt?: string
    workerUrl?: string
    reachable?: boolean
    everReachable?: boolean
    connectingLabel?: string
  }
  tailscale?: {
    enabled?: boolean
    settingsEnabled?: boolean
    tunnelUrl?: string
    running?: boolean
    loggedIn?: boolean
    installed?: boolean
    reachable?: boolean
    everReachable?: boolean
    authUrl?: string
    authMode?: string
    authKey?: string
    statusText?: string
    repairTunnelOrigin?: boolean
  }
  download?: {
    downloading?: boolean
    progress?: number
  }
}

// --- Hermes per-profile configuration (upstream decolua/9router#4660) ---

/** One Hermes profile as reported by `GET /api/cli-tools/hermes-profiles`. */
export interface HermesProfile {
  name: string
  dir: string
  isDefault: boolean
  displayName: string | null
  command: string
  alias: string | null
  model: string | null
  baseUrl: string | null
  has9Router: boolean
}

export interface HermesProfilesResponse {
  profiles: HermesProfile[]
}

/** The top-level `model:` block of `~/.hermes/config.yaml`. */
export interface HermesModelBlock {
  default: string
  provider: string
  base_url: string
  api_key: string
}

/** The top-level `delegation:` block. */
export interface HermesDelegationBlock {
  model: string
  provider: string
  base_url: string
}

/** One entry of the `auxiliary:` map, keyed by role id. */
export interface HermesAuxRole {
  model: string
  provider: string
  base_url: string
}

export interface HermesSettings {
  model: HermesModelBlock | null
  delegation: HermesDelegationBlock | null
  auxiliary: Record<string, HermesAuxRole>
}

export interface HermesSettingsResponse {
  installed: boolean
  profile?: { name: string; dir: string; isDefault: boolean }
  settings: HermesSettings | null
  has9Router?: boolean
  configPath?: string
  message?: string
}

/** One `{ role, model }` pair in an apply payload. `default` is the main block. */
export interface HermesSelection {
  role: string
  model: string
}

export interface HermesApplyPayload {
  profile?: string
  baseUrl: string
  apiKey?: string | null
  model?: string
  selections?: HermesSelection[]
  applyToAll?: boolean
}

export interface HermesApplyResult {
  profile: string
  status: 'updated' | 'skipped' | 'failed'
  reason?: string
}

export interface HermesApplyResponse {
  success: boolean
  bulk?: boolean
  results?: HermesApplyResult[]
  updated?: number
  skipped?: number
  message?: string
  error?: string
  profile?: { name: string; dir: string; isDefault: boolean }
  configPath?: string
}

export interface HermesResetResponse {
  success: boolean
  profile?: string
  removed?: { model: boolean; delegation: boolean; auxiliary: string[] }
  kept?: string[]
  message?: string
  error?: string
}

// --- CLI Tools Configuration (Claude Code, Codex, OpenCode, Droid, Crush, Kilo, Cline, Pi, OMP) ---
export interface CliToolSettingsResponse {
  tool: string
  installed: boolean
  has9Router?: boolean
  files?: Array<{ path: string; format: string; purpose: string }>
  plan?: Array<{ path: string; purpose: string; removeFile?: boolean; edits?: Array<{ keyPath: string[] }> }>
  currentConfig?: {
    baseUrl?: string
    apiKey?: string
    hasApiKey?: boolean
    model?: string
    tiers?: Record<string, string>
    autoCompactWindow?: string | number
  }
  message?: string
}

export interface CliToolSettingsApplyPayload {
  baseUrl: string
  apiKey?: string
  model?: string
  models?: unknown[]
  tiers?: Record<string, string>
  autoCompactWindow?: string
}

export interface CliToolSettingsApplyResponse {
  success: boolean
  tool: string
  changed: boolean
  backups?: string[]
  message?: string
}

export interface HeadroomStatusResponse {
  installed?: boolean
  running?: boolean
  python?: string | null
  localUrl?: boolean
  canStart?: boolean
  managedPid?: number | null
  path?: string | null
  version?: string | null
  extras?: { code?: boolean; ml?: boolean }
  url?: string
  engine?: 'builtin' | 'external'
  error?: string
}

export interface HeadroomExtrasResponse {
  version?: string | null
  extras?: { code?: boolean; ml?: boolean }
  available?: string[]
  log?: string
  error?: string
}

export interface ProxyPool {
  id: string
  name: string
  type: 'http' | 'socks5' | 'vercel' | 'cloudflare' | 'deno' | 'netlify' | string
  proxyUrl?: string
  urls?: string[]
  noProxy?: string
  strictProxy?: boolean
  isActive: boolean
  testStatus?: 'passed' | 'failed' | 'unknown' | string
  latency?: number
  lastTestedAt?: string | null
  boundConnectionCount?: number
  createdAt?: string
  updatedAt?: string
  [key: string]: unknown
}

/** Payload accepted by POST /api/connections (upstream POST /api/providers). */
export interface CreateConnectionPayload {
  id?: string
  provider: string
  authType: string
  name?: string
  displayName?: string
  apiKey?: string
  data?: string
  priority?: number
  testStatus?: 'active' | 'unknown' | string
  proxyPoolId?: string | null
  defaultModel?: string
  providerSpecificData?: Record<string, unknown>
}

/** Result of POST /api/providers/validate. supported=false means this backend
 * has no probe for the provider, so the UI must not claim the key is invalid. */
export interface ValidateProviderResult {
  supported: boolean
  valid: boolean
  error?: string
}

export interface ProviderNode {
  id: string
  type: string
  name: string
  prefix?: string
  apiType?: string
  baseUrl?: string
  /**
   * The custom tail of the provider id, without the "openai-compatible-chat-"
   * literal in front of it. Empty for a node created before the field existed.
   */
  urlSuffix?: string
  /** True when the id tail is the random fallback rather than a chosen value. */
  urlSuffixGenerated?: boolean
  createdAt?: string
  updatedAt?: string
  /**
   * The node's capacity policy plus what it has spent against it. Filled by the
   * list endpoint, so the dashboard never has to make a second call to discover
   * what a node is currently allowed.
   */
  limits?: ProviderNodeLimits
}

/** Availability of a live candidate provider namespace or composed id. */
export interface ProviderNodeFieldAvailability {
  value: string
  status: 'available' | 'taken' | 'invalid' | 'automatic' | 'random'
  reason?: string
  providerId?: string
}

export interface ProviderNodeAvailability {
  prefix: ProviderNodeFieldAvailability
  urlSuffix: ProviderNodeFieldAvailability
}

/**
 * ProviderNodeLimits mirrors the Go side (ProviderNodeLimitsResponse). Live
 * figures (`rpmUsedWindow`, `activeConcurrency`) come from the gateway's own
 * limiter and read 0 when nothing has been metered yet.
 */
export interface ProviderNodeLimits {
  rpm: number
  tpm: number
  concurrency: number
  tokenQuota: number
  queueTimeoutMs: number
  quotaResetId: number
  contractExpiresAt?: string
  usedTokens: number
  expired: boolean
  rpmUsedWindow: number
  activeConcurrency: number
}

/**
 * ProviderNodeLimitsPatch is the write side. Every field is optional and an
 * omitted field keeps its stored value — sending `0` is what clears one, and the
 * distinction is the whole reason this is a separate call from updateProviderNode.
 */
export interface ProviderNodeLimitsPatch {
  rpm?: number
  tpm?: number
  concurrency?: number
  tokenQuota?: number
  queueTimeoutMs?: number
  /** RFC3339 or a date (YYYY-MM-DD); "" clears the expiry. */
  contractExpiresAt?: string
}

/**
 * CustomModelBatchEntry is one line of a bulk save. Capabilities travel per entry
 * even though the dashboard sends them uniformly for the whole paste (D10): the
 * endpoint stays general, and a caller that already holds per-entry values is not
 * forced to collapse them into one set.
 */
export interface CustomModelBatchEntry {
  id: string
  name?: string
  type?: string
  vision?: boolean
  reasoning?: boolean
  contextWindow?: number
  maxOutput?: number
}

export interface SystemVersionInfo {
  currentVersion: string
  latestVersion?: string
  hasUpdate?: boolean
  downloadUrl?: string
  releaseNotes?: string
  goVersion?: string
  os?: string
  arch?: string
  checkedAt?: string
}
export interface FreebuffInitiateResponse {
  loginUrl: string
  authCode: string
  fingerprintId: string
  fingerprintHash: string
  expiresAt: string
}

export interface FreebuffPollResponse {
  status: 'authorized' | 'pending' | 'expired'
  connectionId?: string
  user?: {
    id?: string
    name?: string
    email?: string
  }
}

export interface ConnectionQuotaInfo {
  used?: number
  total?: number
  resetAt?: string
  remainingPercentage?: number
  remaining?: number
  unlimited?: boolean
  displayName?: string
  name?: string
  modelKey?: string
}

export interface ConnectionUsageResponse {
  plan?: string
  message?: string
  quotas?: Record<string, ConnectionQuotaInfo> | ConnectionQuotaInfo[]
  error?: string
}

// Usage dashboard (Details section): heatmap cells, lifetime cards, node health.
export interface UsageDailyPoint {
  date: string
  requests: number
  tokens: number
  cost: number
}
export interface UsageTopModel {
  model: string
  provider?: string
  tokens: number
}
export interface UsageLifetime {
  totalRequests: number
  totalTokens: number
  peakTokens: number
  peakDate: string
  currentStreak: number
  longestStreak: number
  topModel: UsageTopModel | null
  cacheHitRate: number | null
  successRate: number | null
}
export interface UsageNodeHealth {
  name?: string
  model?: string
  provider: string
  requests: number
  errors: number
  successRate: number
  avgLatencyMs: number | null
  avgTtftMs: number | null
  avgSpeed: number | null
  hasPerfData: boolean
  score: number | null
  promptTokens: number
  completionTokens: number
  cost: number
  lastUsed: string
}
export interface UsageDashboardResponse {
  daily: UsageDailyPoint[]
  nodes: UsageNodeHealth[]
  models: UsageNodeHealth[]
  lifetime: UsageLifetime | null
}

/** One client API key's spend inside the window. */
export interface UsageByAPIKeyItem {
  /** The apiKeys row the spend belongs to; empty when the key was deleted. */
  id: string
  name: string
  /** The masked key, in the form the usage ledger stores. */
  key: string
  /** The masked key in the API keys page's own form. */
  keyDisplay: string
  /** Whether the key is still enabled. */
  active: boolean
  requests: number
  errors: number
  promptTokens: number
  completionTokens: number
  cachedTokens: number
  cost: number
  /** When the key last made a request inside the window. */
  lastUsed: string
}

export interface UsageByAPIKeyTotals {
  requests: number
  errors: number
  promptTokens: number
  completionTokens: number
  cachedTokens: number
  cost: number
}

export interface UsageByAPIKeyResponse {
  period: string
  keys: UsageByAPIKeyItem[]
  totals: UsageByAPIKeyTotals
}

// Codex reset credits. `selectionToken` is the field the dashboard selects
// with; `code` carries the typed refusal the server raised (e.g. no_credit vs
// nothing_to_reset) so the modal can say which happened.
export interface CodexResetCredit {
  selectionToken: string
  resetType?: string
  status?: string
  grantedAt?: string
  expiresAt?: string
  title?: string
  description?: string
}

export interface CodexResetCreditListResponse {
  credits: CodexResetCredit[]
  availableCount: number
  error?: string
  code?: string
}

export interface CodexResetCreditConsumeResponse {
  outcome: 'reset' | 'alreadyRedeemed' | 'error'
  selectionToken?: string
  idempotencyKey?: string
  error?: string
  code?: string
}

// GLM Reset Cards (Z.ai international). Mirrors the Go GLMResetCard and
// GLMResetCardsResponse types.
export interface GLMResetCard {
  recordId: string
  type: 'FIVE_HOUR' | 'WEEK'
  expiresAt: string // RFC3339 UTC
}

export interface GLMResetCardsResponse {
  availableCount: number
  cards: GLMResetCard[]
  error?: string
}

export interface GLMResetCardRedeemResponse {
  reset: boolean
  alreadyUsed?: boolean
  uncertain?: boolean
  code: string
  message: string
  recordId: string
  type: string
  requestId: string
  error?: string
}

export interface FreebuffSessionSwitchResponse {
  status: 'active'
  currentModel: string
  instanceId?: string
  expiresAt?: string
  /** False when the session was already on the requested model. */
  switched: boolean
  /** Freebucks the server credited back for the released session. */
  freebucksRefund?: number
}

export interface FreebuffSessionStatusResponse {
  status: 'active' | 'queued' | 'ended' | 'none' | 'unauthorized' | 'banned' | 'country_blocked'
  /** Account this report describes — set so the UI can name it. */
  connectionId?: string
  connectionName?: string
  currentModel?: string
  instanceId?: string
  expiresAt?: string
  accessTier?: string
  countryCode?: string
  /** Present when the server refuses this region, even on an active session. */
  countryBlockReason?: string
  /** Sessions the account has left today, for the model in currentModel. */
  rateLimit?: {
    model?: string
    limit?: number
    recentCount?: number
    poolLabel?: string
    resetAt?: string
    resetTimeZone?: string
  }
  freebucks?: {
    balance?: number
    daily?: {
      limit?: number
      spent?: number
      remaining?: number
      resetAt?: string
      resetTimeZone?: string
    }
    wallet?: {
      balance?: number
      monthlyBonus?: number
    }
    planId?: string | null
    prices?: Record<string, number>
    [key: string]: unknown
  }
}
export interface RequireLoginResponse {
  requireLogin: boolean
  tunnelDashboardAccess?: boolean
  tunnelUrl?: string
  tailscaleUrl?: string
  hasPassword?: boolean
  authMode?: string
  authenticated?: boolean
}

export interface LoginResponse {
  success: boolean
  mustChangePassword?: boolean
  error?: string
  retryAfter?: number
  resetHint?: string
  remainingBeforeLock?: number
}

export interface SemanticCacheStats {
  memoryEntries: number
  dbEntries: number
  hits: number
  misses: number
  hitRate: string
  tokensSaved: number
}

export interface PromptCacheProviderStats {
  requests: number
  totalRequests?: number
  cachedRequests?: number
  inputTokens: number
  cachedTokens: number
  cacheCreationTokens: number
}

export interface PromptCacheMetrics {
  totalRequests: number
  requestsWithCacheControl: number
  totalInputTokens: number
  totalCachedTokens: number
  totalCacheCreationTokens: number
  tokensSaved: number
  estimatedCostSaved: number
  byProvider: Record<string, PromptCacheProviderStats>
  byModel?: Record<string, PromptCacheProviderStats>
  lastUpdated: string
  /** The window these numbers describe, echoed from the request. */
  period?: string
  /** Distinct providers/models left out of the breakdowns by the server cap. */
  truncatedProviders?: number
  truncatedModels?: number
}

export interface CacheTrendPoint {
  timestamp: string
  requests: number
  cachedRequests: number
  inputTokens: number
  cachedTokens: number
  cacheCreationTokens: number
}

export interface CacheStatsResponse {
  semanticCache: SemanticCacheStats
  promptCache: PromptCacheMetrics | null
  trend: CacheTrendPoint[]
  idempotency: {
    activeKeys: number
    windowMs: number
  }
  config?: {
    semanticCacheEnabled: boolean
  }
}

export interface CacheEntryMeta {
  id: string
  signature: string
  model: string
  hit_count: number
  tokens_saved: number
  created_at: string
  expires_at: string
}

export interface CacheEntriesResponse {
  entries: CacheEntryMeta[]
  pagination: {
    page: number
    limit: number
    total: number
    totalPages: number
  }
}

/** One provider/model pair's circuit breaker snapshot. */
export interface ModelBreakerStatus {
  /** `closed` serves normally; `cooling` sits out a cooldown; `disabled` needs a reset. */
  state: 'closed' | 'cooling' | 'disabled'
  provider: string
  model: string
  /** Consecutive model-scoped failures since the last success. */
  failures: number
  /** Cooldown cycles already served, which is what makes the next one longer. */
  cooldowns: number
  /** Milliseconds until the pair is probed again. 0 when there is no timer. */
  retryAfterMs: number
}

/** The panel response: held-back pairs ordered first by the backend. */
export interface ModelBreakerStatusResponse {
  models: ModelBreakerStatus[]
}

export interface CompressionModeStats {
  count: number
  tokensSaved: number
  avgSavingsPct: number
  skipped?: number
}

export interface CompressionProviderStats {
  count: number
  tokensSaved: number
}

export interface CompressionModelStats {
  count: number
  tokensSaved: number
  avgSavingsPct: number
}

export interface CompressionHourBucket {
  hour: string
  count: number
  tokensSaved: number
}

export interface CompressionRealUsage {
  requestsWithReceipts: number
  promptTokens: number
  completionTokens: number
  totalTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  estimatedUsdSaved: number
  bySource?: Record<string, number>
}

export interface CompressionTopSaver {
  requestId: string
  timestamp: string
  provider: string
  model: string
  mode: string
  originalTokens: number
  compressedTokens: number
  tokensSaved: number
  savingsPct: number
  durationMs: number
  estimatedUsd: number
}

export interface CompressionAnalyticsSummary {
  totalRequests: number
  totalTokensSaved: number
  avgSavingsPct: number
  avgDurationMs: number
  byMode: Record<string, CompressionModeStats>
  byProvider: Record<string, CompressionProviderStats>
  byModel?: Record<string, CompressionModelStats>
  /** The window these numbers describe, echoed from the request. */
  period?: string
  /** Hourly series over `period`. */
  trend: CompressionHourBucket[]
  /** Distinct providers/models left out of the breakdowns by the server cap. */
  truncatedProviders?: number
  truncatedModels?: number
  totalSkipped?: number
  bySkipReason?: Record<string, number>
  validationFallbacks: number
  realUsage: CompressionRealUsage
  roiTokensPerMs?: number
  topSavers?: CompressionTopSaver[]
}
/** Jailbreak: the default injected document (upstream decolua/9router parity). */
export interface JailbreakDoc {
  prompt: string
  geminiSafety: boolean
  source?: string
  /** Instruction-file convention: AGENTS, SOUL, PERSONA, LOAD, … */
  kind?: string
  id?: string
  name?: string
  /** Models matching one of these rules resolve to this preset. */
  keywords?: JailbreakKeyword[]
  /** Models matching one of these rules never get this preset. */
  excludes?: JailbreakKeyword[]
  /** Explicit on/off switch. An absent value on a synced preset means off. */
  enabled?: boolean
  /** Operator arrangement position in the preset list. */
  order?: number
  /**
   * Where the prompt lands in the outbound body. "" and "system" prepend it as
   * the system prompt (the default a legacy or imported preset keeps);
   * "append" places it after the original content.
   */
  position?: string
  /**
   * What kind of turn the prompt travels as. "" sends it as a system prompt;
   * "chat" sends it as a scripted trailing user turn for endpoints with no
   * system prompt slot.
   */
  mode?: string
  /**
   * Scope of one client API key: empty is every key, a non-empty id makes this
   * preset visible only to requests that present that key. The resolver
   * prefers a key-scoped match over the global one.
   */
  apiKeyId?: string
  /** RFC3339 UTC stamp the store filled on first write. */
  createdAt?: string
  /** RFC3339 UTC stamp of the last write. */
  updatedAt?: string
  /**
   * Provenance of a preset the auto-sync worker wrote: the repository it came
   * from ("owner/repo@ref") and the file inside it. A preset carrying both is
   * rewritten by every pass of that source; one carrying neither was typed,
   * uploaded or imported by hand and sync never touches it.
   */
  syncOrigin?: string
  syncPath?: string
}

/** One GitHub collection the preset library auto-syncs from. */
export interface JailbreakSyncSource {
  id: string
  /** Anything the GitHub import accepts: owner/repo, a GitHub URL, or a raw file URL. */
  target: string
  /** Branch, tag or sha to follow. Empty follows the repository default. */
  ref?: string
  /** Paused sources are kept and never fetched. */
  enabled: boolean
  /** Label the panel shows; derived from the target when absent. */
  name?: string
  lastSyncAt?: string
  lastError?: string
  lastResult?: JailbreakSyncResult
}

/** What one pass of one source did. */
export interface JailbreakSyncResult {
  written: number
  updated: number
  orphaned: number
  failed: number
  files?: JailbreakSyncFile[]
}

/** One file of one pass. */
export interface JailbreakSyncFile {
  path: string
  preset?: string
  /** "written" | "updated" | "unchanged" | "orphaned" | "failed" */
  action: string
  reason?: string
}

/**
 * One model filter rule, shared by the include and exclude lists.
 * `mode` is `word` (default, whole token), `partial` (substring) or `regex`.
 */
export interface JailbreakKeyword {
  text: string
  mode?: string
}

/** One model's binding: whether injection is armed and which preset it uses. */
export interface JailbreakModelConfig {
  enabled: boolean
  presetId: string
  /** Empty is every key; a key-scoped binding outranks the same model's global one. */
  apiKeyId?: string
}

/** The global exclude list: models matching these rules are never injected. */
export interface JailbreakExcludes {
  excludes: JailbreakKeyword[]
}

/** One preset a GitHub import wrote. */
export interface JailbreakImportEntry {
  id: string
  name: string
  path: string
  /** Instruction-file convention: AGENTS, SOUL, PERSONA, LOAD, … */
  kind?: string
  keywords: string[]
}

/** One file a GitHub import skipped, with the reason. */
export interface JailbreakImportRefusal {
  path: string
  reason: string
}

/** The result of importing a prompt collection from GitHub. */
export interface JailbreakImportResult {
  ok: boolean
  source: string
  imported: JailbreakImportEntry[]
  refused: JailbreakImportRefusal[]
}

/** Why one model id resolved (or did not resolve) to a preset. */
export interface JailbreakMatch {
  injected: boolean
  /** global-exclude | exact-binding | key-binding | keyword | preset-exclude | no-match */
  reason: string
  presetId?: string
  presetName?: string
  keyword?: string
  matchedId?: string
  detail?: string
}
// One MiniMax Code account's last daily check-in outcome, as the background
// loop records it. Mirrors internal/minimaxcheckin.Result.
export interface MiniMaxCheckinResult {
  outcome: 'claimed' | 'done' | 'inactive' | 'ineligible' | 'failed'
  credit: number
  streak: number
  message?: string
  provider: string
  account: string
  accountId: string
  checkedAt: string
}
