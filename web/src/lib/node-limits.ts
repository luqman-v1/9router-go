// Draft helpers for a provider node's capacity limits.
//
// The form holds text (the shared Input component binds strings, and a date input
// speaks `YYYY-MM-DD`) while the API stores numbers and RFC3339. Both modals and
// the read-only summary on the provider page use these conversions, so the add
// path, the edit path and the summary cannot disagree about what a draft means.
import type { ProviderNodeLimits, ProviderNodeLimitsPatch } from '../api/client'

/**
 * A limits form state: the count fields, plus the contract date as `YYYY-MM-DD`.
 *
 * The counts are `string | number` on purpose. Svelte binds `<input type="number">`
 * as a number, so a field the operator types into holds 700 while the same field
 * the seed filled holds '700'. Declaring only one shape would be a lie the compiler
 * cannot catch at the boundary and the runtime catches as `e.trim is not a function`
 * — which is exactly how this feature first failed: every save with a typed limit
 * threw, the request never left, and the dialog closed showing nothing.
 */
export interface NodeLimitsDraft {
  rpm: string | number
  tpm: string | number
  concurrency: string | number
  tokenQuota: string | number
  queueTimeoutMs: string | number
  contractExpiresAt: string
}

/** Every field empty: a node that has never been limited. */
export const EMPTY_NODE_LIMITS: NodeLimitsDraft = {
  rpm: '',
  tpm: '',
  concurrency: '',
  tokenQuota: '',
  queueTimeoutMs: '',
  contractExpiresAt: '',
}

/** The stored policy as a draft, or the empty draft when nothing was read. */
export function nodeLimitsDraftOf(limits?: ProviderNodeLimits | null): NodeLimitsDraft {
  if (!limits) return { ...EMPTY_NODE_LIMITS }
  let expires = ''
  if (limits.contractExpiresAt) {
    const at = new Date(limits.contractExpiresAt)
    expires = Number.isNaN(at.getTime()) ? limits.contractExpiresAt.slice(0, 10) : at.toISOString().slice(0, 10)
  }
  const count = (value: number) => (value > 0 ? String(value) : '')
  return {
    rpm: count(limits.rpm),
    tpm: count(limits.tpm),
    concurrency: count(limits.concurrency),
    tokenQuota: count(limits.tokenQuota),
    queueTimeoutMs: count(limits.queueTimeoutMs),
    contractExpiresAt: expires,
  }
}

/**
 * The write payload for a draft.
 *
 * The chosen day is inclusive, so the stored timestamp is that day's end in UTC:
 * midnight is the start of the day the operator picked, which in a positive-offset
 * timezone is the previous evening — a paying node offline a day early.
 *
 * Anything that is not a positive integer becomes 0 (unlimited), which is the same
 * meaning the server gives an absent field and the same meaning the form's hint
 * promises.
 */
export function nodeLimitsPatchOf(draft: NodeLimitsDraft): ProviderNodeLimitsPatch {
  return {
    rpm: countOf(draft.rpm),
    tpm: countOf(draft.tpm),
    concurrency: countOf(draft.concurrency),
    tokenQuota: countOf(draft.tokenQuota),
    queueTimeoutMs: countOf(draft.queueTimeoutMs),
    contractExpiresAt: draft.contractExpiresAt ? `${draft.contractExpiresAt}T23:59:59Z` : '',
  }
}

/**
 * Whether a draft asks for anything at all. Three things must agree on this: the
 * limits section opening by itself, the add path following its create with a
 * second request, and the summary having something to say.
 */
export function hasAnyNodeLimit(draft: NodeLimitsDraft): boolean {
  return (
    countOf(draft.rpm) > 0 ||
    countOf(draft.tpm) > 0 ||
    countOf(draft.concurrency) > 0 ||
    countOf(draft.tokenQuota) > 0 ||
    countOf(draft.queueTimeoutMs) > 0 ||
    draft.contractExpiresAt !== ''
  )
}

/**
 * `hasAnyNodeLimit` for a finished patch. The dialogs hand their parent a plain
 * patch rather than the live draft, so "is anything set" travels with the values
 * instead of depending on which component still holds the object.
 */
export function hasAnyNodeLimitPatch(patch: ProviderNodeLimitsPatch): boolean {
  return (
    (patch.rpm ?? 0) > 0 ||
    (patch.tpm ?? 0) > 0 ||
    (patch.concurrency ?? 0) > 0 ||
    (patch.tokenQuota ?? 0) > 0 ||
    (patch.queueTimeoutMs ?? 0) > 0 ||
    (patch.contractExpiresAt ?? '') !== ''
  )
}

/** `60 rpm · 4 conc · 5M tok · ends 31 Jan 2027`, or `Unlimited`. */
export function nodeLimitsSummary(draft: NodeLimitsDraft): string {
  if (!hasAnyNodeLimit(draft)) return 'Unlimited'
  const parts: string[] = []
  const rpm = countOf(draft.rpm)
  const tpm = countOf(draft.tpm)
  const concurrency = countOf(draft.concurrency)
  const queueTimeoutMs = countOf(draft.queueTimeoutMs)
  const tokenQuota = countOf(draft.tokenQuota)
  if (rpm > 0) parts.push(`${rpm} rpm`)
  if (tpm > 0) parts.push(`${compactCount(tpm)} tpm`)
  if (concurrency > 0) parts.push(`${concurrency} conc`)
  if (queueTimeoutMs > 0) parts.push(`${queueTimeoutMs} ms queue`)
  if (tokenQuota > 0) parts.push(`${compactCount(tokenQuota)} tok`)
  if (draft.contractExpiresAt) parts.push(`ends ${shortDate(draft.contractExpiresAt)}`)
  return parts.join(' · ')
}

/** `620k / 5M tok · 3/60 this minute · 1/4 in flight`. */
export function nodeLimitsUsageSummary(limits: ProviderNodeLimits): string {
  const tokens =
    limits.tokenQuota > 0
      ? `${compactCount(limits.usedTokens)} / ${compactCount(limits.tokenQuota)} tok`
      : `${compactCount(limits.usedTokens)} tok`
  const minute = limits.rpm > 0 ? `${limits.rpmUsedWindow}/${limits.rpm} this minute` : `${limits.rpmUsedWindow} this minute`
  const flight =
    limits.concurrency > 0
      ? `${limits.activeConcurrency}/${limits.concurrency} in flight`
      : `${limits.activeConcurrency} in flight`
  return `${tokens} · ${minute} · ${flight}`
}
// A count field as a number: anything that is not a whole positive number means
// unlimited. Number() rather than parseInt(), because parseInt('1.5.2') reads 1 —
// a typo would quietly throttle the node to one request a minute instead of
// leaving it unlimited.
//
// Both shapes are accepted because both reach here: a number from an
// `<input type="number">` binding and a string from the seed or a cleared field.
function countOf(field: string | number): number {
  const value = typeof field === 'number' ? field : Number(field.trim())
  return Number.isInteger(value) && value > 0 ? value : 0
}

function compactCount(value: number): string {
  if (value >= 1_000_000) return `${Math.round(value / 100_000) / 10}M`.replace('.0M', 'M')
  if (value >= 1_000) return `${Math.round(value / 100) / 10}k`.replace('.0k', 'k')
  return String(value)
}

/** `31 Jan 2027` from `2027-01-31`. */
function shortDate(date: string): string {
  const at = new Date(`${date}T00:00:00Z`)
  if (Number.isNaN(at.getTime())) return date
  return at.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
}
