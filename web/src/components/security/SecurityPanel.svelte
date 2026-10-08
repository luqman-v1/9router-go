<script lang="ts">
  import { api, type GuardrailLog, type GuardrailPolicy, type VaultStatus } from '../../api/client'
  import {
    Activity,
    AlertTriangle,
    KeyRound,
    Loader2,
    Lock,
    Plus,
    RefreshCw,
    ShieldCheck,
    Trash2
  } from 'lucide-svelte'

  let vault = $state<VaultStatus | null>(null)
  let policies = $state<GuardrailPolicy[]>([])
  let logs = $state<GuardrailLog[]>([])
  let isLoading = $state(false)
  let error = $state<string | null>(null)
  // Mirrors settings.guardrailsEnabled. Default true: a policy row is an
  // explicit decision, so an unwritten setting must not read as "off".
  let guardrailsEnabled = $state(true)
  let isToggling = $state(false)

  let scope = $state('global')
  let policyName = $state('')
  let detectors = $state<string[]>(['pii'])
  let action = $state('log_only')
  let isCreating = $state(false)

  let rotateKey = $state('')
  let isRotating = $state(false)

  const SCOPES = [
    { id: 'global', label: 'Global' },
    { id: 'provider', label: 'Provider' },
    { id: 'model', label: 'Model' },
    { id: 'chain', label: 'Chain' },
    { id: 'apikey', label: 'API Key' }
  ]

  const ACTIONS = [
    { id: 'allow', label: 'Allow', hint: 'Detect but never act' },
    { id: 'log_only', label: 'Log only', hint: 'Record the decision (default)' },
    { id: 'warn', label: 'Warn', hint: 'Record and warn' },
    { id: 'mask', label: 'Mask', hint: 'Redact matches before dispatch' },
    { id: 'block', label: 'Block', hint: 'Reject the request with 400' }
  ]

  let actionHint = $derived(ACTIONS.find((a) => a.id === action)?.hint ?? '')

  async function load() {
    isLoading = true
    error = null
    try {
      const [vaultRes, policyRes, logRes, settingsRes] = await Promise.all([
        api.getVaultStatus(),
        api.getGuardrailPolicies(scope),
        api.getGuardrailLogs(50),
        api.getSettings()
      ])
      vault = vaultRes
      policies = policyRes ?? []
      logs = logRes ?? []
      const raw = (settingsRes as Record<string, unknown> | undefined)?.guardrailsEnabled
      guardrailsEnabled = typeof raw === 'boolean' ? raw : true
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isLoading = false
    }
  }

  // The kill-switch is the escape hatch for a false positive that is blocking
  // real traffic. It has to stop enforcement without deleting the policy,
  // because deleting the policy also deletes the audit trail that explains why
  // it was turned on in the first place.
  async function toggleGuardrails() {
    isToggling = true
    error = null
    try {
      const next = !guardrailsEnabled
      await api.updateSettings({ guardrailsEnabled: next })
      guardrailsEnabled = next
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isToggling = false
    }
  }

  $effect(() => {
    void scope
    void load()
  })

  function toggleDetector(name: string) {
    detectors = detectors.includes(name)
      ? detectors.filter((d) => d !== name)
      : [...detectors, name]
  }

  async function createPolicy() {
    error = null
    if (!policyName.trim()) {
      error = 'A policy needs a name'
      return
    }
    if (detectors.length === 0) {
      error = 'Select at least one detector'
      return
    }
    try {
      isCreating = true
      await api.createGuardrailPolicy({
        name: policyName.trim(),
        scope,
        config: JSON.stringify({ detectors, action })
      })
      policyName = ''
      await load()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isCreating = false
    }
  }

  async function removePolicy(id: string) {
    if (!confirm('Delete this guardrail policy?')) return
    try {
      await api.deleteGuardrailPolicy(id)
      await load()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    }
  }

  async function rotateMasterKey() {
    error = null
    if (!rotateKey.trim()) return
    if (
      !confirm(
        'Rotate the master key? Credentials are re-wrapped under the new key. Make sure ROUTER_MASTER_KEY_PREVIOUS still holds the old key, otherwise sealed credentials become unreadable.'
      )
    )
      return
    try {
      isRotating = true
      const res = await api.rotateVault(rotateKey.trim())
      rotateKey = ''
      if (res.failures?.length) {
        error = `${res.rewrapped} re-wrapped, ${res.failures.length} failed: ${res.failures.join(', ')}`
      }
      await load()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isRotating = false
    }
  }

  function actionClass(a: string): string {
    if (a === 'block') return 'bg-danger/10 text-danger border-danger/25'
    if (a === 'mask') return 'bg-warning/10 text-warning border-warning/25'
    if (a === 'warn') return 'bg-info/10 text-info border-info/25'
    return 'bg-surface-2 text-text-subtle border-border'
  }
</script>

<div class="space-y-6">
  <div class="flex flex-col sm:flex-row sm:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[10px] uppercase tracking-wider text-brand-500 px-2 py-0.5 rounded bg-brand-500/10 border border-brand-500/25 font-bold">
          Security Posture
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-text-main tracking-tight">
        Credential Vault & Guardrails
      </h1>
      <p class="font-body text-xs sm:text-sm text-text-muted max-w-2xl leading-relaxed">
        Encrypt provider credentials at rest, and scan prompts for personal data and injection
        attempts before they leave the gateway.
      </p>
    </div>

    <button
      type="button"
      onclick={load}
      disabled={isLoading}
      class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg border border-border text-text-muted hover:text-text-main hover:border-brand-500/40 transition cursor-pointer disabled:opacity-50"
    >
      {#if isLoading}
        <Loader2 class="w-4 h-4 animate-spin" />
      {:else}
        <RefreshCw class="w-4 h-4" />
      {/if}
      <span>Refresh</span>
    </button>
  </div>

  {#if error}
    <p class="text-danger text-[11px] border border-danger/30 bg-danger/5 rounded-lg px-3 py-2">
      {error}
    </p>
  {/if}

  <!-- Credential vault -->
  <section class="bg-surface border border-border rounded-xl overflow-hidden shadow-xl">
    <div class="p-4 border-b border-border flex items-center justify-between">
      <h3 class="font-headline text-sm font-bold text-text-main flex items-center gap-2">
        <Lock class="w-4 h-4 text-brand-500" />
        <span>Credential Vault</span>
      </h3>
      {#if vault}
        <span
          class="px-2 py-0.5 rounded text-[10px] font-bold border {vault.enabled
            ? 'bg-success/10 text-success border-success/25'
            : 'bg-surface-2 text-text-subtle border-border'}"
        >
          {vault.enabled ? 'ENABLED' : 'DISABLED'}
        </span>
      {/if}
    </div>

    <div class="p-4 space-y-4">
      {#if vault && !vault.enabled}
        <div class="flex items-start gap-2.5 p-3 rounded-lg bg-warning/5 border border-warning/25">
          <AlertTriangle class="w-4 h-4 text-warning shrink-0 mt-0.5" />
          <p class="text-text-muted text-[11px] leading-relaxed">
            Credentials are stored in plaintext. Set <span class="font-code">ROUTER_MASTER_KEY</span>
            to a base64 32-byte value and restart the gateway to encrypt them with AES-256-GCM
            envelope encryption. This is opt-in — nothing changes until you set it.
          </p>
        </div>
      {/if}

      {#if vault}
        <div class="grid grid-cols-2 gap-3">
          <div class="p-3 rounded-lg bg-bg border border-border">
            <div class="font-code text-[10px] uppercase text-text-subtle tracking-wider">Sealed</div>
            <div class="font-headline text-xl font-bold text-success mt-1">{vault.sealedCount}</div>
          </div>
          <div class="p-3 rounded-lg bg-bg border border-border">
            <div class="font-code text-[10px] uppercase text-text-subtle tracking-wider">Plaintext</div>
            <div class="font-headline text-xl font-bold text-warning mt-1">{vault.plaintextCount}</div>
          </div>
        </div>
      {/if}

      {#if vault?.enabled}
        <div class="space-y-2 pt-2 border-t border-border">
          <label for="rotate-key" class="block font-semibold text-text-muted">
            Rotate master key
          </label>
          <p class="text-text-subtle text-[11px]">
            Re-wraps every data key under a new master key without re-encrypting any secret. Keep
            <span class="font-code">ROUTER_MASTER_KEY_PREVIOUS</span> pointing at the old key so this
            can be undone.
          </p>
          <div class="flex gap-2">
            <input
              id="rotate-key"
              type="password"
              bind:value={rotateKey}
              placeholder="base64 32-byte key"
              class="flex-1 bg-bg border border-border rounded-lg px-3 py-2 font-code text-[11px] text-text-main focus:outline-none focus:border-brand-500"
            />
            <button
              type="button"
              onclick={rotateMasterKey}
              disabled={isRotating || !rotateKey.trim()}
              class="px-3 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white text-[11px] font-bold transition cursor-pointer disabled:opacity-50 shrink-0"
            >
              {#if isRotating}<Loader2 class="w-3.5 h-3.5 animate-spin inline" />{:else}Rotate{/if}
            </button>
          </div>
        </div>
      {/if}
    </div>
  </section>

  <!-- Guardrails -->
  <section class="bg-surface border border-border rounded-xl overflow-hidden shadow-xl">
    <div class="p-4 border-b border-border flex items-center justify-between">
      <h3 class="font-headline text-sm font-bold text-text-main flex items-center gap-2">
        <ShieldCheck class="w-4 h-4 text-brand-500" />
        <span>Guardrails</span>
      </h3>
      <span class="font-code text-[11px] text-text-subtle">{policies.length} Policies</span>
    </div>

    <div class="p-4 space-y-4">
      <p class="text-text-muted text-[11px] leading-relaxed">
        Offline regex detection — no external service is contacted. Policies layer from least to
        most specific, so a model-scoped policy overrides a global one.
      </p>

      <!-- Kill-switch: the escape hatch when a policy is blocking real traffic. -->
      <div
        class="flex items-center justify-between gap-3 p-3 rounded-lg bg-bg border {guardrailsEnabled
          ? 'border-border'
          : 'border-warning/40'}"
      >
        <div class="min-w-0">
          <div class="font-headline text-xs font-bold text-text-main">Enforce guardrails</div>
          <div class="text-[11px] text-text-subtle mt-0.5">
            {guardrailsEnabled
              ? 'Policies are scanning requests and responses.'
              : 'Policies are kept but not enforced — the escape hatch for a false positive.'}
          </div>
        </div>
        <button
          type="button"
          onclick={toggleGuardrails}
          disabled={isToggling}
          role="switch"
          aria-checked={guardrailsEnabled}
          aria-label="Enforce guardrails"
          class="relative w-10 h-5 rounded-full transition shrink-0 cursor-pointer disabled:opacity-50 {guardrailsEnabled
            ? 'bg-brand-500'
            : 'bg-surface-3'}"
        >
          <span
            class="absolute top-0.5 w-4 h-4 rounded-full bg-white transition-all {guardrailsEnabled
              ? 'left-5'
              : 'left-0.5'}"
          ></span>
        </button>
      </div>

      <!-- Scope filter -->
      <div class="flex flex-wrap gap-1.5">
        {#each SCOPES as s (s.id)}
          <button
            type="button"
            onclick={() => (scope = s.id)}
            class="px-2.5 py-1 rounded-lg text-[11px] border transition cursor-pointer {scope === s.id
              ? 'bg-brand-500/10 border-brand-500/30 text-brand-500'
              : 'bg-surface-2 border-border text-text-subtle hover:text-text-main'}"
          >
            {s.label}
          </button>
        {/each}
      </div>

      <!-- Existing policies -->
      {#if policies.length > 0}
        <div class="space-y-2">
          {#each policies as p (p.id)}
            <div class="flex items-start justify-between gap-3 p-3 rounded-lg bg-bg border border-border">
              <div class="min-w-0">
                <div class="font-headline text-xs font-bold text-text-main">{p.name}</div>
                <div class="font-code text-[10px] text-text-subtle mt-1 truncate">{p.config}</div>
              </div>
              <div class="flex items-center gap-2 shrink-0">
                <span class="px-2 py-0.5 rounded text-[10px] font-bold border {actionClass(p.config.includes('block') ? 'block' : p.config.includes('mask') ? 'mask' : p.config.includes('warn') ? 'warn' : 'allow')}">
                  {p.config.match(/"action"\s*:\s*"([^"]+)"/)?.[1] ?? 'log_only'}
                </span>
                <button
                  type="button"
                  onclick={() => removePolicy(p.id)}
                  class="p-1.5 rounded-lg text-text-subtle hover:bg-danger/10 hover:text-danger transition cursor-pointer"
                  title="Delete policy"
                >
                  <Trash2 class="w-3.5 h-3.5" />
                </button>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <p class="text-text-subtle font-code text-[11px]">
          No policy at {scope} scope — traffic passes through unscanned.
        </p>
      {/if}

      <!-- Create policy -->
      <div class="pt-3 border-t border-border space-y-3">
        <div>
          <label for="policy-name" class="block font-semibold text-text-muted mb-1">Policy name</label>
          <input
            id="policy-name"
            type="text"
            bind:value={policyName}
            placeholder="e.g. Block PII in prompts"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 font-body text-xs text-text-main focus:outline-none focus:border-brand-500"
          />
        </div>

        <div>
          <span class="block font-semibold text-text-muted mb-1.5">Detectors</span>
          <div class="flex gap-2">
            {#each [{ id: 'pii', label: 'PII' }, { id: 'injection', label: 'Prompt injection' }] as d (d.id)}
              <button
                type="button"
                onclick={() => toggleDetector(d.id)}
                class="px-3 py-1.5 rounded-lg text-[11px] border transition cursor-pointer {detectors.includes(d.id)
                  ? 'bg-brand-500/10 border-brand-500/30 text-brand-500'
                  : 'bg-surface-2 border-border text-text-subtle hover:text-text-main'}"
              >
                {d.label}
              </button>
            {/each}
          </div>
        </div>

        <div>
          <span class="block font-semibold text-text-muted mb-1.5">Action</span>
          <div class="flex flex-wrap gap-1.5">
            {#each ACTIONS as a (a.id)}
              <button
                type="button"
                onclick={() => (action = a.id)}
                title={a.hint}
                class="px-2.5 py-1 rounded-lg text-[11px] border transition cursor-pointer {action === a.id
                  ? 'bg-brand-500/10 border-brand-500/30 text-brand-500'
                  : 'bg-surface-2 border-border text-text-subtle hover:text-text-main'}"
              >
                {a.label}
              </button>
            {/each}
          </div>
          <p class="text-text-subtle text-[10px] mt-1.5">{actionHint}</p>
        </div>

        <button
          type="button"
          onclick={createPolicy}
          disabled={isCreating}
          class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white text-[11px] font-bold transition cursor-pointer disabled:opacity-60"
        >
          {#if isCreating}
            <Loader2 class="w-3.5 h-3.5 animate-spin" />
          {:else}
            <Plus class="w-3.5 h-3.5" />
          {/if}
          <span>Add Policy</span>
        </button>
      </div>
    </div>
  </section>

  <!-- Audit log -->
  <section class="bg-surface border border-border rounded-xl overflow-hidden shadow-xl">
    <div class="p-4 border-b border-border flex items-center justify-between">
      <h3 class="font-headline text-sm font-bold text-text-main flex items-center gap-2">
        <Activity class="w-4 h-4 text-brand-500" />
        <span>Guardrail Audit Log</span>
      </h3>
      <span class="font-code text-[11px] text-text-subtle">Last {logs.length}</span>
    </div>

    {#if logs.length > 0}
      <div class="overflow-x-auto">
        <table class="w-full text-left font-body text-xs">
          <thead>
            <tr class="border-b border-border text-text-subtle font-code uppercase text-[10px] tracking-wider bg-surface-2">
              <th class="py-2.5 px-4">Detector</th>
              <th class="py-2.5 px-4">Action</th>
              <th class="py-2.5 px-4">Severity</th>
              <th class="py-2.5 px-4">When</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/50">
            {#each logs as entry (entry.id)}
              <tr class="hover:bg-surface-2/40 transition">
                <td class="py-2.5 px-4 font-code text-text-main">{entry.detector}</td>
                <td class="py-2.5 px-4">
                  <span class="px-2 py-0.5 rounded text-[10px] font-bold border {actionClass(entry.action)}">
                    {entry.action.toUpperCase()}
                  </span>
                </td>
                <td class="py-2.5 px-4 font-code text-[11px] text-text-subtle">
                  {entry.severity || '—'}
                </td>
                <td class="py-2.5 px-4 font-code text-[11px] text-text-subtle">
                  {new Date(entry.createdAt).toLocaleString()}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <p class="p-4 text-text-subtle font-code text-[11px]">
        <KeyRound class="w-3.5 h-3.5 inline align-text-bottom" />
        No guardrail decisions recorded yet.
      </p>
    {/if}
  </section>
</div>