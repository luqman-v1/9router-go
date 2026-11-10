<script lang="ts">
  import { api, type APIKey, type ApiKeyBudget, type APIKeyPolicy, type Combo, type Plan, type ProviderConnection, type ProviderNode } from '../../api/client'
  import { Loader2, Save, X } from 'lucide-svelte'
  import { isNavEntryHidden } from '../../lib/navVisibility.svelte'
  import { formatTokenAmount, parseTokenAmount } from '../../lib/tokenUnits'
  import ModelPickerModal from '../combos/ModelPickerModal.svelte'

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
  // The lifetime budget and the concurrency wait are the two governance columns
  // the modal gained alongside the per-window rates. Both read 0 as "unset":
  // no quota, and the server's default queue timeout.
  //
  // The two token amounts are text, not numbers: a budget of ten million is
  // written "10M", and a seven-digit number field is unreadable. The parser
  // is decimal, so 1M is a million tokens.
  let tokenQuota = $state(formatTokenAmount(apiKey.tokenQuota ?? 0))
  let contextLimit = $state(formatTokenAmount(apiKey.contextLimit ?? 0))
  let queueTimeoutMs = $state(apiKey.queueTimeoutMs ?? 0)
  // The per-period budget (F-2). Cost is a text dollar amount read into
  // integer micros on save; tokens reuse the lifetime shorthand parser. A
  // blank cost means "no cost limit", so 0 is the explicit clear and never a
  // typo that silently removes the ceiling. hardCutoff starts armed (true)
  // only when the key already spends against a budget, else it keeps whatever
  // is stored.
  let budgetLimitUsd = $state(
    apiKey.limitMicros ? String(Number((apiKey.limitMicros / 1e6).toFixed(6))) : '',
  )
  let budgetLimitTokens = $state(formatTokenAmount(apiKey.limitTokens ?? 0))
  let budgetPeriod = $state<string>(apiKey.budgetPeriod || 'monthly')
  let alertPct = $state(apiKey.alertPct ?? 0)
  // hardCutoff initialises from the stored value, full stop: the list returns
  // the key's own hardCutoff, so forcing true here would silently flip a
  // warn-only key to refuse-at-ceiling the moment the operator pressed Save on
  // this dialog for any other reason. The `?? true` default only covers rows
  // minted before the column existed, which the server reads the same way.
  let hardCutoff = $state(apiKey.hardCutoff ?? true)
  // The spend read is paid when the key could be budgeted: a column on the
  // key, or a plan assigned to it (a plan can arm the budget while every
  // column reads zero). The endpoint then resolves plan-then-key like the
  // limiter and its `armed` field is the truth for what the panel shows.
  let couldBeArmed = $derived(
    (apiKey.limitMicros ?? 0) > 0 || (apiKey.limitTokens ?? 0) > 0 || !!apiKey.planId,
  )
  let budget = $state<ApiKeyBudget | null>(null)
  let budgetLoadError = $state(false)

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
  let isModelPickerOpen = $state(false)
  // The picker lists what the gateway publishes, which is a question about the
  // whole deployment rather than about this key. The three sources are read
  // when the picker opens, not when the dialog opens: an operator who never
  // touches the allowlist should not pay for the reads.
  let pickerConnections = $state<ProviderConnection[]>([])
  let pickerProviderNodes = $state<ProviderNode[]>([])
  let pickerCombos = $state<Combo[]>([])
  // The pattern field is the fallback for the glob and deny syntax the picker
  // cannot express. It stays out of the way until it is asked for.
  let showPatternInput = $state(false)
  let isLoadingModels = $state(false)
  let isSaving = $state(false)
  let error = $state<string | null>(null)
  // Whether the stored allowlist has been fetched. A key's allowlist is part of
  // the policy this modal edits, so it has to be present on open rather than
  // behind a button press.
  let hasLoadedModels = $state(false)
  // F-3 plan assignment. The plan is part of this same policy, so it is loaded
  // on open next to the allowlist rather than behind a second dialog.
  let planId = $state(apiKey.planId ?? '')
  let plans = $state<Plan[]>([])
  let isLoadingPlans = $state(false)

  // The Plan field belongs to the Advanced usage feature: with the sidebar
  // entry hidden, the plans page is unreachable, so the field is not offered.
  let plansHidden = $derived(isNavEntryHidden('plans'))

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

  // A token amount is read from the text field into a whole count. An amount
  // the parser cannot read is refused here rather than stored as 0: 0 means
  // "unlimited", so a typo would silently remove the ceiling the operator
  // was trying to set.
  function tokenAmount(value: string, label: string): number | null {
    const trimmed = value.trim()
    if (!trimmed) return 0
    const parsed = parseTokenAmount(trimmed)
    if (parsed === null) {
      error = `${label} is not a token amount. Use a number, or a scale like 500K, 2M or 1B.`
      return null
    }
    return parsed
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

  // openModelPicker reads what the gateway publishes and opens the picker.
  //
  // The nodes and their connections are what put a custom endpoint's own
  // models in the list: without them the picker offers only the catalog's
  // providers, and a model served by a custom node — the one a reseller is
  // most likely to be selling — cannot be picked at all.
  //
  // Each source catches on its own: one failing request must not leave the
  // picker empty, which would read as "this gateway publishes nothing".
  async function openModelPicker() {
    isModelPickerOpen = true
    const [nodes, conns, comboList] = await Promise.all([
      api.getProviderNodes().catch(() => [] as ProviderNode[]),
      api.getConnections().catch(() => [] as ProviderConnection[]),
      api.getCombos().catch(() => [] as Combo[]),
    ])
    pickerProviderNodes = nodes
    pickerConnections = conns
    pickerCombos = comboList
  }

  // A model picked in the picker joins the allowlist. It is not written
  // straight away: the allowlist is one part of the policy this dialog saves,
  // and a half-finished dialog must not leave half of it behind.
  function selectModel(value: string) {
    if (!models.includes(value)) {
      models = [...models, value]
    }
  }

  function deselectModel(value: string) {
    models = models.filter((m) => m !== value)
  }

  // An existing allowlist is part of the policy this modal edits, exactly like
  // the rate limits, so it has to be fetched on open. The list used to start
  // empty and only filled after "Load" was pressed, so the modal claimed "every
  // model is allowed" for a key that was in fact restricted — and the empty
  // draft that resulted then overwrote the stored list on the next save.
  // Plans are the templates this key can inherit from. They are read once on
  // open: a failed read leaves the select empty but does NOT block the save,
  // because the plan is only written when the operator picks one — an omitted
  // planId leaves the stored assignment alone.
  $effect(() => {
    if (!hasLoadedModels) loadModels()
    if (!plansHidden) void loadPlans()
    // Open-window spend is read-only context, so a failed read must not block
    // the save or clobber the shared error banner the save path owns. Both
    // guards keep it to a single fetch: `budget === null` skips a refetch once
    // it loaded, and `!budgetLoadError` skips a retry when it failed (leaving
    // `budget` null would otherwise loop forever). The gate is couldBeArmed,
    // not the endpoint's armed flag: a plan can arm the budget while the key's
    // columns read zero, and only the read itself knows for sure — it reports
    // `armed` and the panel renders on that.
    if (couldBeArmed && budget === null && !budgetLoadError) void loadKeyBudget()
  })

  async function loadPlans() {
    isLoadingPlans = true
    try {
      const res = await api.getPlans()
      plans = res.plans ?? []
    } catch {
      plans = []
    } finally {
      isLoadingPlans = false
    }
  }

  async function loadKeyBudget() {
    try {
      budget = await api.getKeyBudget(apiKey.id)
      budgetLoadError = false
    } catch {
      budgetLoadError = true
    }
  }

  // Micros to dollars, two decimals, the same shape PlansView uses.
  function fmtMicros(micros: number): string {
    return `$${(micros / 1_000_000).toFixed(2)}`
  }

  // formatTokenAmount writes 0 as "", which would render "Spent  of ..."; the
  // fallback keeps a zero axis legible.
  function fmtTokens(tokens: number): string {
    return formatTokenAmount(tokens) || '0'
  }

  // Status chip colours reuse the Badge palette: success / warning / danger.
  const statusLabel: Record<ApiKeyBudget['status'], string> = {
    ok: 'OK',
    approaching: 'Approaching',
    exhausted: 'Exhausted',
  }
  const statusClasses: Record<ApiKeyBudget['status'], string> = {
    ok: 'bg-success/10 text-success border border-success/25',
    approaching: 'bg-warning/10 text-warning border border-warning/25',
    exhausted: 'bg-danger/10 text-danger border border-danger/25',
  }

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
      const quota = tokenAmount(tokenQuota, 'Lifetime token quota')
      if (quota === null) {
        isSaving = false
        return
      }
      const context = tokenAmount(contextLimit, 'Context limit')
      if (context === null) {
        isSaving = false
        return
      }
      // Per-period cost budget. A blank is an explicit "no cost limit" (0),
      // matching how the other unset columns read as unlimited; a value the
      // parser cannot read is refused rather than stored as 0.
      const rawUsd = budgetLimitUsd.trim()
      let budgetMicros = 0
      if (rawUsd) {
        const parsed = Number.parseFloat(rawUsd)
        if (Number.isNaN(parsed) || parsed < 0 || !Number.isFinite(parsed)) {
          error = 'Period budget is not a dollar amount. Use a number like 25 or 25.50.'
          isSaving = false
          return
        }
        budgetMicros = Math.round(parsed * 1e6)
      }
      const periodTokens = tokenAmount(budgetLimitTokens, 'Period token budget')
      if (periodTokens === null) {
        isSaving = false
        return
      }
      if (!Number.isFinite(alertPct) || alertPct < 0 || alertPct > 100) {
        error = 'Alert percentage must be between 0 and 100.'
        isSaving = false
        return
      }
      const policy: APIKeyPolicy = {
        rateLimitRpm: nonNegative(rpm),
        rateLimitTpm: nonNegative(tpm),
        rateLimitConcurrency: nonNegative(concurrency),
        tokenQuota: quota,
        contextLimit: context,
        queueTimeoutMs: nonNegative(queueTimeoutMs),
        expiresAt: toRFC3339(expiresLocal),
        metadata: metadataValue,
        name: name.trim(),
        // The plan is written only when one is selected: an empty string is a
        // deliberate detach, and the server refuses an id that does not exist
        // rather than storing a dangling one.
        planId,
        // Per-period budget. Always sending the cost as an explicit number
        // matters: the server treats an omitted limitMicros as "keep stored
        // value", so 0 is the only way to clear a cost limit.
        limitMicros: budgetMicros,
        limitTokens: periodTokens,
        budgetPeriod,
        alertPct,
        hardCutoff,
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

      <!-- F-3 plan template. Part of the Advanced usage feature: with the
           sidebar entry hidden the plans page is unreachable, so the field goes
           with it instead of pointing at a page that redirects. -->
      {#if !plansHidden}
        <section class="space-y-3">
          <div>
            <h3 class="font-headline text-xs font-bold text-text-main">Plan</h3>
            <p class="text-text-subtle mt-0.5">
              The key inherits this plan's limits as defaults. Any limit set below overrides the plan.
              Choose <span class="font-code">No plan</span> to detach the key.
            </p>
          </div>
          <select
            bind:value={planId}
            aria-label="Plan"
            class="w-full bg-surface border border-border rounded-lg px-3 py-2 text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="">No plan — this key's own columns only</option>
            {#each plans as plan (plan.id)}
              <option value={plan.id}>{plan.name}</option>
            {/each}
          </select>
          {#if isLoadingPlans}
            <p class="text-text-subtle text-[11px]">Loading plans…</p>
          {:else if plans.length === 0}
            <p class="text-text-subtle text-[11px]">
              No plans defined yet. Create one on the Plans page.
            </p>
          {/if}
        </section>
      {/if}

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

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="pol-quota" class="block font-semibold text-text-muted mb-1">
              Lifetime token quota
            </label>
            <input
              id="pol-quota"
              type="text"
              inputmode="text"
              placeholder="1M"
              bind:value={tokenQuota}
              class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
            />
            <p class="text-text-subtle mt-1">
              Total prompt + completion tokens for the key's lifetime. Accepts a number or a scale
              (500K, 2M, 1B). Once spent, requests are refused with 429.
            </p>
          </div>
          <div>
            <label for="pol-queue" class="block font-semibold text-text-muted mb-1">
              Queue timeout (ms)
            </label>
            <input
              id="pol-queue"
              type="number"
              min="0"
              step="1"
              bind:value={queueTimeoutMs}
              class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
            />
            <p class="text-text-subtle mt-1">
              How long a request waits for a free concurrency slot before it is refused with 429.
              Leave 0 for the server default.
            </p>
          </div>
        </div>

        <!-- Per-period budget (F-2). Cost and token ceilings reset on the
             period below; open-window spend is read-only context loaded once
             on open. -->
        <div class="space-y-3">
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label for="pol-bud-usd" class="block font-semibold text-text-muted mb-1">
                Cost budget ($)
              </label>
              <input
                id="pol-bud-usd"
                type="text"
                inputmode="decimal"
                placeholder="25.00"
                bind:value={budgetLimitUsd}
                class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
              />
              <p class="text-text-subtle mt-1">
                USD the key may spend each {budgetPeriod}. Blank = no cost limit.
              </p>
            </div>
            <div>
              <label for="pol-bud-tok" class="block font-semibold text-text-muted mb-1">
                Token budget
              </label>
              <input
                id="pol-bud-tok"
                type="text"
                placeholder="1M"
                bind:value={budgetLimitTokens}
                class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
              />
              <p class="text-text-subtle mt-1">
                Prompt + completion tokens per period. Blank = no token limit.
              </p>
            </div>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label for="pol-bud-period" class="block font-semibold text-text-muted mb-1">
                Period
              </label>
              <select
                id="pol-bud-period"
                bind:value={budgetPeriod}
                class="w-full bg-surface border border-border rounded-lg px-3 py-2 text-xs text-text-main focus:outline-none focus:border-brand-500"
              >
                <option value="daily">Daily</option>
                <option value="weekly">Weekly</option>
                <option value="monthly">Monthly</option>
                <option value="total">Total — never resets</option>
              </select>
            </div>
            <div>
              <label for="pol-bud-alert" class="block font-semibold text-text-muted mb-1">
                Alert at (%)
              </label>
              <input
                id="pol-bud-alert"
                type="number"
                min="0"
                max="100"
                bind:value={alertPct}
                class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
              />
              <p class="text-text-subtle mt-1">
                Warn at this share of the budget. 0 = no warning.
              </p>
            </div>
          </div>

          <label class="flex items-start gap-2 cursor-pointer">
            <input type="checkbox" bind:checked={hardCutoff} class="mt-0.5" />
            <span>
              <span class="block font-semibold text-text-muted">Refuse when exhausted (402)</span>
              <span class="text-text-subtle block">
                Off = keep serving past the limit with a warning header only.
              </span>
            </span>
          </label>

          {#if couldBeArmed}
            {#if budgetLoadError}
              <p class="text-text-subtle text-[11px]">Could not load period spend.</p>
            {:else if budget?.armed}
              <div class="mt-2 space-y-1 rounded-lg border border-border bg-surface-2 px-3 py-2 font-code text-[11px] text-text-main">
                <p>
                  {#if budget!.limitMicros === 0}
                    Spent {fmtMicros(budget!.spentMicros)} (no cost limit)
                  {:else}
                    Spent {fmtMicros(budget!.spentMicros)} of {fmtMicros(budget!.limitMicros)}
                  {/if}
                </p>
                <p>
                  {#if budget!.limitTokens === 0}
                    Spent {fmtTokens(budget!.spentTokens)} (no token limit)
                  {:else}
                    Spent {fmtTokens(budget!.spentTokens)} of {fmtTokens(budget!.limitTokens)}
                  {/if}
                </p>
                <p class="flex items-center gap-1.5">
                  <span
                    class="inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-medium {statusClasses[budget!.status]}"
                  >
                    {statusLabel[budget!.status]}
                  </span>
                </p>
                {#if budget!.note}
                  <p class="text-text-subtle">{budget!.note}</p>
                {/if}
              </div>
            {/if}
          {/if}
        </div>

        <div>
          <label for="pol-context" class="block font-semibold text-text-muted mb-1">
            Context limit per request
          </label>
          <input
            id="pol-context"
            type="text"
            inputmode="text"
            placeholder="200K"
            bind:value={contextLimit}
            class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
          />
          <p class="text-text-subtle mt-1">
            The largest context one request may use. Accepts a number or a scale (200K, 1M). A
            conversation longer than this is trimmed to fit, and the answer's budget is capped to
            what is left. Leave empty for the model's own window.
          </p>
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
            JSON object kept with the key. Accepted keys:
            <span class="font-code">customerId</span>,
            <span class="font-code">priceCents</span> (whole cents),
            <span class="font-code">billingCycle</span> (one-off, monthly, quarterly, yearly),
            <span class="font-code">contact</span>,
            <span class="font-code">notes</span>
            — up to 2 KB. Any other key is refused, so a typo cannot silently drop
            the key out of the resale report.
          </p>
        </div>
        <textarea
          bind:value={metadata}
          rows="3"
          spellcheck="false"
          placeholder={'{\n  "customerId": "acme",\n  "priceCents": 5000,\n  "billingCycle": "monthly"\n}'}
          class="w-full bg-surface border border-border rounded-lg px-3 py-2 font-code text-[11px] text-text-main focus:outline-none focus:border-brand-500 resize-y"
        ></textarea>
      </section>

      <!-- Model allowlist -->
      <section class="space-y-3">
        <div class="flex items-start justify-between gap-3">
          <div>
            <h3 class="font-headline text-xs font-bold text-text-main">Model access</h3>
            <p class="text-text-subtle mt-0.5">
              The models this key may call. An empty list allows every model.
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

        <!-- The picker is the way in: what a reseller sells is a set of models
             they can see, and typing a pattern to name one of them is
             guesswork. The pattern field below stays for the glob and deny
             syntax the store understands. -->
        <button
          type="button"
          onclick={openModelPicker}
          class="w-full px-3 py-2 rounded-lg bg-surface-2 border border-border text-[11px] text-text-main hover:border-brand-500/40 transition cursor-pointer"
        >
          Choose from the models this gateway publishes
        </button>

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

        <button
          type="button"
          onclick={() => (showPatternInput = !showPatternInput)}
          class="self-start text-[11px] text-text-subtle hover:text-text-main transition cursor-pointer"
        >
          {showPatternInput ? 'Hide pattern input' : 'Add a pattern instead'}
        </button>

        {#if showPatternInput}
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
          <p class="text-text-subtle text-[11px]">
            Patterns may use <span class="font-code">*</span> per segment, e.g.
            <span class="font-code">e2e/allowed-*</span>, and
            <span class="font-code">!</span> or <span class="font-code">-</span> denies a model.
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

<ModelPickerModal
  isOpen={isModelPickerOpen}
  target="key"
  connections={pickerConnections}
  providerNodes={pickerProviderNodes}
  combos={pickerCombos}
  addedModelValues={models}
  onSelect={selectModel}
  onDeselect={deselectModel}
  onClose={() => (isModelPickerOpen = false)}
/>