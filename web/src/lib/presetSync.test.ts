import { describe, expect, it } from 'bun:test'
import { syncLabel, syncPassSummary, syncRelTime, syncSummaryText } from './presetSync'
import type { JailbreakSyncSource } from '../api/client'

const source = (over: Partial<JailbreakSyncSource> = {}): JailbreakSyncSource => ({
  id: 's1',
  target: 'togg53192-cmd/jailbreaks',
  enabled: true,
  ...over,
})

describe('syncSummaryText', () => {
  it('says a source that never ran has not run', () => {
    expect(syncSummaryText(source())).toBe('not synced yet')
  })

  it('leads with the reason when the last pass failed', () => {
    expect(syncSummaryText(source({ lastError: 'network unreachable' }))).toBe(
      'failed: network unreachable',
    )
  })

  it('describes a clean pass by what it wrote', () => {
    expect(syncSummaryText(source({ lastResult: { written: 3, updated: 2, orphaned: 1, failed: 0 } }))).toBe(
      '3 written · 2 updated · 1 orphaned',
    )
  })

  it('reads up to date when a pass found nothing to do', () => {
    expect(syncSummaryText(source({ lastResult: { written: 0, updated: 0, orphaned: 0, failed: 0 } }))).toBe(
      'up to date',
    )
  })

  it('omits the zero counts rather than listing them', () => {
    expect(syncSummaryText(source({ lastResult: { written: 5, updated: 0, orphaned: 0, failed: 0 } }))).toBe(
      '5 written',
    )
  })
})

describe('syncRelTime', () => {
  const now = Date.UTC(2026, 10, 22, 12, 0, 0)

  it('reads an absent stamp as never', () => {
    expect(syncRelTime(undefined, now)).toBe('never')
  })

  it('reads an unreadable stamp as never', () => {
    expect(syncRelTime('not a date', now)).toBe('never')
  })

  it('picks the coarsest unit that still says something', () => {
    expect(syncRelTime(new Date(now - 5_000).toISOString(), now)).toBe('just now')
    expect(syncRelTime(new Date(now - 5 * 60_000).toISOString(), now)).toBe('5m ago')
    expect(syncRelTime(new Date(now - 5 * 3_600_000).toISOString(), now)).toBe('5h ago')
    expect(syncRelTime(new Date(now - 3 * 86_400_000).toISOString(), now)).toBe('3d ago')
  })
})

describe('syncLabel', () => {
  it('shows the operator name when there is one', () => {
    expect(syncLabel(source({ name: 'My prompts' }))).toBe('My prompts')
  })

  it('falls back to the target', () => {
    expect(syncLabel(source())).toBe('togg53192-cmd/jailbreaks')
  })
})

describe('syncPassSummary', () => {
  it('sums the outcomes of several sources', () => {
    expect(
      syncPassSummary([
        { written: 2, updated: 0, orphaned: 0, failed: 0 },
        { written: 1, updated: 3, orphaned: 2, failed: 0 },
      ]),
    ).toBe('3 written · 3 updated · 2 orphaned')
  })

  it('is empty when nothing changed', () => {
    expect(syncPassSummary([{ written: 0, updated: 0, orphaned: 0, failed: 0 }])).toBe('')
  })
})