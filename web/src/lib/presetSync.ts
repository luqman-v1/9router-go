import type { JailbreakSyncResult, JailbreakSyncSource } from '../api/client'

/**
 * The pure helpers behind the Persona page's auto-sync card.
 *
 * They live here rather than inside JailbreakView because the card's claims are
 * testable without a browser and without a gateway: "a source that never ran
 * says so" and "a failed pass leads with the reason" are the two the operator
 * reads before pressing anything, so they are pinned here.
 */

/**
 * What one source's last pass did, as the row describes it.
 *
 * A source that never ran says "not synced yet" rather than reading as idle and
 * healthy — a card that shows nothing for a source whose first pass has not
 * happened yet looks the same as one whose pass found nothing, and those two
 * mean different things to someone deciding whether to wait.
 */
export function syncSummaryText(source: JailbreakSyncSource): string {
  if (source.lastError) return `failed: ${source.lastError}`
  const r = source.lastResult
  if (!r) return 'not synced yet'
  const parts: string[] = []
  if (r.written) parts.push(`${r.written} written`)
  if (r.updated) parts.push(`${r.updated} updated`)
  if (r.orphaned) parts.push(`${r.orphaned} orphaned`)
  if (r.failed) parts.push(`${r.failed} failed`)
  return parts.length ? parts.join(' · ') : 'up to date'
}

/**
 * How long ago a pass ran, in the coarsest unit that still says something.
 * An unreadable or missing stamp reads "never", which is what a source that
 * has never synced should show.
 */
export function syncRelTime(iso?: string, now = Date.now()): string {
  if (!iso) return 'never'
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return 'never'
  const secs = Math.round((now - then) / 1000)
  if (secs < 60) return 'just now'
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`
  return `${Math.floor(secs / 86400)}d ago`
}

/** The label a row shows: the operator's name, else the target. */
export function syncLabel(source: JailbreakSyncSource): string {
  return source.name || source.target || 'repository'
}

/** The status the "Sync now" button reports after one pass. */
export function syncPassSummary(results: JailbreakSyncResult[]): string {
  let written = 0
  let updated = 0
  let orphaned = 0
  let failed = 0
  for (const r of results) {
    written += r.written
    updated += r.updated
    orphaned += r.orphaned
    failed += r.failed
  }
  const parts: string[] = []
  if (written) parts.push(`${written} written`)
  if (updated) parts.push(`${updated} updated`)
  if (orphaned) parts.push(`${orphaned} orphaned`)
  if (failed) parts.push(`${failed} failed`)
  return parts.length ? parts.join(' · ') : ''
}