<script lang="ts">
  // Console Log page. Owns the authenticated SSE transport, the server log
  // level, and the buffer window; ConsoleLogStream owns how rows are read.
  import { Trash2 } from 'lucide-svelte'
  import ConsoleLogStream from './ConsoleLogStream.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import { getAuthHeaders, responseErrorMessage } from '../api/client'
  import { notifications } from '../lib/notifications'
  import { toConsoleEntry, type ConsoleEntry } from '../lib/consoleLog'

  const MAX_LINES = 200
  const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const

  let entries = $state<ConsoleEntry[]>([])
  let logLevel = $state<string>('info')
  let levelBusy = $state(false)
  let seq = 0

  // A reconnect replays the whole buffer as "init", so the old rows have to go
  // or the same line would appear twice.
  function adoptBuffer(raw: unknown[]): void {
    seq = 0
    entries = raw.map((item) => toConsoleEntry(item as Record<string, unknown>, seq++))
  }

  async function fetchLogLevel() {
    try {
      const res = await fetch('/api/translator/console-logs/level', {
        headers: getAuthHeaders(),
      })
      if (!res.ok) return
      const data = await res.json()
      if (typeof data.level === 'string') logLevel = data.level
    } catch {
      // keep default; the selector still writes a level
    }
  }

  async function handleLevelChange(e: Event) {
    const next = (e.target as HTMLSelectElement).value
    const prev = logLevel
    logLevel = next
    levelBusy = true
    try {
      const res = await fetch('/api/translator/console-logs/level', {
        method: 'PUT',
        headers: getAuthHeaders(),
        body: JSON.stringify({ level: next }),
      })
      if (!res.ok) throw new Error(await responseErrorMessage(res, `HTTP ${res.status}`))
      const data = await res.json()
      if (!data?.success) throw new Error('Log level update was rejected')
      logLevel = data.level || next
      notifications.success(`Log level set to ${logLevel} (no restart needed)`)
    } catch (err) {
      logLevel = prev
      notifications.error(`Failed to set log level: ${err instanceof Error ? err.message : err}`)
    } finally {
      levelBusy = false
    }
  }

  async function handleClear() {
    try {
      const res = await fetch('/api/translator/console-logs', {
        method: 'DELETE',
        headers: getAuthHeaders(),
      })
      if (!res.ok) throw new Error(await responseErrorMessage(res, `HTTP ${res.status}`))
      // The view clears via the SSE "clear" event, so the server stays the
      // single owner of the buffer.
    } catch (err) {
      notifications.error(`Failed to clear console logs: ${err instanceof Error ? err.message : err}`)
    }
  }

  fetchLogLevel()

  $effect(() => {
    let cancelled = false
    let controller: AbortController | null = null

    // Read through fetch so the stream rides the same authenticated transport
    // and error handling as the rest of the dashboard.
    const connect = async () => {
      try {
        controller = new AbortController()
        const res = await fetch('/api/translator/console-logs/stream', {
          headers: getAuthHeaders(),
          signal: controller.signal,
        })
        if (!res.ok) throw new Error(await responseErrorMessage(res, `HTTP ${res.status}`))

        const reader = res.body?.getReader()
        if (!reader) return

        const decoder = new TextDecoder()
        let buffer = ''
        while (!cancelled) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const frames = buffer.split('\n')
          buffer = frames.pop() || ''

          for (const frame of frames) {
            const trimmed = frame.trim()
            if (!trimmed.startsWith('data: ')) continue

            let msg: Record<string, unknown>
            try {
              msg = JSON.parse(trimmed.slice(6))
            } catch {
              continue
            }

            if (msg.type === 'init') {
              adoptBuffer(Array.isArray(msg.entries) ? msg.entries : [])
            } else if (msg.type === 'line') {
              entries = [...entries, toConsoleEntry(msg.entry as Record<string, unknown>, seq++)].slice(
                -MAX_LINES
              )
            } else if (msg.type === 'clear') {
              entries = []
            }
          }
        }
      } catch {
        // stream closed or aborted — reconnect below
      }

      if (!cancelled) setTimeout(connect, 5000)
    }

    connect()

    return () => {
      cancelled = true
      controller?.abort()
    }
  })
</script>

<Card padding="none" class="flex flex-col">
  <div class="flex flex-wrap items-center gap-2 border-b border-border-subtle px-4 py-2.5">
    <label class="text-xs text-text-muted" for="console-log-level">Level</label>
    <select
      id="console-log-level"
      class="rounded-[8px] border border-border bg-surface px-2 py-1 text-xs text-text-main disabled:opacity-50"
      bind:value={logLevel}
      disabled={levelBusy}
      onchange={handleLevelChange}
    >
      {#each LOG_LEVELS as level (level)}
        <option value={level}>{level}</option>
      {/each}
    </select>
    <span class="text-[11px] text-text-muted">
      Applies immediately — lines below this level are not buffered.
    </span>

    <Button size="sm" variant="outline" class="ml-auto" onclick={handleClear}>
      <Trash2 class="h-3.5 w-3.5" />
      Clear
    </Button>
  </div>

  <ConsoleLogStream {entries} />
</Card>