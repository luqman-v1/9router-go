// Plan-badge helpers shared by the Codex and Claude cards.
//
// Both providers report a tier code that needs turning into a label, and both
// attach it the same way: a `subscription: {plan, expiresAt}` block on the
// usage response. Only Codex has an expiry — Anthropic's OAuth profile reports
// the seat, never when it lapses — so `expiresAt` is optional here.
// Pure functions only; the component holds the markup.
export interface PlanBadgeView {
  plan?: string | null
  expiresAt?: string | null
}

// planLabel renders a tier code as a label a reader recognises. An unknown or
// empty tier yields null so the badge is not drawn rather than showing the code.
export function planLabel(plan: string | null | undefined): string | null {
  if (!plan) return null
  const key = plan.trim().toLowerCase()
  if (!key) return null
  switch (key) {
    case 'plus':
      return 'Plus'
    case 'pro':
      return 'Pro'
    case 'team':
      return 'Team'
    case 'enterprise':
      return 'Enterprise'
    case 'edu':
      return 'Edu'
    case 'free':
      return 'Free'
    // Claude seats: the server already sends the multiplier ("Max 20x").
    case 'max':
      return 'Max'
    default:
      return key.charAt(0).toUpperCase() + key.slice(1)
  }
}

// formatPlanExpiry renders when the plan lapses, in the same shape the reset
// credit card uses: the date first, then the countdown when it is still ahead.
// A past date reads as expired rather than as a negative countdown.
export function formatPlanExpiry(
  expiresAt: string | null | undefined,
  locale?: string,
  now: number = Date.now(),
): { absolute: string | null; relative: string | null; expired: boolean } {
  if (!expiresAt) return { absolute: null, relative: null, expired: false }
  const date = new Date(expiresAt)
  if (!Number.isFinite(date.getTime())) return { absolute: null, relative: null, expired: false }
  const diffMs = date.getTime() - now
  return {
    absolute: date.toLocaleString(locale, { dateStyle: 'medium' }),
    relative: diffMs > 0 ? formatDuration(diffMs) : null,
    expired: diffMs <= 0,
  }
}

// formatDuration coarsens a positive millisecond span to the unit that fits.
function formatDuration(diffMs: number): string {
  const minutes = Math.max(1, Math.ceil(diffMs / 60_000))
  if (minutes < 60) return `${minutes}m`
  const hours = Math.ceil(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.ceil(hours / 24)}d`
}

// getPlanBadge reads the subscription block off a usage response, tolerating
// the shapes an older server may still send.
export function getPlanBadge(raw: unknown): PlanBadgeView | null {
  const sub = (raw as { subscription?: { plan?: unknown; expiresAt?: unknown } } | undefined)
    ?.subscription
  if (!sub) return null
  const plan = typeof sub.plan === 'string' ? sub.plan : null
  const expiresAt = typeof sub.expiresAt === 'string' ? sub.expiresAt : null
  if (!plan && !expiresAt) return null
  return { plan, expiresAt }
}
