<script lang="ts">
  import { api, type GuardrailLog, type GuardrailPolicy, type VaultStatus } from '../../api/client'
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import ConfirmModal from '../../lib/ui/ConfirmModal.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import { Activity, AlertTriangle, KeyRound, Lock, Plus, RefreshCw, ShieldCheck, Trash2 } from 'lucide-svelte'

  type BadgeTone = 'default' | 'neutral' | 'primary' | 'success' | 'warning' | 'danger' | 'error' | 'info' | 'outline'

  const SCOPES = [
    { id: 'global', label: 'Global', hint: 'Applies to every request' },
    { id: 'provider', label: 'Provider', hint: 'One provider, e.g. openai' },
    { id: 'model', label: 'Model', hint: 'One model id, e.g. gpt-4o-mini' },
    { id: 'chain', label: 'Chain', hint: 'One combo / chain' },
    { id: 'apikey', label: 'API key', hint: 'One client key' }
  ] as const

  const DETECTORS = [
    { id: 'pii', label: 'PII', hint: 'Email, IP, NIK, cards, IBAN' },
    { id: 'injection', label: 'Prompt injection', hint: 'Override and exfiltration attempts' },
    { id: 'custom', label: 'Custom pattern', hint: 'User-defined regular expressions' }
  ] as const

  const ACTIONS = [
    { id: 'allow', label: 'Allow', hint: 'Detect but never act' },
    { id: 'log_only', label: 'Log only', hint: 'Record the decision (default)' },
    { id: 'warn', label: 'Warn', hint: 'Record and warn' },
    { id: 'mask', label: 'Mask', hint: 'Redact matches before dispatch' },
    { id: 'block', label: 'Block', hint: 'Reject the request with 400' }
  ] as const

  let vault = $state<VaultStatus | null>(null)
  let policies = $state<GuardrailPolicy[]>([])
  let logs = $state<GuardrailLog[]>([])
  let isLoading = $state(false)
  let error = $state<string | null>(null)
  let notice = $state<string | null>(null)

  // Mirrors settings.guardrailsEnabled. Default true: a policy row is an
  // explicit decision, so an unwritten setting must not read as "off".
  let guardrailsEnabled = $state(true)
  let isToggling = $state(false)

  let scopeFilter = $state('all')
  let isEditorOpen = $state(false)
  let formName = $state('')
  let formScope = $state('global')
  let formScopeId = $state('')
  let formDetectors = $state<string[]>(['pii'])
  let formAction = $state('log_only')
  let isCreating = $state(false)

  let formCustomPatterns = $state<Array<{ name: string; regex: string; replacement?: string }>>([])
  let customName = $state('')
  let customRegex = $state('')
  let customReplacement = $state('')

  let rotateKey = $state('')
  let isRotating = $state(false)
  let pendingDelete = $state<GuardrailPolicy | null>(null)
  let isRotateConfirmOpen = $state(false)

  let visiblePolicies = $derived(
    scopeFilter === 'all' ? policies : policies.filter((p) => p.scope === scopeFilter)
  )
  let formActionHint = $derived(ACTIONS.find((a) => a.id === formAction)?.hint ?? '')
  let formScopeHint = $derived(SCOPES.find((s) => s.id === formScope)?.hint ?? '')

  async function load() {
    isLoading = true
    error = null
    try {
      const [vaultRes, logRes, settingsRes] = await Promise.all([
        api.getVaultStatus(),
        api.getGuardrailLogs(50),
        api.getSettings()
      ])
      // Every scope is listed, because a policy scoped to one provider or one
      // key lives under its own scope id and is invisible to a scope-wide read.
      const groups = await Promise.all(SCOPES.map((s) => api.getGuardrailPolicies(s.id)))
      vault = vaultRes
      logs = logRes ?? []
      policies = groups
        .flatMap((group) => group ?? [])
        .sort((a, b) => scopeRank(a.scope) - scopeRank(b.scope) || a.name.localeCompare(b.name))
      const raw = (settingsRes as Record<string, unknown> | undefined)?.guardrailsEnabled
      guardrailsEnabled = typeof raw === 'boolean' ? raw : true
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isLoading = false
    }
  }

  $effect(() => {
    load()
  })

  function scopeRank(scope: string): number {
    const idx = SCOPES.findIndex((s) => s.id === scope)
    return idx === -1 ? SCOPES.length : idx
  }

  // The kill-switch is the escape hatch for a false positive that is blocking
  // real traffic. It has to stop enforcement without deleting the policy,
  // because deleting the policy also deletes the audit trail that explains why
  // it was turned on in the first place.
  async function toggleGuardrails(next: boolean) {
    isToggling = true
    error = null
    try {
      await api.updateSettings({ guardrailsEnabled: next })
      guardrailsEnabled = next
      notice = next ? 'Guardrails are enforcing.' : 'Guardrails paused — policies kept.'
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isToggling = false
    }
  }

  function openEditor() {
    formName = ''
    formScope = scopeFilter === 'all' ? 'global' : scopeFilter
    formScopeId = ''
    formDetectors = ['pii']
    formAction = 'log_only'
    formCustomPatterns = []
    customName = ''
    customRegex = ''
    customReplacement = ''
    error = null
    notice = null
    isEditorOpen = true
  }

  function toggleFormDetector(id: string) {
    formDetectors = formDetectors.includes(id)
      ? formDetectors.filter((d) => d !== id)
      : [...formDetectors, id]
  }

  function addCustomPattern() {
    if (!customRegex.trim()) {
      error = 'Regex pattern is required'
      return
    }
    formCustomPatterns = [
      ...formCustomPatterns,
      {
        name: customName.trim() || 'custom',
        regex: customRegex.trim(),
        replacement: customReplacement.trim() || undefined
      }
    ]
    customName = ''
    customRegex = ''
    customReplacement = ''
    error = null
  }

  function removeCustomPattern(index: number) {
    formCustomPatterns = formCustomPatterns.filter((_, i) => i !== index)
  }

  async function createPolicy() {
    error = null
    notice = null
    if (!formName.trim()) {
      error = 'A policy needs a name'
      return
    }
    if (formScope !== 'global' && !formScopeId.trim()) {
      error = `A ${formScope}-scoped policy needs the ${formScope} it applies to`
      return
    }
    if (formDetectors.length === 0) {
      error = 'Select at least one detector'
      return
    }
    if (formDetectors.includes('custom') && formCustomPatterns.length === 0 && customRegex.trim() !== '') {
      addCustomPattern()
    }
    if (formDetectors.includes('custom') && formCustomPatterns.length === 0) {
      error = 'Custom pattern detector requires at least one pattern'
      return
    }
    try {
      isCreating = true
      const config: Record<string, unknown> = {
        detectors: formDetectors,
        action: formAction
      }
      if (formDetectors.includes('custom') && formCustomPatterns.length > 0) {
        config.customPatterns = formCustomPatterns
      }
      await api.createGuardrailPolicy({
        name: formName.trim(),
        scope: formScope,
        scopeId: formScope === 'global' ? '' : formScopeId.trim(),
        config: JSON.stringify(config)
      })
      isEditorOpen = false
      notice = 'Policy created'
      await load()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isCreating = false
    }
  }

  async function deletePolicy() {
    const policy = pendingDelete
    if (!policy) return
    error = null
    try {
      await api.deleteGuardrailPolicy(policy.id)
      pendingDelete = null
      notice = `Deleted ${policy.name}`
      await load()
    } catch (err) {
      pendingDelete = null
      error = err instanceof Error ? err.message : String(err)
    }
  }

  async function rotateMasterKey() {
    error = null
    notice = null
    if (!rotateKey.trim()) return
    try {
      isRotating = true
      const res = await api.rotateVault(rotateKey.trim())
      rotateKey = ''
      isRotateConfirmOpen = false
      if (res.failures?.length) {
        error = `${res.rewrapped} re-wrapped, ${res.failures.length} failed: ${res.failures.join(', ')}`
      } else {
        notice = `${res.rewrapped} data key(s) re-wrapped under the new master key`
      }
      await load()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      isRotating = false
    }
  }

  function parseConfig(raw: string): { detectors: string[]; action: string; customPatterns: Array<{ name: string; regex: string; replacement?: string }> } {
    try {
      const cfg = JSON.parse(raw) as { detectors?: unknown; action?: unknown; customPatterns?: unknown }
      return {
        detectors: Array.isArray(cfg.detectors) ? cfg.detectors.map(String) : [],
        action: typeof cfg.action === 'string' ? cfg.action : 'log_only',
        customPatterns: Array.isArray(cfg.customPatterns) ? cfg.customPatterns : []
      }
    } catch {
      return { detectors: [], action: 'log_only', customPatterns: [] }
    }
  }

  function actionTone(action: string): BadgeTone {
    if (action === 'block') return 'danger'
    if (action === 'mask') return 'warning'
    if (action === 'warn') return 'info'
    return 'neutral'
  }

  function detectorLabel(id: string): string {
    return DETECTORS.find((d) => d.id === id)?.label ?? id
  }

  function scopeLabel(scope: string): string {
    return SCOPES.find((s) => s.id === scope)?.label ?? scope
  }

  function formatWhen(iso: string): string {
    const date = new Date(iso)
    if (Number.isNaN(date.getTime())) return iso
    return date.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    })
  }
</script>

<div class="space-y-6">

  {#if error}
    <div class="rounded-[10px] border border-error/40 bg-error/10 px-4 py-3 text-sm text-error" role="alert">
      {error}
    </div>
  {/if}
  {#if notice}
    <div class="rounded-[10px] border border-success/40 bg-success/10 px-4 py-3 text-sm text-text-main">
      {notice}
    </div>
  {/if}

  <!-- Summary -->
  <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
      <div class="font-code text-[10px] uppercase tracking-wider text-text-subtle">Vault</div>
      <div class="mt-2">
        {#if vault}
          <Badge tone={vault.enabled ? 'success' : 'neutral'} dot>
            {vault.enabled ? 'AES-256-GCM' : 'Plaintext'}
          </Badge>
        {:else}
          <span class="text-sm text-text-subtle">—</span>
        {/if}
      </div>
      <p class="mt-2 text-xs text-text-muted">Credentials at rest</p>
    </div>

    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
      <div class="font-code text-[10px] uppercase tracking-wider text-text-subtle">Sealed</div>
      <div class="mt-1 text-2xl font-semibold text-success">{vault?.sealedCount ?? '—'}</div>
      <p class="mt-1 text-xs text-text-muted">Credential slots encrypted</p>
    </div>

    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
      <div class="font-code text-[10px] uppercase tracking-wider text-text-subtle">Plaintext</div>
      <div class="mt-1 text-2xl font-semibold {vault && vault.plaintextCount > 0 ? 'text-warning' : 'text-text-main'}">
        {vault?.plaintextCount ?? '—'}
      </div>
      <p class="mt-1 text-xs text-text-muted">Rows awaiting sealing</p>
    </div>

    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
      <div class="font-code text-[10px] uppercase tracking-wider text-text-subtle">Guardrails</div>
      <div class="mt-1 text-2xl font-semibold text-text-main">{policies.length}</div>
      <p class="mt-1 text-xs text-text-muted">
        {guardrailsEnabled ? 'Policies enforcing' : 'Policies paused'}
      </p>
    </div>
  </div>

  <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
    <!-- Credential vault -->
    <Card title="Credential Vault" subtitle="Envelope encryption for provider secrets" padding="md" elev>
      {#snippet icon()}
        <Lock size={16} />
      {/snippet}
      {#snippet action()}
        {#if vault}
          <Badge tone={vault.enabled ? 'success' : 'neutral'} dot>
            {vault.enabled ? 'Enabled' : 'Disabled'}
          </Badge>
        {/if}
      {/snippet}

      <div class="space-y-4">
        {#if vault && !vault.enabled}
          <div class="flex items-start gap-2.5 rounded-[10px] border border-warning/25 bg-warning/5 p-3">
            <AlertTriangle class="mt-0.5 h-4 w-4 shrink-0 text-warning" />
            <p class="text-xs leading-relaxed text-text-muted">
              Credentials are stored in plaintext. Set
              <span class="font-code text-text-main">ROUTER_MASTER_KEY</span>
              to a base64 32-byte value and restart the gateway to encrypt them. This is opt-in —
              nothing changes until you set it.
            </p>
          </div>
        {:else if vault}
          <div class="space-y-2 border-t border-border-subtle pt-4">
            <Input
              id="rotate-key"
              label="Rotate master key"
              type="password"
              placeholder="base64 32-byte key"
              bind:value={rotateKey}
              hint="Re-wraps every data key without re-encrypting a secret. Keep ROUTER_MASTER_KEY_PREVIOUS on the old key so this can be undone."
            />
            <Button
              variant="primary"
              size="sm"
              disabled={!rotateKey.trim()}
              onclick={() => (isRotateConfirmOpen = true)}
            >
              Rotate key
            </Button>
          </div>
        {:else}
          <p class="text-sm text-text-muted">Vault status unavailable.</p>
        {/if}
      </div>
    </Card>

    <!-- Guardrail enforcement -->
    <Card title="Guardrails" subtitle="Offline detection, no external service" padding="md" elev>
      {#snippet icon()}
        <ShieldCheck size={16} />
      {/snippet}
      {#snippet action()}
        <Toggle
          checked={guardrailsEnabled}
          disabled={isToggling}
          label="Enforce guardrails"
          onchange={toggleGuardrails}
        />
      {/snippet}

      <div class="space-y-3">
        <p class="text-sm text-text-muted">
          {guardrailsEnabled
            ? 'Policies are scanning prompts and responses on every request.'
            : 'Policies are kept but not enforced — the escape hatch for a false positive.'}
        </p>
        <p class="text-xs leading-relaxed text-text-subtle">
          Policies layer from least to most specific (global → provider → model → chain → API key).
          The most specific matching policy wins; a request with no policy passes through
          unscanned.
        </p>
      </div>
    </Card>
  </div>

  <!-- Policies -->
  <div class="space-y-3">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h2 class="text-base font-semibold text-text-main">Guardrail policies</h2>
        <p class="mt-0.5 text-sm text-text-muted">
          One policy per scope — the resolver takes the most specific match.
        </p>
      </div>
      <div class="flex items-center gap-2">
        <select
          bind:value={scopeFilter}
          aria-label="Filter policies by scope"
          class="rounded-[10px] border border-border bg-surface px-3 py-2 text-sm text-text-main focus:outline-none focus:ring-2 focus:ring-brand-500/40 cursor-pointer"
        >
          <option value="all">All scopes</option>
          {#each SCOPES as option (option.id)}
            <option value={option.id}>{option.label}</option>
          {/each}
        </select>
        <Button
          variant="outline"
          size="md"
          loading={isLoading}
          onclick={load}
          title="Reload vault status, policies and audit log"
        >
          <RefreshCw size={15} />
          Refresh
        </Button>
        <Button variant="primary" size="md" onclick={openEditor}>
          <Plus size={16} />
          New policy
        </Button>
      </div>
    </div>

    {#if isLoading && policies.length === 0}
      <div class="flex items-center gap-2 py-8 text-sm text-text-muted">
        <RefreshCw size={15} class="animate-spin" />
        Loading policies…
      </div>
    {:else if visiblePolicies.length === 0}
      <div class="rounded-[14px] border border-border-subtle bg-surface px-5 py-8 text-center">
        <p class="text-sm text-text-muted">
          No policy{scopeFilter === 'all' ? '' : ` at ${scopeLabel(scopeFilter)} scope`} —
          traffic passes through unscanned.
        </p>
      </div>
    {:else}
      <div class="overflow-hidden rounded-[14px] border border-border-subtle bg-surface">
        <table class="w-full text-sm">
          <thead class="bg-surface-2 text-text-muted">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium">Name</th>
              <th class="px-4 py-2.5 text-left font-medium">Scope</th>
              <th class="px-4 py-2.5 text-left font-medium">Detectors</th>
              <th class="px-4 py-2.5 text-left font-medium">Action</th>
              <th class="hidden px-4 py-2.5 text-left font-medium md:table-cell">Created</th>
              <th class="px-4 py-2.5"></th>
            </tr>
          </thead>
          <tbody>
            {#each visiblePolicies as policy (policy.id)}
              {@const cfg = parseConfig(policy.config)}
              <tr class="border-t border-border-subtle">
                <td class="px-4 py-3">
                  <div class="font-medium text-text-main">{policy.name}</div>
                  {#if policy.scopeId}
                    <div class="mt-0.5 font-code text-xs text-text-subtle">{policy.scopeId}</div>
                  {/if}
                </td>
                <td class="px-4 py-3">
                  <Badge tone="outline" size="sm">{scopeLabel(policy.scope)}</Badge>
                </td>
                <td class="px-4 py-3">
                  <div class="flex flex-wrap gap-1">
                    {#each cfg.detectors as detector (detector)}
                      <Badge tone="primary" size="sm">{detectorLabel(detector)}</Badge>
                    {/each}
                  </div>
                </td>
                <td class="px-4 py-3">
                  <Badge tone={actionTone(cfg.action)} size="sm">{cfg.action}</Badge>
                </td>
                <td class="hidden px-4 py-3 font-code text-xs text-text-subtle md:table-cell">
                  {formatWhen(policy.createdAt)}
                </td>
                <td class="px-4 py-3">
                  <div class="flex items-center justify-end">
                    <button
                      type="button"
                      onclick={() => (pendingDelete = policy)}
                      title="Delete policy"
                      aria-label={`Delete ${policy.name}`}
                      class="rounded-lg p-1.5 text-text-muted transition-colors cursor-pointer hover:bg-error/10 hover:text-error"
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
  </div>

  <!-- Audit log -->
  <div class="space-y-3">
    <div class="flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
      <div class="flex items-center gap-2">
        <Activity size={16} class="text-text-muted" />
        <h2 class="text-base font-semibold text-text-main">Audit log</h2>
      </div>
      <span class="font-code text-xs text-text-subtle">Last {logs.length} decisions</span>
    </div>

    {#if logs.length === 0}
      <div class="rounded-[14px] border border-border-subtle bg-surface px-5 py-8 text-center">
        <p class="inline-flex items-center gap-2 text-sm text-text-muted">
          <KeyRound size={15} />
          No guardrail decisions recorded yet.
        </p>
      </div>
    {:else}
      <div class="overflow-hidden rounded-[14px] border border-border-subtle bg-surface">
        <table class="w-full text-sm">
          <thead class="bg-surface-2 text-text-muted">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium">When</th>
              <th class="px-4 py-2.5 text-left font-medium">Detector</th>
              <th class="px-4 py-2.5 text-left font-medium">Direction</th>
              <th class="px-4 py-2.5 text-left font-medium">Action</th>
              <th class="px-4 py-2.5 text-left font-medium">Severity</th>
            </tr>
          </thead>
          <tbody>
            {#each logs as entry (entry.id)}
              <tr class="border-t border-border-subtle">
                <td class="whitespace-nowrap px-4 py-3 font-code text-xs text-text-subtle">
                  {formatWhen(entry.createdAt)}
                </td>
                <td class="px-4 py-3 font-code text-xs text-text-main">{entry.detector}</td>
                <td class="px-4 py-3 text-text-muted">{entry.direction}</td>
                <td class="px-4 py-3">
                  <Badge tone={actionTone(entry.action)} size="sm">{entry.action}</Badge>
                </td>
                <td class="px-4 py-3 font-code text-xs text-text-subtle">{entry.severity || '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
</div>

<!-- Create policy -->
<Modal
  isOpen={isEditorOpen}
  title="New guardrail policy"
  size="md"
  onClose={() => (isEditorOpen = false)}
>
  <form
    id="guardrail-policy-form"
    class="space-y-4"
    onsubmit={(e) => {
      e.preventDefault()
      createPolicy()
    }}
  >
    <Input
      id="policy-name"
      label="Name"
      placeholder="e.g. Block PII in prompts"
      bind:value={formName}
      required
    />

    <div class="flex flex-col gap-1.5">
      <label for="policy-scope" class="text-sm font-medium text-text-main">Scope</label>
      <select
        id="policy-scope"
        bind:value={formScope}
        class="w-full rounded-[10px] border border-transparent bg-surface-2 px-3 py-2.5 text-sm text-text-main focus:border-brand-500/40 focus:outline-none focus:ring-2 focus:ring-brand-500/30 cursor-pointer"
      >
        {#each SCOPES as option (option.id)}
          <option value={option.id}>{option.label}</option>
        {/each}
      </select>
      <p class="text-xs text-text-subtle">{formScopeHint}</p>
    </div>

    {#if formScope !== 'global'}
      <Input
        id="policy-scope-id"
        label={formScope === 'apikey' ? 'API key id' : `${formScope} id`}
        placeholder={formScope === 'provider'
          ? 'openai'
          : formScope === 'model'
            ? 'gpt-4o-mini'
            : formScope === 'chain'
              ? 'combo name'
              : 'key id'}
        bind:value={formScopeId}
        required
      />
    {/if}

    <div>
      <span class="mb-1.5 block text-sm font-medium text-text-main">Detectors</span>
      <div class="flex flex-wrap gap-2">
        {#each DETECTORS as detector (detector.id)}
          <button
            type="button"
            onclick={() => toggleFormDetector(detector.id)}
            title={detector.hint}
            aria-pressed={formDetectors.includes(detector.id)}
            class="rounded-[10px] border px-3 py-1.5 text-sm transition-colors cursor-pointer {formDetectors.includes(
              detector.id
            )
              ? 'border-brand-500/30 bg-brand-500/10 text-brand-600 dark:text-brand-400'
              : 'border-border bg-surface-2 text-text-muted hover:text-text-main'}"
          >
            {detector.label}
          </button>
        {/each}
      </div>
    </div>

    {#if formDetectors.includes('custom')}
      <div class="space-y-3 border-t border-border-subtle pt-4">
        <h4 class="text-sm font-medium text-text-main">Custom patterns</h4>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Input
            id="custom-name"
            label="Name"
            placeholder="e.g. API key"
            bind:value={customName}
          />
          <Input
            id="custom-regex"
            label="Regex pattern"
            placeholder="e.g. sk-[a-zA-Z0-9]{32}"
            bind:value={customRegex}
          />
          <Input
            id="custom-replacement"
            label="Replacement (optional)"
            placeholder="e.g. [REDACTED]"
            bind:value={customReplacement}
          />
        </div>
        <Button variant="outline" size="sm" onclick={addCustomPattern} disabled={!customRegex.trim()}>
          <Plus size={14} />
          Add pattern
        </Button>
        {#if formCustomPatterns.length > 0}
          <div class="space-y-2">
            {#each formCustomPatterns as pattern, index (index)}
              <div class="flex items-center gap-2 rounded-[10px] border border-border bg-surface-2 px-3 py-2">
                <div class="flex-1 min-w-0">
                  <div class="font-code text-xs text-text-main truncate">{pattern.name}</div>
                  <div class="font-code text-xs text-text-subtle truncate">{pattern.regex}</div>
                  {#if pattern.replacement}
                    <div class="font-code text-xs text-text-muted truncate">→ {pattern.replacement}</div>
                  {/if}
                </div>
                <button
                  type="button"
                  onclick={() => removeCustomPattern(index)}
                  title="Remove pattern"
                  class="rounded-lg p-1 text-text-muted hover:text-error hover:bg-error/10 transition-colors"
                >
                  <Trash2 size={14} />
                </button>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <div class="flex flex-col gap-1.5">
      <label for="policy-action" class="text-sm font-medium text-text-main">Action</label>
      <select
        id="policy-action"
        bind:value={formAction}
        class="w-full rounded-[10px] border border-transparent bg-surface-2 px-3 py-2.5 text-sm text-text-main focus:border-brand-500/40 focus:outline-none focus:ring-2 focus:ring-brand-500/30 cursor-pointer"
      >
        {#each ACTIONS as option (option.id)}
          <option value={option.id}>{option.label}</option>
        {/each}
      </select>
      <p class="text-xs text-text-subtle">{formActionHint}</p>
    </div>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (isEditorOpen = false)} disabled={isCreating}>
      Cancel
    </Button>
    <Button
      variant="primary"
      loading={isCreating}
      disabled={isCreating}
      onclick={createPolicy}
    >
      Create policy
    </Button>
  {/snippet}
</Modal>

<ConfirmModal
  isOpen={!!pendingDelete}
  title="Delete policy"
  message={pendingDelete
    ? `Delete "${pendingDelete.name}"? Its audit history stays, but the rule stops applying immediately.`
    : ''}
  confirmText="Delete"
  onClose={() => (pendingDelete = null)}
  onConfirm={deletePolicy}
/>

<ConfirmModal
  isOpen={isRotateConfirmOpen}
  title="Rotate master key"
  message="Credentials are re-wrapped under the new key. Keep ROUTER_MASTER_KEY_PREVIOUS on the old key, otherwise sealed credentials become unreadable."
  confirmText="Rotate"
  variant="primary"
  loading={isRotating}
  onClose={() => (isRotateConfirmOpen = false)}
  onConfirm={rotateMasterKey}
/>
