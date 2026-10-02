// Bounded quota fan-out for the tracker.
//
// The tracker refreshes every visible connection at once, and the gateway paces
// those reads (internal/fetchgate, ~250ms apart) so a burst from one IP does not
// look like a fleet. With ACCOUNT_PAGE_SIZE_MAX = 500 that pacing alone is a
// two-minute queue, and two unbounded fan-outs over it were the shape behind the
// report in #78 item 3: the browser opened hundreds of sockets at once, the
// later ones outlived the earlier cycle's render, and the rows the user was
// looking at stayed blank until a manual refresh.
//
// Two rules make a cycle survive a page that is large, slow, or refreshed while
// still loading:
//
//   - bounded concurrency, so the socket count follows the page size instead of
//     how fast the gateway answers;
//   - an abort signal, so a superseded pass stops writing state as soon as the
//     pass that replaced it starts, and drops the reads it no longer needs.
//
// The second is what was actually missing. The previous code set `loading` for
// every row up front and cleared it per row as answers landed, so rows still in
// flight rendered as finished — blank, no spinner, no error — and a late answer
// from the abandoned page overwrote the current page's rows.

// Max quota reads in flight at once. Six is what a browser keeps against a
// single origin anyway, so it costs nothing and makes the load predictable when
// the dashboard is served over HTTP/1.1.
export const QUOTA_FETCH_CONCURRENCY = 6

export interface QuotaFetchTarget {
  id: string
  provider: string
}

export interface QuotaFetchOptions<T> {
  targets: QuotaFetchTarget[]
  /**
   * Aborts the cycle. The caller owns it so a newer pass can cancel this one
   * before it starts; every write below is skipped once it is aborted, which is
   * what keeps a superseded pass from overwriting its replacement.
   */
  signal: AbortSignal
  concurrency?: number
  /**
   * Runs one quota read. Rejections are reported through `onError`; aborts are
   * absorbed, because they mean this pass was superseded, not that a read
   * failed.
   */
  fetch: (target: QuotaFetchTarget, force: boolean) => Promise<T>
  /** Receives each settled value in completion order. Ignored once aborted. */
  onResult: (target: QuotaFetchTarget, value: T, force: boolean) => void
  /** Receives a read that rejected. Ignored once aborted. */
  onError?: (target: QuotaFetchTarget, error: unknown, force: boolean) => void
  force?: boolean
  /** Fires once the last target has settled. Ignored once aborted. */
  onComplete?: (force: boolean) => void
}

/**
 * Runs every target through `fetch`, at most `concurrency` at a time, reporting
 * each outcome as it lands and stopping as soon as `signal` aborts.
 */
export async function runQuotaFetchCycle<T>(options: QuotaFetchOptions<T>): Promise<void> {
  const { targets, signal } = options
  const force = options.force === true

  if (targets.length === 0) {
    options.onComplete?.(force)
    return
  }

  const concurrency = Math.max(1, options.concurrency ?? QUOTA_FETCH_CONCURRENCY)
  let cursor = 0
  const workers = Array.from({ length: Math.min(concurrency, targets.length) }, async () => {
    while (cursor < targets.length) {
      const target = targets[cursor++]
      try {
        const value = await options.fetch(target, force)
        if (signal.aborted) return
        options.onResult(target, value, force)
      } catch (error) {
        if (signal.aborted) return
        options.onError?.(target, error, force)
      }
    }
  })

  await Promise.all(workers)

  if (signal.aborted) return
  options.onComplete?.(force)
}
