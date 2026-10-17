<script lang="ts">
  // GitHub-style activity heatmap for the Details section (issue #280): a
  // trailing-12-month grid of daily request counts, ported from
  // techysy/10router's ActivityHeatmap. Cells are plain divs — the dashboard
  // ships no chart dependency.
  import type { UsageDailyPoint } from '../../api/client'
  import { fmtTokens } from '../../lib/compactNumber'

  interface Props {
    daily: UsageDailyPoint[]
    currentStreak?: number
  }

  let { daily = [], currentStreak = 0 }: Props = $props()

  const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']

  const byDate = $derived(new Map(daily.map((d) => [d.date, d])))
  const maxRequests = $derived(Math.max(1, ...daily.map((d) => d.requests)))
  interface Week {
    month: string
    cells: (UsageDailyPoint | null)[]
  }

  // Weeks run oldest → newest, cells top-to-bottom Mon..Sun. The grid starts
  // on the Monday of the earliest active week and ends with the last active
  // day, so empty days inside the window still render as level-0 cells.
  const weeks = $derived.by((): Week[] => {
    if (daily.length === 0) return []
    const keys = [...byDate.keys()].sort()
    const start = new Date(keys[0] + 'T00:00:00Z')
    const end = new Date(keys[keys.length - 1] + 'T00:00:00Z')
    // Walk back to the Monday of the start week (getUTCDay: Sun=0).
    start.setUTCDate(start.getUTCDate() - ((start.getUTCDay() + 6) % 7))
    const out: Week[] = []
    const cursor = new Date(start)
    let lastMonth = -1
    while (cursor <= end) {
      const cells: (UsageDailyPoint | null)[] = []
      let month = ''
      for (let dow = 0; dow < 7; dow++) {
        const key = cursor.toISOString().slice(0, 10)
        cells.push(byDate.get(key) || null)
        const m = cursor.getUTCMonth()
        if (m !== lastMonth) {
          if (month === '') month = monthName(m)
          lastMonth = m
        }
        cursor.setUTCDate(cursor.getUTCDate() + 1)
      }
      out.push({ month, cells })
    }
    return out
  })

  function monthName(m: number): string {
    return ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][m]
  }

  function levelOf(p: UsageDailyPoint | null): number {
    if (!p || p.requests === 0) return 0
    return Math.min(4, Math.ceil((p.requests / maxRequests) * 4))
  }

  const LEVEL_CLASS = [
    'bg-black/[0.07] dark:bg-white/[0.08]',
    'bg-brand-500/25',
    'bg-brand-500/50',
    'bg-brand-500/75',
    'bg-brand-500',
  ]

  function tooltip(p: UsageDailyPoint | null): string {
    if (!p) return 'No activity'
    return `${p.date}: ${p.requests.toLocaleString()} requests, ${fmtTokens(p.tokens)} tokens`
  }
</script>

<div class="flex min-w-0 flex-col gap-2">
  {#if weeks.length === 0}
    <p class="py-6 text-center text-sm text-text-muted">No activity in the selected period</p>
  {:else}
    <div class="min-w-0 overflow-x-auto">
      <div class="flex gap-[3px] min-w-max">
        <div class="flex flex-col gap-[3px] pt-[15px] text-[9px] text-text-subtle w-7 shrink-0 select-none">
          {#each WEEKDAYS as wd, i}
            <div class="h-[12px] leading-[12px]">{i % 2 === 1 ? wd : ''}</div>
          {/each}
        </div>
        {#each weeks as week}
          <div class="flex flex-col gap-[3px]">
            <div class="h-[12px] text-[9px] leading-[12px] text-text-subtle select-none whitespace-nowrap">
              {week.month}
            </div>
            {#each week.cells as cell}
              <div
                class="w-[12px] h-[12px] rounded-[2px] {LEVEL_CLASS[levelOf(cell)]}"
                title={tooltip(cell)}
              ></div>
            {/each}
          </div>
        {/each}
      </div>
    </div>
    <div class="flex items-center justify-between text-[10px] text-text-subtle">
      <span>{currentStreak > 0 ? `${currentStreak}-day streak` : 'No active streak'}</span>
      <span class="flex items-center gap-1">
        Less
        {#each LEVEL_CLASS as cls}
          <span class="w-[10px] h-[10px] rounded-[2px] {cls}"></span>
        {/each}
        More
      </span>
    </div>
  {/if}
</div>
