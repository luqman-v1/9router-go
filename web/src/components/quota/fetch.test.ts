import { describe, expect, it } from 'bun:test'
import { QUOTA_FETCH_CONCURRENCY, runQuotaFetchCycle, type QuotaFetchTarget } from './fetch'

const targets = (n: number): QuotaFetchTarget[] =>
  Array.from({ length: n }, (_, i) => ({ id: `conn-${i}`, provider: 'kiro' }))

/** A read that parks until released, so in-flight counts stay observable. */
interface Gate {
  promise: Promise<string>
  resolve: (value: string) => void
}

function deferred(): Gate {
  let resolve!: (value: string) => void
  const promise = new Promise<string>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

describe('runQuotaFetchCycle', () => {
  it('reads every target, however many are queued', async () => {
    const seen: string[] = []

    await runQuotaFetchCycle({
      targets: targets(50),
      signal: new AbortController().signal,
      concurrency: QUOTA_FETCH_CONCURRENCY,
      fetch: async (target) => {
        seen.push(target.id)
        return 'quota'
      },
      onResult: () => {},
    })

    // The report was "only the first ~10 of 50 fill in". Every target must be
    // visited exactly once, not just the ones that fit in the first wave.
    expect(seen.length).toBe(50)
    expect(new Set(seen).size).toBe(50)
  })
  it('never exceeds the configured concurrency', async () => {
    let inFlight = 0
    let peak = 0
    // Pre-made gates, drained in order. Releasing one read admits exactly one
    // more, so the in-flight count is a real measurement rather than a
    // same-tick artefact of a fetch that resolves synchronously.
    const gates: Gate[] = targets(20).map(() => deferred())
    let next = 0

    const cycle = runQuotaFetchCycle({
      targets: targets(20),
      signal: new AbortController().signal,
      concurrency: 4,
      fetch: async () => {
        const gate = gates[next++]
        inFlight++
        peak = Math.max(peak, inFlight)
        const value = await gate.promise
        inFlight--
        return value
      },
      onResult: () => {},
    })

    await Promise.resolve()
    await Promise.resolve()
    // Unbounded fan-out would have all 20 reads parked at once.
    expect(peak).toBe(4)

    for (let i = 0; i < 20; i++) {
      gates[i].resolve('quota')
      await Promise.resolve()
      await Promise.resolve()
    }
    await cycle
    expect(peak).toBe(4)
  })

  it('reports every read even when some fail', async () => {
    const ok: string[] = []
    const failed: string[] = []

    await runQuotaFetchCycle({
      targets: targets(10),
      signal: new AbortController().signal,
      fetch: async (target) => {
        if (Number(target.id.slice(-1)) % 2 === 0) throw new Error('upstream 500')
        return 'quota'
      },
      onResult: (target) => ok.push(target.id),
      onError: (target) => failed.push(target.id),
    })

    // A failed read must not abort the pass: one dead provider cannot be why
    // the other rows stay blank.
    expect(ok.length).toBe(5)
    expect(failed.length).toBe(5)
  })

  it('drops results and skips completion once the cycle is aborted', async () => {
    const controller = new AbortController()
    const results: string[] = []
    let completed = false
    const gates: Gate[] = []

    const cycle = runQuotaFetchCycle({
      targets: targets(30),
      signal: controller.signal,
      concurrency: 2,
      fetch: async () => {
        const gate = deferred()
        gates.push(gate)
        // The user changed page mid-refresh: this pass is no longer wanted.
        if (gates.length === 2) controller.abort()
        return gate.promise
      },
      onResult: (target) => results.push(target.id),
      onComplete: () => {
        completed = true
      },
    })

    // A late answer from a superseded pass must never reach the view — that
    // overwrite is what left rows showing the previous page's quotas.
    gates.forEach((g) => g.resolve('stale'))
    await cycle

    expect(results).toEqual([])
    expect(completed).toBe(false)
  })

  it('completes immediately when there is nothing to read', async () => {
    let completed = false

    await runQuotaFetchCycle({
      targets: [],
      signal: new AbortController().signal,
      fetch: async () => 'quota',
      onResult: () => {},
      onComplete: () => {
        completed = true
      },
    })

    // The tracker's "last updated" clock and busy flag hang off this callback,
    // so an empty page must still resolve it rather than leaving the spinner up.
    expect(completed).toBe(true)
  })
})
