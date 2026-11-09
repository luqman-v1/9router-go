<script lang="ts">
  // Port of upstream AddCustomModelModal.js (dashboard/providers/[id]).
  import Button from '../../lib/ui/Button.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import { api, type CustomModelBatchEntry } from '../../api/client'
  import { parseModelIds, summarizeBulkModels } from '../../lib/bulk-models'

  export interface ModelCaps {
    vision: boolean
    reasoning: boolean
    jailbreak: boolean
  }

  /** The operator's own declaration of what the endpoint takes. 0 = unknown. */
  export interface ModelLimits {
    contextWindow: number
    maxOutput: number
  }

  interface Props {
    isOpen: boolean
    /** Storage alias of the provider (e.g. "oc") — used to strip the alias prefix and to test. */
    providerAlias: string
    onSave: (modelId: string, caps: ModelCaps, limits: ModelLimits) => Promise<void> | void
    /** Called after a bulk save, so the parent reloads the list the paste changed. */
    onBulkSaved?: () => void
    onClose: () => void
  }

  let { isOpen, providerAlias, onSave, onBulkSaved, onClose }: Props = $props()

  // Port of CAPACITY_META (upstream shared/constants/models.js).
  const CAPACITY_META: Record<string, { label: string; desc: string }> = {
    vision: { label: 'Vision', desc: 'Supports image input' },
    reasoning: { label: 'Reasoning', desc: 'Supports reasoning / thinking' },
    jailbreak: { label: 'Jailbreak', desc: 'Uncensored / unrestricted — accepts prompts a filtered model refuses' },
  }

  const defaultCaps = (): ModelCaps => ({ vision: false, reasoning: false, jailbreak: false })
  const defaultLimits = (): ModelLimits => ({ contextWindow: 0, maxOutput: 0 })

  let modelId = $state('')
  let caps = $state<ModelCaps>(defaultCaps())
  let limits = $state<ModelLimits>(defaultLimits())
  let testStatus = $state<'testing' | 'ok' | 'error' | null>(null)
  let testError = $state('')
  let saving = $state(false)

  // One field, one rule: whatever is typed or pasted is parsed, and the shape decides
  // what happens. The first version put this behind a `Single | Bulk Add` switch that
  // defaulted to Single, so pasting "model1 model2 model3" saved one model whose id was
  // that whole string — the operator had to know the switch existed before the feature
  // could help them. A separator in the field is now the trigger, which is what pasting
  // a column of ids looks like anyway.
  let bulkSaving = $state(false)
  let bulkError = $state('')
  let bulkResult = $state<{ added: string[]; skipped: Array<{ id: string; reason: string; code: string }> } | null>(
    null,
  )
  let plan = $derived(parseModelIds(modelId, providerAlias))
  // More than one id, or something the paste half-failed on: either way the answer is a
  // list, so the list is shown before anything is written.
  let isPaste = $derived(plan.ids.length > 1 || plan.duplicates.length > 0 || plan.invalid.length > 0)

  function parseLimit(raw: string): number {
    const n = Number.parseInt(raw, 10)
    return Number.isFinite(n) && n > 0 ? n : 0
  }

  // max_output_tokens is drawn from the same window, so declaring both with
  // the output larger than the window is a contradiction rather than a value
  // the gateway can publish.
  let limitsError = $derived(
    limits.contextWindow > 0 && limits.maxOutput > limits.contextWindow
      ? 'Max output cannot exceed the context window.'
      : '',
  )

  // Reset state when modal opens (upstream parity).
  $effect(() => {
    if (isOpen) {
      modelId = ''
      caps = defaultCaps()
      limits = defaultLimits()
      testStatus = null
      testError = ''
      bulkResult = null
      bulkError = ''
    }
  })

  /** Strip provider's own alias prefix (e.g. "cc/model" -> "model" for cc provider). */
  function stripAlias(id: string): string {
    const prefix = `${providerAlias}/`
    return id.startsWith(prefix) ? id.slice(prefix.length) : id
  }

  let cleanId = $derived(plan.ids.length === 1 ? plan.ids[0] : stripAlias(modelId.trim()))

  async function handleTest() {
    if (!cleanId || testStatus === 'testing') return
    testStatus = 'testing'
    testError = ''
    try {
      const data = await api.testModel(`${providerAlias}/${cleanId}`)
      testStatus = data.ok ? 'ok' : 'error'
      testError = data.error || ''
    } catch (err) {
      testStatus = 'error'
      testError = err instanceof Error ? err.message : String(err)
    }
  }

  // One button, two destinations: a single clean id keeps the single-save path (it can
  // be tested against the provider first), anything longer goes to the batch endpoint so
  // that one bad line cannot take the others down with it.
  async function handleSubmit() {
    if (limitsError) return
    if (isPaste) {
      if (bulkSaving || plan.ids.length === 0) return
      bulkSaving = true
      bulkError = ''
      try {
        // The response names every id it stored and every id it refused, so the counts
        // below are what landed instead of what was asked for. One entry per id, each
        // carrying the same capabilities: the form applies them to the whole paste
        // (D10), and the endpoint stays general for a caller that does not.
        bulkResult = await api.saveCustomModelsBulk({
          providerAlias,
          models: plan.ids.map((id): CustomModelBatchEntry => ({ id, ...caps, ...limits })),
        })
        onBulkSaved?.()
      } catch (err) {
        bulkResult = null
        bulkError = err instanceof Error ? err.message : String(err)
      } finally {
        bulkSaving = false
      }
      return
    }
    if (!cleanId || saving) return
    saving = true
    try {
      await onSave(cleanId, { ...caps }, { ...limits })
    } finally {
      saving = false
    }
  }

  function handleKeyDown(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleTest()
    }
  }
</script>

<Modal {isOpen} {onClose} title="Add Custom Model">
  <div class="flex flex-col gap-4">
    <div>
      <label class="text-sm font-medium mb-1.5 block" for="add-custom-model-id">Model ID</label>
      <div class="flex gap-2">
        <input
          id="add-custom-model-id"
          type="text"
          value={modelId}
          oninput={(e) => {
            modelId = (e.target as HTMLInputElement).value
            bulkResult = null
            testStatus = null
            testError = ''
          }}
          onkeydown={handleKeyDown}
          placeholder="e.g. claude-opus-4-5 — paste a list to add several"
          class="flex-1 px-3 py-2 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary"
        />
        {#if !isPaste}
          <Button
            variant="secondary"
            loading={testStatus === 'testing'}
            onclick={handleTest}
            disabled={!modelId.trim() || testStatus === 'testing'}
          >
            {#if testStatus !== 'testing'}
              <span class="material-symbols-outlined text-[18px]">science</span>
            {/if}
            {testStatus === 'testing' ? 'Testing...' : 'Test'}
          </Button>
        {/if}
      </div>
      {#if isPaste}
        <p class="text-xs text-text-muted mt-1">
          {plan.ids.length} id{plan.ids.length === 1 ? '' : 's'} to add
          {#if plan.duplicates.length > 0}· {plan.duplicates.length} repeated in the paste{/if}
          {#if plan.invalid.length > 0}· {plan.invalid.length} rejected{/if}
          — separated by spaces, commas or newlines, a
          <code class="font-mono bg-sidebar px-1 rounded">{providerAlias}/</code> prefix is stripped, and the
          capabilities and token limits below apply to every id.
        </p>
        {#if plan.ids.length > 0}
          <div class="mt-2 flex flex-wrap gap-1">
            {#each plan.ids as id (id)}
              <code class="font-mono text-xs bg-sidebar px-1.5 py-0.5 rounded">{id}</code>
            {/each}
          </div>
        {/if}
        {#if plan.invalid.length > 0}
          <ul class="text-xs text-red-500 mt-1 space-y-0.5">
            {#each plan.invalid as id (id)}
              <li><code class="font-mono">{id}</code> — not a usable id (pipe, quotes or angle brackets)</li>
            {/each}
          </ul>
        {/if}
      {:else}
        <p class="text-xs text-text-muted mt-1">
          Sent to provider as:
          <code class="font-mono bg-sidebar px-1 rounded">{cleanId || 'model-id'}</code>
        </p>
      {/if}
    </div>

    {#if bulkResult}
      <div class="text-sm font-medium {bulkResult.skipped.length > 0 ? 'text-yellow-400' : 'text-green-400'}">
        ✓ {summarizeBulkModels(plan, bulkResult.added.length)}
      </div>
      {#if bulkResult.skipped.length > 0}
        <ul class="text-xs text-text-muted space-y-0.5">
          {#each bulkResult.skipped as skip (skip.id)}
            <li>
              <code class="font-mono">{skip.id}</code>
              — {skip.reason}
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
    {#if bulkError}
      <p class="text-xs text-red-600 dark:text-red-400" role="alert">{bulkError}</p>
    {/if}

    <div>
      <label class="text-sm font-medium mb-1.5 block">Capabilities</label>
      <div class="flex flex-wrap gap-4">
        {#each Object.entries(CAPACITY_META) as [key, meta]}
          <div class="flex items-center gap-2">
            <Toggle
              checked={caps[key as keyof ModelCaps]}
              onchange={(v: boolean) => {
                caps = { ...caps, [key]: v }
              }}
              label={meta.label}
              size="sm"
            />
            <div class="leading-tight">
              <div class="text-sm">{meta.label}</div>
              <div class="text-xs text-text-muted">{meta.desc}</div>
            </div>
          </div>
        {/each}
      </div>
    </div>

    <div>
      <label class="text-sm font-medium mb-1.5 block">Token limits (optional)</label>
      <div class="grid gap-3 sm:grid-cols-2">
        <div>
          <label class="text-xs text-text-muted mb-1 block" for="add-custom-model-ctx">Context window</label>
          <input
            id="add-custom-model-ctx"
            type="text"
            inputmode="numeric"
            placeholder="e.g. 131072"
            value={limits.contextWindow || ''}
            oninput={(e) => {
              limits = { ...limits, contextWindow: parseLimit((e.target as HTMLInputElement).value) }
            }}
            class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary"
          />
        </div>
        <div>
          <label class="text-xs text-text-muted mb-1 block" for="add-custom-model-out">Max output</label>
          <input
            id="add-custom-model-out"
            type="text"
            inputmode="numeric"
            placeholder="e.g. 8192"
            value={limits.maxOutput || ''}
            oninput={(e) => {
              limits = { ...limits, maxOutput: parseLimit((e.target as HTMLInputElement).value) }
            }}
            aria-describedby={limitsError ? 'add-custom-model-limits-error' : undefined}
            aria-invalid={limitsError ? 'true' : undefined}
            class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary"
          />
        </div>
      </div>
      <p class="text-xs text-text-muted mt-1">
        Published to <code class="font-mono bg-sidebar px-1 rounded">/v1/models</code> instead of a guessed
        value. Leave blank to keep the default.
      </p>
      {#if limitsError}
        <p id="add-custom-model-limits-error" class="text-xs text-red-600 dark:text-red-400 mt-1" role="alert">
          {limitsError}
        </p>
      {/if}
    </div>

    {#if testStatus === 'ok'}
      <div class="flex items-center gap-2 text-sm text-green-600">
        <span class="material-symbols-outlined text-base">check_circle</span>
        Model is reachable
      </div>
    {/if}
    {#if testStatus === 'error'}
      <div class="flex items-start gap-2 text-sm text-red-500">
        <span class="material-symbols-outlined text-base shrink-0">cancel</span>
        <span>{testError || 'Model not reachable'}</span>
      </div>
    {/if}

    <div class="flex gap-2 pt-1">
      <Button onclick={onClose} variant="ghost" fullWidth size="sm">Cancel</Button>
      <Button
        onclick={handleSubmit}
        fullWidth
        size="sm"
        disabled={plan.ids.length === 0 || saving || bulkSaving || !!limitsError}
      >
        {#if saving || bulkSaving}
          Adding...
        {:else if isPaste}
          Add {plan.ids.length} Model{plan.ids.length === 1 ? '' : 's'}
        {:else}
          Add Model
        {/if}
      </Button>
    </div>
  </div>
</Modal>
