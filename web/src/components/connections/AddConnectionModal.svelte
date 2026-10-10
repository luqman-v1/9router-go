<script lang="ts">
  // Port of upstream src/app/(dashboard)/dashboard/providers/[id]/AddApiKeyModal.js
  // Single/Bulk add, credential label per provider, Check (validate) button,
  // Priority + Proxy Pool, provider-specific sub-forms (Azure, Cloudflare
  // Workers AI, Ollama host, Region) and Save/Cancel footer.
  import Button from '../../lib/ui/Button.svelte'
  import { api, type CreateConnectionPayload, type ProxyPool } from '../../api/client'
  import { PROVIDER_CATALOG } from '../../lib/providers'
  import { planBulkAdd } from '../../lib/bulk-add'

  interface Props {
    isOpen: boolean
    providerId?: string
    providerName?: string
    /** Custom OpenAI/Anthropic-compatible node — asks for a default model. */
    isCompatible?: boolean
    isAnthropic?: boolean
    /** Names of already saved connections, so bulk auto-naming never collides. */
    existingNames?: string[]
    proxyPools?: ProxyPool[]
    /** Save error reported by the parent. */
    error?: string
    onClose: () => void
    onSubmit: (payload: CreateConnectionPayload) => Promise<void> | void
    onBulkDone?: () => void
  }

  let {
    isOpen,
    providerId = '',
    providerName = '',
    isCompatible = false,
    isAnthropic = false,
    existingNames = [],
    proxyPools = [],
    error = '',
    onClose,
    onSubmit,
    onBulkDone,
  }: Props = $props()

  const NONE_PROXY_POOL_VALUE = '__none__'
  const BULK_PLACEHOLDER = 'name1|sk-key1\nname2|sk-key2\nsk-key-only-auto-named'

  // Field styling mirrors upstream shared Input/Select (bg-surface-2, rounded 10px).
  const labelCls = 'block text-sm font-medium text-text-main mb-1.5'
  const inputCls =
    'w-full py-2.5 px-3 text-sm text-text-main bg-surface-2 rounded-[10px] border border-transparent placeholder:text-text-muted/70 focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500/40 transition-all duration-150 ease-out'
  const selectWrapCls = 'relative'
  const selectCls =
    'w-full py-2.5 px-3 pr-10 text-sm text-text-main bg-surface-2 border border-transparent rounded-[10px] appearance-none focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500/40 transition-all duration-150'

  // Provider-specific branches (upstream AddApiKeyModal).
  let catalogItem = $derived(PROVIDER_CATALOG.find((p) => p.id === providerId))
  let providerAuthType = $derived(catalogItem?.authType || '')
  let providerAuthHint = $derived(catalogItem?.authHint || '')
  let providerWebsite = $derived(catalogItem?.website || '')
  let isCookie = $derived(providerAuthType === 'cookie' || catalogItem?.category === 'webCookie')
  let isOllamaLocal = $derived(providerId === 'ollama-local')
  let isXaiApiKey = $derived(providerId === 'xai' && !isCookie)
  let isAzure = $derived(providerId === 'azure')
  let isCloudflareAi = $derived(providerId === 'cloudflare-ai')
  // Gate on the catalog flag, not the provider id: keying off `providerId === 'bedrock'`
  // silently left every later AWS entry with no way to enter a profile at all.
  let usesAwsCredentialForm = $derived(catalogItem?.credentialForm === 'aws')
  let providerRegions = $derived(catalogItem?.regions || null)

  let credentialLabel = $derived(
    isCookie ? 'Cookie Value' : providerId === 'qoder' ? 'Personal Access Token (PAT)' : 'API Key'
  )
  let credentialPlaceholder = $derived(
    isCookie
      ? providerId === 'grok-web'
        ? 'sso=xxxxx... or just the raw value'
        : 'eyJhbGciOi...'
      : isXaiApiKey
        ? 'xai-...'
        : providerId === 'qoder'
          ? 'pt-...'
          : usesAwsCredentialForm
            ? 'AWS secret access key'
            : ''
  )
  let modalTitle = $derived(`Add ${providerName || providerId} ${credentialLabel}`)

  // Form state (single mode)
  let mode = $state<'single' | 'bulk'>('single')
  let formName = $state('')
  let formApiKey = $state('')
  let formDefaultModel = $state('')
  let formPriority = $state(1)
  let formProxyPoolId = $state(NONE_PROXY_POOL_VALUE)
  let formOllamaHostUrl = $state('')
  let azureData = $state({
    azureEndpoint: '',
    apiVersion: '2024-10-01-preview',
    deployment: '',
    organization: '',
  })
  let cloudflareAccountId = $state('')
  let region = $state('')
  // The AWS region lives here rather than in the shared `region` state: it is a
  // credential-scoped setting, not one of the catalog's fixed region choices, and the
  // AWS form has to keep working for a provider that declares no `regions` list.
  let awsData = $state({ accessKeyId: '', sessionToken: '', profile: '', region: '' })
  // In profile/SSO mode the connection carries no API key at all — the credential lives
  // in the local AWS config. Declared after awsData so the dependency is real rather
  // than a forward reference.
  let apiKeyOptionalWith = $derived(catalogItem?.apiKeyOptionalWith || '')
  let credentialIsOptional = $derived(apiKeyOptionalWith === 'profile' && awsData.profile.trim())

  let validating = $state(false)
  let validation = $state<'success' | 'failed' | null>(null)
  /** Set when the backend has no probe for this provider — no Valid/Invalid claim. */
  let validationNote = $state('')
  let saving = $state(false)

  // Bulk state
  let bulkText = $state('')
  let bulkResult = $state<{ success: number; failed: number } | null>(null)

  let bulkPlaceholder = $derived(
    isCloudflareAi
      ? 'name1|sk-key1|acc123456\nname2|sk-key2|def789012\nsk-key-only-auto-named'
      : providerId === 'qoder'
        ? 'name1|pt-xxxxx\nname2|pt-yyyyy\npt-only-auto-named'
        : BULK_PLACEHOLDER
  )

  $effect(() => {
    if (isOpen) {
      resetForm()
    }
  })

  // Upstream Modal parity: Escape closes, overlay click closes (see markup below).
  $effect(() => {
    if (!isOpen || typeof window === 'undefined') return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  function resetForm() {
    mode = 'single'
    formName = ''
    formApiKey = ''
    formDefaultModel = ''
    formPriority = 1
    formProxyPoolId = NONE_PROXY_POOL_VALUE
    formOllamaHostUrl = ''
    azureData = { azureEndpoint: '', apiVersion: '2024-10-01-preview', deployment: '', organization: '' }
    cloudflareAccountId = ''
    region = catalogItem?.defaultRegion || catalogItem?.regions?.[0]?.id || ''
    awsData = { accessKeyId: '', sessionToken: '', profile: '', region: region }
    validating = false
    validation = null
    validationNote = ''
    saving = false
    bulkText = ''
    bulkResult = null
  }

  function buildProviderSpecificData(): Record<string, unknown> | undefined {
    if (isOllamaLocal && formOllamaHostUrl.trim()) {
      return { baseUrl: formOllamaHostUrl.trim() }
    }
    if (isAzure) {
      return {
        azureEndpoint: azureData.azureEndpoint,
        apiVersion: azureData.apiVersion,
        deployment: azureData.deployment,
        organization: azureData.organization,
      }
    }
    if (usesAwsCredentialForm) {
      return {
        accessKeyId: awsData.accessKeyId.trim(),
        profile: awsData.profile.trim(),
        // The session token is a credential, so it is written but never echoed back to
        // the edit form. A static ASIA… key without it fails with a clear message.
        ...(awsData.sessionToken.trim() ? { sessionToken: awsData.sessionToken.trim() } : {}),
        ...(awsData.region.trim() ? { region: awsData.region.trim() } : {}),
      }
    }
    if (isCloudflareAi) {
      return { accountId: cloudflareAccountId }
    }
    if (providerRegions && region) {
      return { region }
    }
    return undefined
  }

  /** Compatible nodes carry their pinned model as providerSpecificData.assignedModel. */
  function providerSpecificDataForSave(): Record<string, unknown> | undefined {
    const base = buildProviderSpecificData()
    const assignedModel = isCompatible ? formDefaultModel.trim() : ''
    if (!assignedModel) return base
    return { ...(base || {}), assignedModel }
  }

  async function runValidation(): Promise<boolean> {
    validating = true
    validationNote = ''
    try {
      const res = await api.validateProvider({
        provider: providerId,
        apiKey: formApiKey,
        providerSpecificData: buildProviderSpecificData(),
      })
      if (!res.supported) {
        validation = null
        validationNote = 'Auto-check is not available for this provider — the key is saved as-is.'
        return false
      }
      validation = res.valid ? 'success' : 'failed'
      validationNote = res.valid ? '' : res.error || 'Invalid API key'
      return res.valid
    } catch (err) {
      validation = 'failed'
      validationNote = err instanceof Error ? err.message : String(err)
      return false
    } finally {
      validating = false
    }
  }

  async function handleValidate() {
    await runValidation()
  }

  /**
   * Save is blocked the same way upstream blocks it. The API key is required except when
   * the provider declares a `profile` stand-in and one was typed: in that mode the key
   * lives in the local AWS config and there is nothing to paste.
   */
  let saveDisabled = $derived(
    saving ||
      (!isOllamaLocal && !formName.trim()) ||
      (!credentialIsOptional && !formApiKey.trim()) ||
      (isCompatible && !formDefaultModel.trim()) ||
      (isAzure && (!azureData.azureEndpoint || !azureData.deployment || !azureData.organization)) ||
      (isCloudflareAi && !cloudflareAccountId.trim()) ||
      // Static mode needs both halves of the key pair; profile mode needs a name to
      // resolve, and the API key becomes optional there.
      (usesAwsCredentialForm &&
        (credentialIsOptional
          ? !awsData.profile.trim() || !awsData.region.trim()
          : !awsData.accessKeyId.trim() || !awsData.region.trim()))
  )

  async function handleSubmit(e: SubmitEvent) {
    e.preventDefault()
    if (saveDisabled) return
    saving = true
    try {
      // Validate before saving so the connection gets a real test status.
      const isValid = await runValidation()
      await onSubmit({
        provider: providerId,
        authType: isCompatible ? 'compatible' : 'apikey',
        name: formName.trim(),
        apiKey: formApiKey,
        priority: formPriority,
        proxyPoolId: formProxyPoolId === NONE_PROXY_POOL_VALUE ? null : formProxyPoolId,
        testStatus: isValid ? 'active' : 'unknown',
        providerSpecificData: providerSpecificDataForSave(),
        ...(isCompatible ? { defaultModel: formDefaultModel.trim() } : {}),
      })
    } finally {
      saving = false
    }
  }

  async function handleBulkSubmit() {
    const plan = planBulkAdd(bulkText.split('\n'), existingNames, { isCloudflareAi })
    if (!plan.length) return
    saving = true
    bulkResult = null
    let success = 0
    let failed = 0
    for (const entry of plan) {
      try {
        // Validate each key so bulk-added connections get a real status too.
        const res = await api.validateProvider({
          provider: providerId,
          apiKey: entry.apiKey,
          ...(entry.providerSpecificData ? { providerSpecificData: entry.providerSpecificData } : {}),
        })
        await api.createConnection({
          provider: providerId,
          authType: 'apikey',
          name: entry.name,
          apiKey: entry.apiKey,
          priority: 1,
          testStatus: res.supported && res.valid ? 'active' : 'unknown',
          ...(entry.providerSpecificData ? { providerSpecificData: entry.providerSpecificData } : {}),
        })
        success += 1
      } catch {
        failed += 1
      }
    }
    saving = false
    bulkResult = { success, failed }
    if (success > 0) onBulkDone?.()
  }
</script>

{#if isOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in"
      onclick={onClose}
      role="presentation"
    ></div>
    <div class="relative w-full bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in max-w-md">
      <div class="flex items-center justify-between p-2 border-b border-border-subtle">
        <div class="flex items-center">
          <div class="hidden md:flex items-center gap-2 mr-4 ml-2">
            <button
              type="button"
              onclick={onClose}
              aria-label="Close"
              title="Close"
              class="w-4 h-4 rounded-full bg-[#FF5F56] hover:brightness-90 transition-all cursor-pointer flex items-center justify-center group/dot"
            >
              <span class="text-[9px] font-bold text-white opacity-0 group-hover/dot:opacity-100 transition-opacity leading-none">✕</span>
            </button>
            <div class="w-4 h-4 rounded-full bg-[#3a3a3a]/20 dark:bg-white/15 cursor-not-allowed"></div>
            <div class="w-4 h-4 rounded-full bg-[#3a3a3a]/20 dark:bg-white/15 cursor-not-allowed"></div>
          </div>
          <h2 class="text-lg font-semibold text-text-main">
            {modalTitle}
          </h2>
        </div>
        <button
          type="button"
          onclick={onClose}
          aria-label="Close"
          class="md:hidden p-1.5 rounded-[10px] text-text-muted hover:bg-surface-2 hover:text-text-main transition-colors cursor-pointer"
        >
          <span class="material-symbols-outlined text-[20px]">close</span>
        </button>
      </div>

      <div class="p-6 max-h-[calc(85vh-100px)] overflow-y-auto custom-scrollbar">
      <div class="flex flex-col gap-4">
        <!-- Mode switcher -->
        <div class="flex gap-2">
          <Button
            size="sm"
            variant={mode === 'single' ? 'primary' : 'ghost'}
            onclick={() => {
              mode = 'single'
              bulkResult = null
            }}>Single</Button
          >
          <Button
            size="sm"
            variant={mode === 'bulk' ? 'primary' : 'ghost'}
            onclick={() => {
              mode = 'bulk'
              bulkResult = null
            }}>Bulk Add</Button
          >
        </div>

        {#if mode === 'bulk'}
          <div class="flex flex-col gap-3">
            <p class="text-xs text-text-muted">
              {#if isCloudflareAi}
                One key per line. Format: <code>name|apiKey|accountId</code> or just <code>apiKey</code> (auto-named by index).
              {:else if providerId === 'qoder'}
                One PAT per line. Format: <code>name|pt-...</code> or just <code>pt-...</code> (auto-named by index).
              {:else}
                One key per line. Format: <code>name|apiKey</code> or just <code>apiKey</code> (auto-named by index).
              {/if}
            </p>
            <textarea
              class="w-full rounded border border-accent/30 bg-sidebar p-2 text-sm font-mono resize-y min-h-[140px] text-text-main focus:outline-none focus:ring-1 focus:ring-primary"
              placeholder={bulkPlaceholder}
              bind:value={bulkText}
            ></textarea>
            {#if bulkResult}
              <div class="text-sm font-medium {bulkResult.failed > 0 ? 'text-yellow-400' : 'text-green-400'}">
                ✓ {bulkResult.success} added{bulkResult.failed > 0 ? `, ✗ ${bulkResult.failed} failed` : ''}
              </div>
            {/if}
            <div class="flex gap-2">
              <Button onclick={handleBulkSubmit} fullWidth disabled={saving || !bulkText.trim()}>
                {saving ? 'Adding...' : 'Add All Keys'}
              </Button>
              <Button onclick={onClose} variant="ghost" fullWidth>Cancel</Button>
            </div>
          </div>
        {:else}
          <form onsubmit={handleSubmit} class="flex flex-col gap-4">
            <div>
              <label for="connName" class={labelCls}>
                Name <span class="text-red-500">*</span>
              </label>
              <input
                id="connName"
                type="text"
                required
                bind:value={formName}
                placeholder={isOllamaLocal ? 'Ollama Local' : 'Production Key'}
                class={inputCls}
              />
            </div>

            {#if isOllamaLocal}
              <div class="flex gap-2">
                <div class="flex-1">
                  <label for="ollamaHost" class={labelCls}>Ollama Host URL</label>
                  <input
                    id="ollamaHost"
                    type="text"
                    bind:value={formOllamaHostUrl}
                    placeholder="http://localhost:11434"
                    class="{inputCls} font-mono"
                  />
                </div>
                <div class="pt-8">
                  <Button variant="secondary" onclick={handleValidate} disabled={validating || saving}>
                    {validating ? 'Checking...' : 'Check'}
                  </Button>
                </div>
              </div>
            {:else}
              <div class="flex gap-2">
                <div class="flex-1">
                  <label for="connApiKey" class={labelCls}>
                    {credentialLabel}{#if credentialIsOptional}<span class="text-text-muted text-xs font-normal"> (optional in profile mode)</span>{/if}
                  </label>
                  <input
                    id="connApiKey"
                    type={isCookie ? 'text' : 'password'}
                    required={!credentialIsOptional}
                    bind:value={formApiKey}
                    placeholder={credentialPlaceholder}
                    class="{inputCls} font-mono"
                  />
                </div>
                <div class="pt-8">
                  <Button
                    variant="secondary"
                    onclick={handleValidate}
                    disabled={!formApiKey || validating || saving}
                  >
                    {validating ? 'Checking...' : 'Check'}
                  </Button>
                </div>
              </div>
            {/if}

            {#if usesAwsCredentialForm}
              <div class="bg-sidebar/50 p-4 rounded-lg border border-accent/20 flex flex-col gap-3">
                <h3 class="font-semibold text-sm">AWS Bedrock Credentials</h3>
                <div>
                  <label for="awsAccessKeyId" class={labelCls}>Access Key ID</label>
                  <input
                    id="awsAccessKeyId"
                    type="text"
                    bind:value={awsData.accessKeyId}
                    placeholder="AKIA… / ASIA…"
                    class="{inputCls} font-mono"
                  />
                </div>
                <div>
                  <label for="awsSessionToken" class={labelCls}>Session Token</label>
                  <input
                    id="awsSessionToken"
                    type="password"
                    bind:value={awsData.sessionToken}
                    placeholder="Only for temporary (ASIA…) keys"
                    class="{inputCls} font-mono"
                  />
                </div>
                <div>
                  <label for="awsProfile" class={labelCls}>Profile</label>
                  <input
                    id="awsProfile"
                    type="text"
                    bind:value={awsData.profile}
                    placeholder="Name from ~/.aws/config"
                    class="{inputCls} font-mono"
                  />
                </div>
                <div>
                  <label for="awsRegion" class={labelCls}>Region</label>
                  <input
                    id="awsRegion"
                    type="text"
                    bind:value={awsData.region}
                    placeholder="us-east-1"
                    class="{inputCls} font-mono"
                  />
                </div>
                <p class="text-xs text-text-muted mt-2">
                  SSO / profile (recommended): fill in Profile and Region, leave the API key empty,
                  then run <code>aws sso login --profile &lt;name&gt;</code> — credentials refresh
                  automatically. Static keys: put the AWS secret access key above and the key id
                  here, adding a Session Token for temporary keys. A profile, if set, takes
                  precedence over static keys.
                </p>
              </div>
            {/if}

            {#if validation === 'success' || validation === 'failed'}
              <div class="flex items-center gap-2 -mt-2">
                <span
                  class="inline-flex items-center gap-1.5 rounded-full font-semibold px-2.5 py-1 text-xs {validation === 'success'
                    ? 'bg-green-500/10 text-green-600 dark:text-green-400'
                    : 'bg-red-500/10 text-red-600 dark:text-red-400'}"
                >
                  {validation === 'success' ? 'Valid' : 'Invalid'}
                </span>
                {#if validationNote}
                  <span class="text-xs {validation === 'success' ? 'text-text-muted' : 'text-red-500'} break-words">{validationNote}</span>
                {/if}
              </div>
            {:else if validationNote}
              <p class="text-xs text-text-muted break-words -mt-2">{validationNote}</p>
            {/if}
            {#if isXaiApiKey}
              <p class="text-xs text-text-muted">
                Use a direct xAI API key from console.x.ai. This is separate from Grok Build OAuth.
              </p>
            {/if}
            {#if isCookie && providerAuthHint}
              <p class="text-xs text-text-muted">
                {providerAuthHint}
                {#if providerWebsite}
                  {' '}
                  <a href={providerWebsite} target="_blank" rel="noopener noreferrer" class="text-primary underline">
                    Open {providerWebsite.replace(/^https?:\/\//, '')}
                  </a>
                {/if}
              </p>
            {/if}

            {#if providerRegions}
              <div>
                <label for="connRegion" class={labelCls}>Region</label>
                <div class={selectWrapCls}>
                  <select
                    id="connRegion"
                    bind:value={region}
                    class={selectCls}
                  >
                    {#each providerRegions as r (r.id)}
                      <option value={r.id}>{r.label}</option>
                    {/each}
                  </select>
                  <div class="absolute inset-y-0 right-0 flex items-center pr-3 pointer-events-none text-text-muted">
                    <span class="material-symbols-outlined text-[20px]">expand_more</span>
                  </div>
                </div>
              </div>
            {/if}

            {#if isCompatible}
              <div>
                <label for="connDefaultModel" class={labelCls}>
                  Default Model <span class="text-red-500">*</span>
                </label>
                <input
                  id="connDefaultModel"
                  type="text"
                  bind:value={formDefaultModel}
                  placeholder={isAnthropic ? 'claude-3-5-sonnet-latest' : 'gpt-4o-mini'}
                  class="{inputCls} font-mono"
                />
              </div>
            {/if}

            {#if isOllamaLocal}
              <p class="text-xs text-text-muted">
                Leave blank to use <code>http://localhost:11434</code>. For remote Ollama, enter the full host URL (e.g. <code>http://192.168.1.10:11434</code>).
              </p>
            {/if}

            {#if isCloudflareAi}
              <div class="bg-sidebar/50 p-4 rounded-lg border border-accent/20 flex flex-col gap-3">
                <h3 class="font-semibold mb-3 text-sm">Cloudflare Workers AI</h3>
                <div>
                  <label for="cfAccountId" class={labelCls}>Account ID</label>
                  <input
                    id="cfAccountId"
                    type="text"
                    bind:value={cloudflareAccountId}
                    placeholder="abc123def456..."
                    class="{inputCls} font-mono"
                  />
                </div>
                <p class="text-xs text-text-muted mt-2">
                  Find your Account ID in the right sidebar of
                  <a href="https://dash.cloudflare.com" target="_blank" rel="noopener noreferrer" class="text-primary underline"
                    >dash.cloudflare.com</a
                  >
                </p>
              </div>
            {/if}

            {#if isAzure}
              <div class="bg-sidebar/50 p-4 rounded-lg border border-accent/20">
                <h3 class="font-semibold mb-3 text-sm">Azure OpenAI Configuration</h3>
                <div class="flex flex-col gap-3">
                  <div>
                    <label for="azureEndpoint" class={labelCls}>Azure Endpoint</label>
                    <input
                      id="azureEndpoint"
                      type="text"
                      bind:value={azureData.azureEndpoint}
                      placeholder="https://your-resource.openai.azure.com"
                      class="{inputCls} font-mono"
                    />
                  </div>
                  <div>
                    <label for="azureDeployment" class={labelCls}>Deployment Name</label>
                    <input
                      id="azureDeployment"
                      type="text"
                      bind:value={azureData.deployment}
                      placeholder="gpt-4"
                      class="{inputCls} font-mono"
                    />
                  </div>
                  <div>
                    <label for="azureApiVersion" class={labelCls}>API Version</label>
                    <input
                      id="azureApiVersion"
                      type="text"
                      bind:value={azureData.apiVersion}
                      placeholder="2024-10-01-preview"
                      class="{inputCls} font-mono"
                    />
                  </div>
                  <div>
                    <label for="azureOrganization" class={labelCls}>Organization</label>
                    <input
                      id="azureOrganization"
                      type="text"
                      bind:value={azureData.organization}
                      placeholder="Organization ID"
                      class="{inputCls} font-mono"
                    />
                  </div>
                </div>
              </div>
            {/if}

            {#if error}
              <p class="text-xs text-red-500 break-words">{error}</p>
            {/if}

            {#if isCompatible}
              <p class="text-xs text-text-muted">
                Enter the model ID exactly as your compatible endpoint expects it. This model will be saved as the
                connection default.
              </p>
            {/if}

            <div>
              <label for="connPriority" class={labelCls}>Priority</label>
              <input
                id="connPriority"
                type="number"
                min="1"
                bind:value={formPriority}
                class={inputCls}
              />
            </div>

            <div>
              <label for="connProxyPool" class={labelCls}>Proxy Pool</label>
              <div class={selectWrapCls}>
                <select
                  id="connProxyPool"
                  bind:value={formProxyPoolId}
                  class={selectCls}
                >
                  <option value={NONE_PROXY_POOL_VALUE}>None</option>
                  {#each proxyPools as pool (pool.id)}
                    <option value={pool.id}>{pool.name}</option>
                  {/each}
                </select>
                <div class="absolute inset-y-0 right-0 flex items-center pr-3 pointer-events-none text-text-muted">
                  <span class="material-symbols-outlined text-[20px]">expand_more</span>
                </div>
              </div>
            </div>

            {#if proxyPools.length === 0}
              <p class="text-xs text-text-muted">
                No active proxy pools available. Create one in Proxy Pools page first.
              </p>
            {/if}

            <div class="flex gap-2">
              <Button type="submit" fullWidth disabled={saveDisabled}>
                {saving ? 'Saving...' : 'Save'}
              </Button>
              <Button onclick={onClose} variant="ghost" fullWidth>Cancel</Button>
            </div>
          </form>
        {/if}
      </div>
      </div>
    </div>
  </div>
{/if}
