<script lang="ts">
  // Per-model capability editor (the "model editor"). An operator pins any of
  // the eleven capability fields on one provider+model pair; a field left on
  // Auto is absent from the payload, so detection still answers it. This is the
  // sparse model upstream keeps in MODEL_CAPS_OVERRIDE_META, ported to Svelte 5.
  import { api } from '../../api/client'
  import type { ModelCapsOverrideResponse } from '../../api/client'

  interface Props {
    isOpen: boolean
    providerId: string
    model: string
    onClose: () => void
    /** Called after a save so the parent can refresh the row's capability icons. */
    onSaved?: () => void
  }

  let { isOpen, providerId, model, onClose, onSaved }: Props = $props()

  // One editable field as the operator is editing it: `value` is a string for
  // both types so a number input and a text input share one state shape, and
  // an empty string is Auto.
  interface FieldState {
    key: string
    label: string
    desc: string
    type: 'boolean' | 'number'
    value: string // '' = Auto; 'true'/'false' for booleans; digits for numbers
    detected: string
  }

  let fields = $state<FieldState[]>([])
  let loading = $state(false)
  let saving = $state(false)
  let error = $state('')
  let saved = $state(false)
  let maxTokens = $state(10_000_000)

  $effect(() => {
    if (!isOpen) return
    void load()
  })

  async function load() {
    loading = true
    error = ''
    saved = false
    try {
      const data: ModelCapsOverrideResponse = await api.getModelCapsOverride(providerId, model)
      maxTokens = data.maxTokens ?? 10_000_000
      const stored = data.capsOverride ?? {}
      const detected = data.detected ?? {}
      fields = (data.fields ?? []).map((f) => {
        const pinned = stored[f.key]
        const detectedValue = detected[f.key]
        return {
          key: f.key,
          label: f.label,
          desc: f.desc,
          type: f.type,
          value:
            pinned === undefined || pinned === null
              ? ''
              : typeof pinned === 'boolean'
                ? String(pinned)
                : String(pinned),
          detected:
            detectedValue === undefined || detectedValue === null
              ? ''
              : typeof detectedValue === 'boolean'
                ? detectedValue
                  ? 'on'
                  : 'off'
                : String(detectedValue),
        }
      })
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
      fields = []
    } finally {
      loading = false
    }
  }

  // The payload carries only fields the operator actually pinned. An empty
  // string is Auto and must be absent, not sent as 0/false, or every model
  // would read as "supports nothing".
  let payload = $derived.by(() => {
    const out: Record<string, boolean | number> = {}
    for (const f of fields) {
      const raw = f.value.trim()
      if (raw === '') continue
      if (f.type === 'boolean') {
        if (raw === 'true') out[f.key] = true
        else if (raw === 'false') out[f.key] = false
      } else {
        const n = Number(raw)
        if (Number.isFinite(n) && n > 0 && n <= maxTokens) out[f.key] = Math.floor(n)
      }
    }
    return out
  })

  let anyNumberError = $derived(
    fields.some((f) => {
      if (f.type !== 'number' || f.value.trim() === '') return false
      const n = Number(f.value.trim())
      return !Number.isFinite(n) || n <= 0 || n > maxTokens
    }),
  )

  async function save() {
    if (saving || anyNumberError) return
    saving = true
    error = ''
    saved = false
    try {
      await api.saveModelCapsOverride(providerId, model, payload)
      saved = true
      onSaved?.()
      // Reload so the detected block reflects the override the server now
      // resolves, which is what the Auto placeholders show.
      await load()
      saved = true
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      saving = false
    }
  }

  function setValue(key: string, value: string) {
    fields = fields.map((f) => (f.key === key ? { ...f, value } : f))
  }

  function clearField(key: string) {
    fields = fields.map((f) => (f.key === key ? { ...f, value: '' } : f))
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
      aria-labelledby="model-caps-title"
    >
      <div class="mb-4 flex items-start justify-between gap-4">
        <div>
          <h3 id="model-caps-title" class="text-sm font-semibold text-text-main">Model capabilities</h3>
          <p class="mt-0.5 text-xs text-text-muted">
            Pin what this model supports. A field left on Auto keeps the detected value.
          </p>
          <code class="mt-1 inline-block max-w-full truncate rounded bg-sidebar px-1.5 py-0.5 font-mono text-[11px] text-text-muted">
            {providerId}/{model}
          </code>
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
      {:else if error && fields.length === 0}
        <p class="py-4 text-sm text-red-500">{error}</p>
      {:else}
        <div class="max-h-[60vh] space-y-2 overflow-y-auto pr-1">
          {#each fields as field (field.key)}
            <div class="flex items-start gap-3 rounded-lg border border-border-subtle px-3 py-2">
              <div class="min-w-0 flex-1">
                <p class="text-sm font-medium text-text-main">{field.label}</p>
                <p class="text-[11px] text-text-muted">{field.desc}</p>
                {#if field.detected}
                  <p class="mt-0.5 text-[11px] text-text-muted">
                    Auto: <span class="font-mono text-text-main">{field.detected}</span>
                  </p>
                {/if}
              </div>
              <div class="flex shrink-0 items-center gap-1.5">
                {#if field.type === 'boolean'}
                  <select
                    aria-label={`${field.label} value`}
                    value={field.value}
                    onchange={(e) => setValue(field.key, (e.target as HTMLSelectElement).value)}
                    class="rounded-lg border border-border bg-background px-2 py-1 text-xs text-text-main outline-none focus:border-primary"
                  >
                    <option value="">Auto</option>
                    <option value="true">Yes</option>
                    <option value="false">No</option>
                  </select>
                {:else}
                  <input
                    type="number"
                    min="1"
                    max={maxTokens}
                    step="1"
                    placeholder={field.detected || 'Auto'}
                    aria-label={`${field.label} value`}
                    value={field.value}
                    oninput={(e) => setValue(field.key, (e.target as HTMLInputElement).value)}
                    class="w-28 rounded-lg border border-border bg-background px-2 py-1 font-mono text-xs text-text-main outline-none focus:border-primary"
                  />
                {/if}
                {#if field.value !== ''}
                  <button
                    type="button"
                    onclick={() => clearField(field.key)}
                    aria-label={`Reset ${field.label} to Auto`}
                    class="rounded-md p-1 text-text-muted hover:bg-surface-2 hover:text-text-main"
                  >
                    <span class="material-symbols-outlined text-[18px]" aria-hidden="true">restart_alt</span>
                  </button>
                {/if}
              </div>
            </div>
          {/each}
        </div>

        {#if anyNumberError}
          <p class="mt-2 text-xs text-red-500">A token value must be a whole number between 1 and {maxTokens.toLocaleString()}.</p>
        {/if}
        {#if error}
          <p class="mt-2 text-xs text-red-500">{error}</p>
        {/if}
        {#if saved && !error}
          <p class="mt-2 text-xs text-emerald-600 dark:text-emerald-400">Saved.</p>
        {/if}

        <div class="mt-4 flex items-center justify-end gap-2">
          <button
            type="button"
            onclick={onClose}
            class="rounded-lg px-3 py-1.5 text-xs font-medium text-text-muted hover:bg-surface-2 hover:text-text-main"
          >
            Close
          </button>
          <button
            type="button"
            onclick={save}
            disabled={saving || anyNumberError}
            class="rounded-lg bg-primary px-3 py-1.5 text-xs font-semibold text-white hover:bg-primary-hover disabled:opacity-50"
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      {/if}
    </div>
  </div>
{/if}
