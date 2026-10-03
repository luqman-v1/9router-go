<script lang="ts">
  // Per-provider outbound header overrides (issue #101 part 4, upstream
  // decolua/9router v0.5.95 b3cf3fde). Upstream calls this CustomConfigCard.
  import { api } from '../../api/client'
  import type { ProviderOverridesResponse } from '../../api/client'

  interface Props {
    isOpen: boolean
    providerId: string
    onClose: () => void
  }

  let { isOpen, providerId, onClose }: Props = $props()

  interface HeaderRow {
    key: string
    value: string
  }

  const MAX_HEADERS = 20
  const MAX_VALUE_LENGTH = 8192

  let builtin = $state<Record<string, string>>({})
  let blocked = $state<string[]>([])
  let rows = $state<HeaderRow[]>([])
  let loading = $state(false)
  let saving = $state(false)
  let error = $state('')
  let saved = $state(false)

  let blockedSet = $derived(new Set(blocked.map((b) => b.toLowerCase())))
  let hasRows = $derived(rows.some((r) => r.key.trim() !== ''))

  // A blank name is a row being typed, not a header. A blank value is not: the
  // server drops those, so sending them would show an override in the UI that
  // never reaches a request.
  let payload = $derived.by(() => {
    const out: Record<string, string> = {}
    for (const row of rows) {
      const key = row.key.trim()
      const value = row.value.trim()
      if (!key || !value) continue
      out[key] = value
    }
    return out
  })

  let tooMany = $derived(Object.keys(payload).length > MAX_HEADERS)

  // The same rules the server enforces, so the operator finds out in the field
  // rather than in a 400. Authorization/Cookie/Host and the framing headers
  // are excluded by the server too — this only stops the wasted round trip.
  function rowError(row: HeaderRow): string {
    const key = row.key.trim()
    if (!key) return ''
    if (!/^[A-Za-z0-9-]+$/.test(key)) return 'Letters, digits and dashes only.'
    if (blockedSet.has(key.toLowerCase())) return 'The gateway owns this header.'
    if (row.value.includes('\n') || row.value.includes('\r')) return 'No line breaks in a value.'
    if (row.value.length > MAX_VALUE_LENGTH) return `Max ${MAX_VALUE_LENGTH} characters.`
    return ''
  }

  let anyRowError = $derived(rows.some((r) => rowError(r) !== ''))

  $effect(() => {
    if (!isOpen) return
    void load()
  })

  async function load() {
    loading = true
    error = ''
    saved = false
    try {
      const data: ProviderOverridesResponse = await api.getProviderOverrides(providerId)
      builtin = data.builtinHeaders ?? {}
      blocked = data.blockedHeaders ?? []
      rows = Object.entries(data.headers ?? {}).map(([key, value]) => ({ key, value }))
      if (rows.length === 0) rows = [{ key: '', value: '' }]
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
      rows = [{ key: '', value: '' }]
    } finally {
      loading = false
    }
  }

  function addRow() {
    rows = [...rows, { key: '', value: '' }]
  }

  function removeRow(index: number) {
    const next = rows.filter((_, i) => i !== index)
    rows = next.length > 0 ? next : [{ key: '', value: '' }]
  }

  async function save() {
    if (saving || tooMany || anyRowError) return
    saving = true
    error = ''
    saved = false
    try {
      await api.saveProviderOverrides(providerId, payload)
      const next = Object.entries(payload)
      rows = next.length > 0 ? next.map(([key, value]) => ({ key, value })) : [{ key: '', value: '' }]
      saved = true
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      saving = false
    }
  }

  function handleKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape') onClose()
  }
</script>

{#if isOpen}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
    role="presentation"
    onclick={(e) => e.target === e.currentTarget && onClose()}
    onkeydown={handleKeyDown}
  >
    <div
      class="w-full max-w-2xl rounded-2xl border border-border bg-surface p-5 shadow-xl"
      role="dialog"
      aria-modal="true"
      aria-labelledby="provider-overrides-title"
    >
      <div class="mb-4 flex items-start justify-between gap-4">
        <div>
          <h3 id="provider-overrides-title" class="text-sm font-semibold text-text-main">Custom headers</h3>
          <p class="mt-0.5 text-xs text-text-muted">
            Added to every request this provider receives. An override replaces what the gateway
            sends.
          </p>
        </div>
        <button
          type="button"
          onclick={onClose}
          aria-label="Close"
          class="shrink-0 rounded-md p-1 text-text-muted hover:bg-surface-2 hover:text-text-main"
        >
          <span class="material-symbols-outlined text-[20px]" aria-hidden="true">close</span>
        </button>
      </div>

      {#if loading}
        <p class="py-6 text-center text-sm text-text-muted">Loading…</p>
      {:else}
        {#if Object.keys(builtin).length > 0}
          <details class="mb-4 rounded-lg border border-border-subtle bg-surface-2 px-3 py-2">
            <summary class="cursor-pointer text-xs font-medium text-text-muted">
              Sent by default ({Object.keys(builtin).length})
            </summary>
            <dl class="mt-2 space-y-1">
              {#each Object.entries(builtin) as [name, value] (name)}
                <div class="flex flex-wrap gap-x-2 text-[11px]">
                  <dt class="font-mono text-text-main">{name}</dt>
                  <dd class="truncate font-mono text-text-muted">{value}</dd>
                </div>
              {/each}
            </dl>
          </details>
        {/if}

        <div class="space-y-2">
          {#each rows as row, index (index)}
            <div class="flex flex-col gap-1">
              <div class="flex flex-wrap items-center gap-2">
                <input
                  type="text"
                  placeholder="Header name"
                  aria-label={`Header name ${index + 1}`}
                  value={row.key}
                  oninput={(e) => {
                    const value = (e.target as HTMLInputElement).value
                    rows = rows.map((r, i) => (i === index ? { ...r, key: value } : r))
                  }}
                  class="min-w-0 flex-1 rounded-lg border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-text-main outline-none focus:border-primary"
                />
                <input
                  type="text"
                  placeholder="Value"
                  aria-label={`Header value ${index + 1}`}
                  value={row.value}
                  oninput={(e) => {
                    const value = (e.target as HTMLInputElement).value
                    rows = rows.map((r, i) => (i === index ? { ...r, value } : r))
                  }}
                  class="min-w-0 flex-[2] rounded-lg border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-text-main outline-none focus:border-primary"
                />
                <button
                  type="button"
                  onclick={() => removeRow(index)}
                  aria-label={`Remove header ${index + 1}`}
                  class="shrink-0 rounded-md p-1.5 text-text-muted hover:bg-surface-2 hover:text-text-main"
                >
                  <span class="material-symbols-outlined text-[18px]" aria-hidden="true">delete</span>
                </button>
              </div>
              {#if rowError(row)}
                <p class="text-[11px] text-red-600 dark:text-red-400">{rowError(row)}</p>
              {/if}
            </div>
          {/each}
        </div>

        <div class="mt-3 flex items-center justify-between">
          <button
            type="button"
            onclick={addRow}
            disabled={rows.length >= MAX_HEADERS}
            class="inline-flex items-center gap-1 rounded-lg px-2 py-1 text-xs text-text-muted hover:bg-surface-2 hover:text-text-main disabled:opacity-50"
          >
            <span class="material-symbols-outlined text-[16px]" aria-hidden="true">add</span>
            Add header
          </button>
          <span class="text-[11px] text-text-muted">{Object.keys(payload).length} / {MAX_HEADERS}</span>
        </div>

        {#if tooMany}
          <p class="mt-2 text-[11px] text-red-600 dark:text-red-400">At most {MAX_HEADERS} headers.</p>
        {/if}
        {#if error}
          <p class="mt-2 text-[11px] text-red-600 dark:text-red-400" role="alert">{error}</p>
        {/if}
        {#if saved && !error}
          <p class="mt-2 text-[11px] text-text-muted" role="status">Saved.</p>
        {/if}
      {/if}

      <div class="mt-5 flex justify-end gap-2">
        <button
          type="button"
          onclick={onClose}
          class="rounded-lg px-3 py-1.5 text-xs font-semibold text-text-muted hover:bg-surface-2"
        >
          Close
        </button>
        <button
          type="button"
          onclick={save}
          disabled={saving || loading || tooMany || anyRowError || (!hasRows && saved)}
          class="rounded-lg bg-brand-500 px-3 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-primary-hover disabled:opacity-50"
        >
          {saving ? 'Saving…' : 'Save headers'}
        </button>
      </div>
    </div>
  </div>
{/if}
