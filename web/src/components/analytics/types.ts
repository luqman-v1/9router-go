import { PROVIDER_CATALOG } from '../../lib/providers'

export type MainTab = 'overview' | 'details'
export type PeriodPreset = 'today' | '24h' | '7d' | '30d' | '60d' | 'all'
// A custom entry is "<n>d" or "<n>h"; the server parses both, so the selector is
// not limited to the presets below.
export type Period = PeriodPreset | (string & {})
export type TableView = 'model' | 'provider' | 'account' | 'apiKey' | 'endpoint'
export type ViewMode = 'costs' | 'tokens'

// ViewOption names one entry of a section's sub-view picker. The option carries
// its own icon so the closed control and the open list show the same mark.
export interface ViewOption {
  value: string
  label: string
  icon: string
}

export interface UsageItem {
  requests?: number
  promptTokens?: number
  completionTokens?: number
  cachedTokens?: number
  cost?: number
  lastUsed?: string
  rawModel?: string
  accountName?: string
  keyName?: string
  endpoint?: string
  provider?: string
  key?: string
}

export interface RecentRequestItem {
  status?: string
  model?: string
  provider?: string
  requestedModel?: string
  comboName?: string
  /** How long the request took, for completed requests only. */
  latencyMs?: number
  promptTokens?: number
  cachedTokens?: number
  completionTokens?: number
  timestamp?: string
}
export interface ActiveRequestItem {
  model?: string
  provider?: string
  account?: string
  count?: number
  /** What the client asked for: the combo name when a combo served it. */
  requestedModel?: string
  /** Set only when a combo served this request — the map animates on it. */
  comboName?: string
}


export interface FallbackAttemptItem {
  connectionId?: string
  account?: string
  provider?: string
  model?: string
  statusCode?: number | string
  error?: string
  latencyMs?: number
}

export interface RequestDetailItem {
  id?: string
  status?: string
  timestamp?: string
  startedAt?: string
  endedAt?: string
  provider?: string
  model?: string
  requestedModel?: string
  connectionId?: string
  connName?: string
  connEmail?: string
  account?: string
  apiKey?: string
  combo?: string
  protocol?: string
  cacheSource?: string
  cost?: number
  error?: string
  attempts?: FallbackAttemptItem[]
  latency?: {
    total?: number
    ttft?: number
  }
  tokens?: {
    prompt_tokens?: number
    completion_tokens?: number
    cached_tokens?: number
    cache_read_input_tokens?: number
    cache_creation_input_tokens?: number
    reasoning_tokens?: number
    original_input_tokens?: number
    compressed_input_tokens?: number
    saved_tokens?: number
    saved_percent?: number
  }
  request?: {
    messages?: Array<{ role?: string; content?: unknown }>
  }
  response?: {
    content?: string
    error?: string
    status?: number
  }
  [key: string]: unknown
}

export interface StatsData {
  totalRequests?: number
  totalPromptTokens?: number
  totalCompletionTokens?: number
  totalCachedTokens?: number
  totalCost?: number
  byProvider?: Record<string, UsageItem>
  byModel?: Record<string, UsageItem>
  byAccount?: Record<string, UsageItem>
  byApiKey?: Record<string, UsageItem>
  byEndpoint?: Record<string, UsageItem>
  activeRequests?: ActiveRequestItem[]
  recentRequests?: RecentRequestItem[]
  errorProvider?: string
  pending?: unknown
}

export const PERIODS: { value: PeriodPreset; label: string }[] = [
  { value: 'today', label: 'Today' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
  { value: '60d', label: 'Last 60 days' },
  { value: 'all', label: 'All time' },
]

const PERIOD_PATTERN = /^(\d{1,5})([dh])$/

// periodLabel names any window the server accepts, including a custom one. A
// value that is neither preset nor custom is still shown verbatim rather than
// blank, so the control can never render an empty selection.
export function periodLabel(period: Period): string {
  const preset = PERIODS.find((p) => p.value === period)
  if (preset) return preset.label
  const match = PERIOD_PATTERN.exec(period)
  if (!match) return period
  return match[2] === 'd' ? `Last ${match[1]} days` : `Last ${match[1]} hours`
}

// normalizeCustomPeriod turns free text into the <n>d / <n>h form the server
// understands. Returns null when the text is not a usable window, so the caller
// can reject the input instead of sending a period that silently means 7 days.
export function normalizeCustomPeriod(input: string): PeriodPreset | null {
  const text = input.trim().toLowerCase().replace(/^\s+/, '')
  if (!text) return null
  const match = PERIOD_PATTERN.exec(text)
  if (match && Number(match[1]) > 0) return `${Number(match[1])}${match[2]}` as PeriodPreset
  if (text === 'all') return 'all'
  return null
}

export const TABLE_OPTIONS: { value: TableView; label: string }[] = [
  { value: 'model', label: 'Usage by Model' },
  { value: 'provider', label: 'Usage by Provider' },
  { value: 'account', label: 'Usage by Account' },
  { value: 'apiKey', label: 'Usage by API Key' },
  { value: 'endpoint', label: 'Usage by Endpoint' },
]

// providerDisplayName resolves a stored provider id to the label the dashboard
// shows. Custom compatible nodes persist under a synthetic id
// ("openai-compatible-chat-<suffix>") whose suffix can be mixed-case ("Wildy"),
// while the catalog carries the display name for everything else. Lookups fold
// case so a lowercased caller key ("...-wildy") still matches the stored node
// row. Unknown ids pass through unchanged.
export function providerDisplayName(
  providerId?: string | null,
  nodes?: { id: string; name?: string }[]
): string {
  const id = (providerId || '').trim()
  if (!id) return 'unknown'
  const lower = id.toLowerCase()
  const node = nodes?.find((n) => n?.id === id || n?.id?.toLowerCase().trim() === lower)
  if (node?.name) return node.name
  const cat = PROVIDER_CATALOG.find(
    (p) => p.id === id || p.alias === id || p.id.toLowerCase() === lower || p.alias?.toLowerCase() === lower
  )
  return cat?.name || id
}

export function fmt(n?: number): string {
  return (n || 0).toLocaleString()
}

export function cachedTokensFor(detail: RequestDetailItem): number {
  return detail.tokens?.cached_tokens ?? detail.tokens?.cache_read_input_tokens ?? 0
}

export function timeAgo(timestamp?: string): string {
  if (!timestamp) return 'just now'
  const diff = Math.floor((Date.now() - new Date(timestamp).getTime()) / 1000)
  if (diff < 60) return `${Math.max(1, diff)}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

export function formatDuration(ms?: number | null): string {
  if (ms == null || isNaN(ms)) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

export function calculateTPS(
  completionTokens?: number | null,
  durationMs?: number | null,
  ttftMs?: number | null
): string | null {
  if (!completionTokens || completionTokens <= 0 || !durationMs || durationMs <= 0) return null
  const genTimeMs = ttftMs && ttftMs > 0 && ttftMs < durationMs ? durationMs - ttftMs : durationMs
  if (genTimeMs <= 0) return null
  const tps = (completionTokens / (genTimeMs / 1000)).toFixed(1)
  return `${tps} tps`
}

export function formatLocalTimestamp(ts?: string | null): string {
  if (!ts) return '—'
  try {
    const d = new Date(ts)
    if (isNaN(d.getTime())) return ts
    const dateStr = d.toLocaleDateString()
    const timeStr = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
    return `${dateStr} ${timeStr}`
  } catch {
    return ts
  }
}

export function formatDetailStatus(detail: RequestDetailItem | null | undefined): string {
  if (!detail) return '—'
  if (detail.status === 'success' || detail.status === 'ok') return '200 OK'
  const code = detail.response?.status ?? (detail.attempts?.length ? detail.attempts[detail.attempts.length - 1]?.statusCode : undefined)
  if (code != null) {
    const num = typeof code === 'string' ? parseInt(code, 10) : code
    if (!Number.isNaN(num)) {
      const reasons: Record<number, string> = {
        400: 'Bad Request',
        401: 'Unauthorized',
        402: 'Payment Required',
        403: 'Forbidden',
        404: 'Not Found',
        408: 'Request Timeout',
        409: 'Conflict',
        422: 'Unprocessable Entity',
        429: 'Too Many Requests',
        499: 'Client Closed',
        500: 'Internal Error',
        502: 'Bad Gateway',
        503: 'Service Unavailable',
        504: 'Gateway Timeout',
      }
      return reasons[num] ? `${num} ${reasons[num]}` : `${num} Error`
    }
  }
  return detail.status ? (detail.status.charAt(0).toUpperCase() + detail.status.slice(1)) : 'Error'
}

export function extractDetailError(detail: RequestDetailItem | null | undefined): { status?: number | string; message?: string } | null {
  if (!detail || detail.status === 'success' || detail.status === 'ok') return null
  const status = detail.response?.status ?? (detail.attempts?.length ? detail.attempts[detail.attempts.length - 1]?.statusCode : undefined)
  let lastAttemptErr: string | undefined
  if (detail.attempts?.length) {
    for (let i = detail.attempts.length - 1; i >= 0; i--) {
      if (detail.attempts[i]?.error) {
        lastAttemptErr = detail.attempts[i].error
        break
      }
    }
  }
  const message = detail.response?.error || detail.error || lastAttemptErr || detail.response?.content
  if (!status && !message) return null
  return { status, message }
}
