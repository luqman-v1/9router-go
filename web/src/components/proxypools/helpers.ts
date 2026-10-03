export function getStatusVariant(status: string | null | undefined): 'success' | 'error' | 'default' {
  if (status === 'active' || status === 'passed') return 'success'
  if (status === 'error' || status === 'failed') return 'error'
  return 'default'
}

export function getLatencyBadge(
  latency?: number | null
): { variant: 'success' | 'warning' | 'error'; text: string } | null {
  if (!latency || latency <= 0) return null
  const ms = Math.round(latency)
  if (ms < 300) {
    return { variant: 'success', text: `⚡ ${ms}ms` }
  }
  if (ms <= 800) {
    return { variant: 'warning', text: `⏳ ${ms}ms` }
  }
  return { variant: 'error', text: `🐢 ${ms}ms` }
}
