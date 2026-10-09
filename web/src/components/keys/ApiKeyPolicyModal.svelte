<script lang="ts">
  import { api, type APIKey, type APIKeyPolicy } from '../../api/client'
  import { Loader2, Save, X } from 'lucide-svelte'

  interface Props {
    apiKey: APIKey
    onClose: () => void
    onSaved: () => void
  }

  let { apiKey, onClose, onSaved }: Props = $props()

  // 0 means unlimited everywhere, so the initial value of an unset column is
  // 0 rather than blank: the operator sees "no limit" without guessing.
  let rpm = $state(apiKey.rateLimitRpm ?? 0)
  let tpm = $state(apiKey.rateLimitTpm ?? 0)
  let concurrency = $state(apiKey.rateLimitConcurrency ?? 0)

  // expiresAt is stored as RFC3339. The input is a plain datetime-local because
  // the browser needs a local time and the server needs UTC; convert at the edge
  // instead of asking the operator to type a timezone.
  let expiresLocal = $state(toLocalInput(apiKey.expiresAt))
  let metadata = $state(apiKey.metadata ?? '')
  // Renaming lives here rather than in the table's row menu: the issue asked
  // for one dialog per key that edits the name and shows the policy, so
  // splitting it across two dialogs would undo that.
  let name = $state(apiKey.name ?? '')
  let models = $state<string[]>([])
  let modelDraft = $state('')
  let isLoadingModels = $state(false)
  let isSaving = $state(false)
  let error = $state<string | null>(null)
  // Whether the stored allowlist has been fetched. A key's allowlist is part of
  // the policy this modal edits, so it has to be present on open rather than
  // behind a button press.
  let hasLoadedModels = $state(false)

  function toLocalInput(rfc3339?: string): string {
    if (!rfc3339) return ''
    const parsed = new Date(rfc3339)
    if (Number.isNaN(parsed.getTime())) return ''
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())}T${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`
  }

  function toRFC3339(local: string): string {
    if (!local) return ''
    const parsed = new Date(local)
    if (Number.isNaN(parsed.getTime())) return ''
    return parsed.toISOString()
  }

  function nonNegative(value: number): number {
    return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
  }

  async function loadModels() {
    isLoadingModels = true
    try {
      const res = await api.getApiKeyModels(apiKey.id)
      models = res.models ?? []
      // Only a fetch that actually returned may unblock the save. Marking it
      // on failure too would leave `models` empty while the save believed the
      // list was authoritative — and "Save Policy" would then write that empty
      // list, deleting a real allowlist. A failed load must block the write and
      // say so, not silently replace a restriction with "allow everything".
      hasLoadedModels = true
      error = null
    } catch (err) {
      error = `Could not load the model allowlist: ${err instanceof Error ? err.message : String(err)}. Saving the policy will not change the allowlist — press Reload first.`
    } finally {
      isLoadingModels = false
    }
  }

  // An existing allowlist is part of the policy this modal edits, exactly like
  // the rate limits, so it has to be fetched on open. The list used to start
  // empty and only filled after "Load" was pressed, so the modal claimed "every
  // model is allowed" for a key that was in fact restricted — and the empty
  // draft that resulted then overwrote the stored list on the next save.
  $effect(() => {
    if (!hasLoadedModels) loadModels()
  })

  async function save(e: SubmitEvent) {
    e.preventDefault()
    error = null
    const metadataValue = metadata.trim()
    if (metadataValue) {
      try {
        const parsed = JSON.parse(metadataValue)
        if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
          error = 'Metadata must be a JSON object, e.g. {"customerId":"acme"}'
          return
        }
      } catch {
        error = 'Metadata is not valid JSON'
        return
      }
    }

    // The allowlist may only be written once it has actually been read back.
    // That covers both an in-flight fetch (nothing loaded yet) and a failed one
    // (the list is empty because we never got it, not because the key is
    // unrestricted). In either case writing `models` would replace a real
    // restriction with an empty list, so the save is refused and the operator is
    // told to retry rather than being shown a success that lost data.
    if (!hasLoadedModels) {
      error = isLoadingModels
        ? 'Still loading the model allowlist — try Save again in a moment.'
        : 'The model allowlist could not be read, so saving now could erase it. Press Reload, then Save.'
      return
    }

    try {
      isSaving = true
      const policy: APIKeyPolicy = {
        rateLimitRpm: nonNegative(rpm),
        rateLimitTpm: nonNegative(tpm),
        rateLimitConcurrency: nonNegative(concurrency),
        expiresAt: toRFC3339(expiresLocal),
        metadata: metadataValue,
        name: name.trim(),
      }
      await api.updateApiKeyPolicy(apiKey.id, policy)
      // The allowlist is part of the same policy the operator pressed Save for.
      // Leaving it out meant a pattern added in this dialog was accepted with a
      // 200 and then never stored, and — because the list started empty — the
      // very next save also wiped whatever was already configured.
      await api.setApiKeyModels(apiKey.id, models)
      onSaved()
      onClose()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isSaving = false
    }
  }

  function addModel() {
    const value = modelDraft.trim()
    if (!value || models.includes(value)) return
    models = [...models, value]
    modelDraft = ''
  }

  // Escape closes the dialog, matching every other modal in this dashboard.
  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') onClose()
  }
</script>

<svelte:window onkeydown={onKeydown} />

<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-md p-4">
  <div class="w-full max-w-2xl max-h-[90vh] overflow-y-auto rounded-2xl bg-surface-2 border border-border shadow-2xl">
    <div class="sticky top-0 bg-surface-2 border-b border-border px-6 py-4 flex items-center justify-between z-10">
      <div class="min-w-0">
        <h2 class="font-headline text-sm font-bold text-text-main truncate">
          Policy — {apiKey.name || apiKey.keyDisplay || apiKey.key}
        </h2>
        <p class="font-code text-[10px] text-text-subtle truncate">{apiKey.id}</p>
      </div>
      <button
        type="button"
        onclick={onClose}
        aria-label="Close policy dialog"
        class="p-1.5 rounded-lg text-text-subtle hover:text-text-main hover:bg-surface-3 transition cursor-pointer"
      >
        <X class="w-4 h-4" />
      </button>
    </div>

    <form onsubmit={save} class="px-6 py-5 space-y-6 font-body text-xs">

      <!-- Rename. The name is dashboard-only, and an empty value clears it so
           the row falls back to the masked token. -->
      <section class="space-y-3">
        <div>
          <h3 class="font-headline text-xs font-bold text-text-main">Name</h3>
          <p class="text-text-subtle mt-0.5">
            Shown in this dashboard only. Clearing it falls back to the masked token.
          </p>
        </div>
        <input
          id="key-name"
          type="text"
          bind:value={name}
          maxlength="200"
          placeholder="Client Token"
          aria-label="Key name"
          class="w-full bg-surface border border-border rounded-lg px-3 py-2 text-xs text-text-main focus:outline-none focus:border-brand-500"
        />
      </section>

      <!-- Rate limits -->
      <section class="space-y-3">
        <div>
          <h3 class="font-headline text-xs font-bold text-text-main">Rate limits</h3>
          <p class="text-text-subtle mt-0.5">
            Applied per key. <span class="font-code">0</span> means unlimited — a rejected request
            returns 429 with <span class="font-code">Retry-After</span> and never reaches the provider.
          </p>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <div>
            <label for="pol-rpm" class="block font-semibold text-text-muted mb-1">
              Requests / minute
            </label>
            <input
              id="pol-rpm"
              type="number"
              min="0"
              step="1"
              bind:value={rpm}
              class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
            />
          </div>
          <div>
            <label for="pol-tpm" class="block font-semibold text-text-muted mb-1">
              Tokens / minute
            </label>
            <input
              id="pol-tpm"
              type="number"
              min="0"
              step="1"
              bind:value={tpm}
              class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
            />
          </div>
          <div>
            <label for="pol-conc" class="block font-semibold text-text-muted mb-1">
              Concurrency
            </label>
            <input
              id="pol-conc"
              type="number"
              min="0"
              step="1"
              bind:value={concurrency}
              class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
            />
          </div>
        </div>
      </section>

      <!-- Expiry -->
      <section class="space-y-3">
        <div>
          <h3 class="font-headline text-xs font-bold text-text-main">Contract expiry</h3>
          <p class="text-text-subtle mt-0.5">
            After this instant the key is rejected with 401. Leave empty for a key that never expires.
          </p>
        </div>
        <input
          type="datetime-local"
          bind:value={expiresLocal}
          aria-label="Expiry date and time"
          class="w-full sm:w-64 bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
        />
      </section>

      <!-- Metadata -->
      <section class="space-y-3">
        <div>
          <h3 class="font-headline text-xs font-bold text-text-main">Resale metadata</h3>
          <p class="text-text-subtle mt-0.5">
            Free-form JSON kept with the key, e.g.
              <span class="font-code">&#123;"customerId":"acme","priceCents":5000&#125;</span>
          </p>
        </div>
        <textarea
          bind:value={metadata}
          rows="3"
          spellcheck="false"
          placeholder={'{\n  "customerId": "acme",\n  "priceCents": 5000\n}'}
          class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-[11px] text-text-main focus:outline-none focus:border-brand-500 resize-y"
        ></textarea>
      </section>

      <!-- Model allowlist -->
      <section class="space-y-3">
        <div class="flex items-start justify-between gap-3">
          <div>
            <h3 class="font-headline text-xs font-bold text-text-main">Model access</h3>
            <p class="text-text-subtle mt-0.5">
              Patterns may use <span class="font-code">*</span> per segment, e.g.
              <span class="font-code">e2e/allowed-*</span>. An empty list allows every model.
            </p>
          </div>
          <button
            type="button"
            onclick={loadModels}
            disabled={isLoadingModels}
            class="shrink-0 px-2.5 py-1.5 rounded-lg border border-border text-[11px] text-text-muted hover:text-text-main hover:border-brand-500/40 transition cursor-pointer disabled:opacity-50"
          >
            {#if isLoadingModels}
              <Loader2 class="w-3.5 h-3.5 animate-spin inline" />
            {:else}
              Reload
            {/if}
          </button>
        </div>

        <div class="flex gap-2">
          <input
            type="text"
            bind:value={modelDraft}
            onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), addModel())}
            placeholder="e2e/allowed-*"
            class="flex-1 bg-surface border border-border rounded-lg px-3 py-2 font-code text-[11px] text-text-main focus:outline-none focus:border-brand-500"
          />
          <button
            type="button"
            onclick={addModel}
            class="px-3 py-2 rounded-lg bg-surface-3 border border-border text-[11px] text-text-main hover:border-brand-500/40 transition cursor-pointer"
          >
            Add
          </button>
        </div>

        {#if models.length > 0}
          <div class="flex flex-wrap gap-1.5">
            {#each models as pattern, i (pattern)}
              <span
                class="inline-flex items-center gap-1.5 px-2 py-1 rounded-lg bg-brand-500/10 border border-brand-500/25 font-code text-[11px] text-brand-500"
              >
                {pattern}
                <button
                  type="button"
                  onclick={() => (models = models.filter((_, idx) => idx !== i))}
                  aria-label="Remove {pattern}"
                  class="hover:text-danger transition cursor-pointer"
                >
                  <X class="w-3 h-3" />
                </button>
              </span>
            {/each}
          </div>
        {:else}
          <p class="text-text-subtle font-code text-[11px]">
            {#if isLoadingModels}Loading allowlist…{:else}No allowlist — every model is allowed.{/if}
          </p>
        {/if}

      </section>

      {#if error}
        <p class="text-danger text-[11px] border border-danger/30 bg-danger/5 rounded-lg px-3 py-2">
          {error}
        </p>
      {/if}

      <div class="flex justify-end gap-2 pt-3 border-t border-border">
        <button
          type="button"
          onclick={onClose}
          class="px-4 py-2 rounded-lg text-text-muted hover:text-text-main cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={isSaving}
          class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-bold shadow-md shadow-brand-500/25 transition cursor-pointer disabled:opacity-60"
        >
          {#if isSaving}
            <Loader2 class="w-3.5 h-3.5 animate-spin" />
          {:else}
            <Save class="w-3.5 h-3.5" />
          {/if}
          <span>Save Policy</span>
        </button>
      </div>
    </form>
  </div>
</div>