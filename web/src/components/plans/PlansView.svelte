<script lang="ts">
  import { api, type Plan, type PlanInput, type PlanPeriod } from '../../api/client'
  import { Loader2, Pencil, Plus, RefreshCw, Trash2, X } from 'lucide-svelte'

  const PERIODS: { id: PlanPeriod; label: string; hint: string }[] = [
    { id: 'daily', label: 'Daily', hint: 'Resets every day (UTC)' },
    { id: 'weekly', label: 'Weekly', hint: 'Resets every Monday (UTC)' },
    { id: 'monthly', label: 'Monthly', hint: 'Resets on the 1st (UTC)' },
    { id: 'total', label: 'Total', hint: 'Never resets — a lifetime budget' },
  ]

  let plans = $state<Plan[]>([])
  let keyCounts = $state<Record<string, number>>({})
  let isLoading = $state(false)
  let error = $state<string | null>(null)
  let notice = $state<string | null>(null)

  // Editor state. `editingId` is null for a new plan, which is the only
  // difference between create and update in the form below.
  let isEditorOpen = $state(false)
  let editingId = $state<string | null>(null)
  let isSaving = $state(false)
  let formName = $state('')
  let formDescription = $state('')
  let formRpm = $state('0')
  let formTpm = $state('0')
  let formConcurrency = $state('0')
  let formPeriod = $state<PlanPeriod>('monthly')
  let formBudgetUsd = $state('0')
  let formTokens = $state('0')
  let formAlertPct = $state('80')
  let formHardCutoff = $state(false)
  let formModels = $state('')

  let editingPlan = $derived(plans.find((p) => p.id === editingId) ?? null)

  async function load() {
    isLoading = true
    error = null
    try {
      const res = await api.getPlans()
      plans = res.plans ?? []
      const counts = await Promise.all(
        plans.map(async (p) => {
          try {
            const r = await api.getPlanKeys(p.id)
            return [p.id, r.keys] as const
          } catch {
            return [p.id, 0] as const
          }
        }),
      )
      keyCounts = Object.fromEntries(counts)
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isLoading = false
    }
  }

  $effect(() => {
    load()
  })

  function openCreate() {
    editingId = null
    formName = ''
    formDescription = ''
    formRpm = '0'
    formTpm = '0'
    formConcurrency = '0'
    formPeriod = 'monthly'
    formBudgetUsd = '0'
    formTokens = '0'
    formAlertPct = '80'
    formHardCutoff = false
    formModels = ''
    error = null
    notice = null
    isEditorOpen = true
  }

  function openEdit(plan: Plan) {
    editingId = plan.id
    formName = plan.name
    formDescription = plan.description ?? ''
    formRpm = String(plan.rpmLimit ?? 0)
    formTpm = String(plan.tpmLimit ?? 0)
    formConcurrency = String(plan.concurrencyLimit ?? 0)
    formPeriod = plan.period ?? 'monthly'
    formBudgetUsd = String((plan.limitMicros ?? 0) / 1_000_000)
    formTokens = String(plan.limitTokens ?? 0)
    formAlertPct = String(plan.alertPct ?? 80)
    formHardCutoff = plan.hardCutoff ?? false
    formModels = (plan.allowedModels ?? []).join('\n')
    error = null
    notice = null
    isEditorOpen = true
  }

  function nonNegative(value: string): number {
    const n = Number(value)
    return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
  }

  // USD in the form, integer micros on the wire: a float dollar amount is
  // never what gets stored, so no rounding can let a key overdraw its budget.
  function microsFromUsd(value: string): number {
    const n = Number(value)
    if (!Number.isFinite(n) || n <= 0) return 0
    return Math.round(n * 1_000_000)
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    error = null
    notice = null
    const name = formName.trim()
    if (!name) {
      error = 'Name is required'
      return
    }
    const models = formModels
      .split('\n')
      .map((m) => m.trim())
      .filter(Boolean)
    const payload: PlanInput = {
      name,
      description: formDescription.trim(),
      rpmLimit: nonNegative(formRpm),
      tpmLimit: nonNegative(formTpm),
      concurrencyLimit: nonNegative(formConcurrency),
      period: formPeriod,
      limitMicros: microsFromUsd(formBudgetUsd),
      limitTokens: nonNegative(formTokens),
      alertPct: nonNegative(formAlertPct),
      hardCutoff: formHardCutoff,
      allowedModels: models,
    }
    try {
      isSaving = true
      if (editingId) {
        await api.updatePlan(editingId, payload)
        notice = 'Plan updated'
      } else {
        await api.createPlan(payload)
        notice = 'Plan created'
      }
      isEditorOpen = false
      await load()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isSaving = false
    }
  }

  async function remove(plan: Plan) {
    error = null
    notice = null
    try {
      await api.deletePlan(plan.id)
      notice = `Deleted ${plan.name}`
      await load()
    } catch (err) {
      // The API refuses a plan that keys still use with 409 and a count; that
      // message is the useful one, so it is shown verbatim instead of a
      // generic failure.
      error = err instanceof Error ? err.message : String(err)
    }
  }

  function usd(micros: number): string {
    if (!micros) return '—'
    return `$${(micros / 1_000_000).toFixed(2)}`
  }

  function describeLimits(plan: Plan): string {
    const parts: string[] = []
    if (plan.rpmLimit) parts.push(`${plan.rpmLimit} RPM`)
    if (plan.tpmLimit) parts.push(`${plan.tpmLimit} TPM`)
    if (plan.concurrencyLimit) parts.push(`${plan.concurrencyLimit} concurrent`)
    return parts.length ? parts.join(' · ') : 'no rate limits'
  }
</script>

<div class="max-w-5xl mx-auto space-y-6">
  <div class="flex items-start justify-between gap-4 flex-wrap">
    <div>
      <h1 class="text-2xl font-semibold text-text-main">Plans</h1>
      <p class="text-sm text-text-muted mt-1">
        Reusable policy templates. A key assigned to a plan inherits its limits as defaults and can
        still override them individually.
      </p>
    </div>
    <div class="flex items-center gap-2">
      <button
        onclick={load}
        class="flex items-center gap-2 px-3 py-2 rounded-[10px] border border-border-subtle text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors text-sm"
      >
        <RefreshCw size={16} class={isLoading ? 'animate-spin' : ''} />
        Reload
      </button>
      <button
        onclick={openCreate}
        class="flex items-center gap-2 px-3 py-2 rounded-[10px] bg-primary text-white hover:bg-primary-hover transition-colors text-sm font-medium"
      >
        <Plus size={16} />
        New plan
      </button>
    </div>
  </div>

  {#if error}
    <div
      class="rounded-[10px] border border-error/40 bg-error/10 px-4 py-3 text-sm text-error"
      role="alert"
    >
      {error}
    </div>
  {/if}
  {#if notice}
    <div class="rounded-[10px] border border-success/40 bg-success/10 px-4 py-3 text-sm">
      {notice}
    </div>
  {/if}

  {#if isLoading && plans.length === 0}
    <div class="flex items-center gap-2 text-text-muted text-sm py-8">
      <Loader2 size={16} class="animate-spin" />
      Loading plans…
    </div>
  {:else if plans.length === 0}
    <div class="rounded-[14px] border border-border-subtle bg-surface px-5 py-8 text-center">
      <p class="text-sm text-text-muted">No plans yet.</p>
    </div>
  {:else}
    <div class="rounded-[14px] border border-border-subtle bg-surface overflow-hidden">
      <table class="w-full text-sm">
        <thead class="bg-surface-2 text-text-muted">
          <tr>
            <th class="text-left font-medium px-4 py-2.5">Name</th>
            <th class="text-left font-medium px-4 py-2.5">Rate limits</th>
            <th class="text-left font-medium px-4 py-2.5">Budget</th>
            <th class="text-left font-medium px-4 py-2.5">Models</th>
            <th class="text-left font-medium px-4 py-2.5">Keys</th>
            <th class="px-4 py-2.5"></th>
          </tr>
        </thead>
        <tbody>
          {#each plans as plan (plan.id)}
            <tr class="border-t border-border-subtle">
              <td class="px-4 py-3">
                <div class="font-medium text-text-main">{plan.name}</div>
                {#if plan.description}
                  <div class="text-xs text-text-subtle mt-0.5">{plan.description}</div>
                {/if}
                {#if plan.hardCutoff}
                  <span
                    class="inline-block mt-1 px-1.5 py-0.5 rounded text-[10px] uppercase tracking-wide bg-warning/15 text-warning"
                  >
                    hard cutoff
                  </span>
                {/if}
              </td>
              <td class="px-4 py-3 text-text-muted">{describeLimits(plan)}</td>
              <td class="px-4 py-3 text-text-muted">
                {usd(plan.limitMicros)}{#if plan.limitTokens} · {plan.limitTokens} tok{/if}
                <div class="text-xs text-text-subtle">{plan.period}</div>
              </td>
              <td class="px-4 py-3 text-text-muted">
                {plan.allowedModels?.length
                  ? `${plan.allowedModels.length} pattern(s)`
                  : 'all models'}
              </td>
              <td class="px-4 py-3 text-text-muted">{keyCounts[plan.id] ?? 0}</td>
              <td class="px-4 py-3">
                <div class="flex items-center justify-end gap-1">
                  <button
                    onclick={() => openEdit(plan)}
                    title="Edit plan"
                    class="p-1.5 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-2"
                  >
                    <Pencil size={15} />
                  </button>
                  <button
                    onclick={() => remove(plan)}
                    title="Delete plan"
                    class="p-1.5 rounded-lg text-text-muted hover:text-error hover:bg-error/10"
                  >
                    <Trash2 size={15} />
                  </button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if isEditorOpen}
    <form
      onsubmit={(e) => {
        e.preventDefault()
        save(e)
      }}
      class="rounded-[14px] border border-border-subtle bg-surface p-5 space-y-4"
    >
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold text-text-main">
          {editingPlan ? `Edit ${editingPlan.name}` : 'New plan'}
        </h2>
        <button
          type="button"
          onclick={() => (isEditorOpen = false)}
          class="p-1.5 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-2"
          title="Close"
        >
          <X size={16} />
        </button>
      </div>

      <div class="grid gap-4 sm:grid-cols-2">
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Name</span>
          <input
            bind:value={formName}
            placeholder="Pro"
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          />
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Budget period</span>
          <select
            bind:value={formPeriod}
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          >
            {#each PERIODS as p (p.id)}
              <option value={p.id}>{p.label}</option>
            {/each}
          </select>
        </label>
      </div>

      <p class="text-xs text-text-subtle">
        {PERIODS.find((p) => p.id === formPeriod)?.hint ?? ''}
      </p>

      <label class="flex flex-col gap-1">
        <span class="text-xs text-text-muted">Description</span>
        <input
          bind:value={formDescription}
          placeholder="Paid tier — 120 RPM, $5 monthly budget"
          class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
        />
      </label>

      <div class="grid gap-4 sm:grid-cols-3">
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Requests / min (0 = unlimited)</span>
          <input
            bind:value={formRpm}
            inputmode="numeric"
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          />
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Tokens / min (0 = unlimited)</span>
          <input
            bind:value={formTpm}
            inputmode="numeric"
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          />
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Concurrent (0 = unlimited)</span>
          <input
            bind:value={formConcurrency}
            inputmode="numeric"
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          />
        </label>
      </div>

      <div class="grid gap-4 sm:grid-cols-3">
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Budget (USD, 0 = unlimited)</span>
          <input
            bind:value={formBudgetUsd}
            inputmode="decimal"
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          />
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Token budget (0 = unlimited)</span>
          <input
            bind:value={formTokens}
            inputmode="numeric"
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          />
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-xs text-text-muted">Alert at (% of budget)</span>
          <input
            bind:value={formAlertPct}
            inputmode="numeric"
            class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main"
          />
        </label>
      </div>

      <label class="flex items-center gap-2 text-sm text-text-main">
        <input type="checkbox" bind:checked={formHardCutoff} class="rounded" />
        Hard cutoff — refuse requests once the budget is exhausted
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-xs text-text-muted">
          Allowed models — one pattern per line, empty means every model. A trailing * is a
          wildcard. A key's own list is added to this one.
        </span>
        <textarea
          bind:value={formModels}
          rows="4"
          placeholder={'gpt-4o\nclaude-*'}
          class="px-3 py-2 rounded-[10px] border border-border bg-surface-2 text-sm text-text-main font-mono"
        ></textarea>
      </label>

      <div class="flex items-center justify-end gap-2">
        <button
          type="button"
          onclick={() => (isEditorOpen = false)}
          class="px-3 py-2 rounded-[10px] border border-border-subtle text-sm text-text-muted hover:text-text-main"
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={isSaving}
          class="flex items-center gap-2 px-3 py-2 rounded-[10px] bg-primary text-white hover:bg-primary-hover text-sm font-medium disabled:opacity-60"
        >
          {#if isSaving}<Loader2 size={15} class="animate-spin" />{/if}
          Save plan
        </button>
      </div>
    </form>
  {/if}
</div>
