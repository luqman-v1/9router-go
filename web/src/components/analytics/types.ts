export type MainTab = 'overview' | 'details'
export type PeriodPreset = 'today' | '24h' | '7d' | '30d' | '60d' | 'all'
// A custom entry is "<n>d" or "<n>h"; the server parses both, so the selector is
// not limited to the presets below.
export type Period = PeriodPreset | (string & {})
export type TableView = 'model' | 'account' | 'apiKey' | 'endpoint'
export type ViewMode = 'costs' | 'tokens'

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
}


export interface RequestDetailItem {
  id?: string
  status?: string
  timestamp?: string
  provider?: string
  model?: string
  latency?: {
    total?: number
    ttft?: number
  }
  tokens?: {
    prompt_tokens?: number
    completion_tokens?: number
    cached_tokens?: number
    cache_read_input_tokens?: number
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
  { value: 'account', label: 'Usage by Account' },
  { value: 'apiKey', label: 'Usage by API Key' },
  { value: 'endpoint', label: 'Usage by Endpoint' },
]

export function fmt(n?: number): string {
  return (n || 0).toLocaleString()
}

export function cachedTokensFor(detail: RequestDetailItem): number {
  return detail.tokens?.cached_tokens ?? detail.tokens?.cache_read_input_tokens ?? 0
}

export function fmtCost(n?: number): string {
  return '$' + (n || 0).toFixed(2)
}

export function timeAgo(timestamp?: string): string {
  if (!timestamp) return 'just now'
  const diff = Math.floor((Date.now() - new Date(timestamp).getTime()) / 1000)
  if (diff < 60) return `${Math.max(1, diff)}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}
