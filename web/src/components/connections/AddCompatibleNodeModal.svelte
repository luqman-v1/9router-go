<script lang="ts">
  import { api } from '../../api/client'
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import Modal from '../../lib/ui/Modal.svelte'

  export type CompatibleNodeType = 'openai-compatible' | 'anthropic-compatible' | 'custom-embedding'

  interface Props {
    isOpen: boolean
    /** Initial node type. The user can switch it inside the modal. */
    type?: CompatibleNodeType
    /** Types offered by the type switch; omit to offer all three. */
    allowedTypes?: CompatibleNodeType[]
    isSubmitting?: boolean
    onClose: () => void
    onSubmit: (data: {
      name: string
      prefix: string
      baseUrl: string
      apiType?: 'chat' | 'responses'
      type: string
      urlSuffix: string
    }) => Promise<void> | void
  }

  let {
    isOpen,
    type = 'openai-compatible',
    allowedTypes = ['openai-compatible', 'anthropic-compatible'],
    onClose,
    onSubmit,
  }: Props = $props()

  const VARIANT_CONFIG: Record<CompatibleNodeType, {
    label: string
    summary: string
    title: string
    defaultBaseUrl: string
    namePlaceholder: string
    prefixPlaceholder: string
    baseUrlHint: string
    modelIdPlaceholder: string
    nameHint: string
    prefixHint: string
    modelIdHint: string
    hasApiType: boolean
  }> = {
    'openai-compatible': {
      label: 'OpenAI Compatible',
      summary: 'Chat Completions or Responses endpoints',
      title: 'Add Custom Provider',
      defaultBaseUrl: 'https://api.openai.com/v1',
      namePlaceholder: 'OpenAI Compatible (Prod)',
      prefixPlaceholder: 'oc-prod',
      baseUrlHint: 'Use the base URL (ending in /v1) for your OpenAI-compatible API.',
      modelIdPlaceholder: 'e.g. gpt-4, claude-3-opus',
      nameHint: 'Required. A friendly label for this node.',
      prefixHint: 'Required. Used as the provider prefix for model IDs.',
      modelIdHint: 'If provider lacks /models endpoint, enter a model ID to validate via chat/completions instead.',
      hasApiType: true,
    },
    'anthropic-compatible': {
      label: 'Anthropic Compatible',
      summary: 'Endpoints speaking the Anthropic Messages API',
      title: 'Add Custom Provider',
      defaultBaseUrl: 'https://api.anthropic.com/v1',
      namePlaceholder: 'Anthropic Compatible (Prod)',
      prefixPlaceholder: 'ac-prod',
      baseUrlHint: 'Use the base URL (ending in /v1) for your Anthropic-compatible API. The system will append /messages.',
      modelIdPlaceholder: 'e.g. claude-3-opus',
      nameHint: 'Required. A friendly label for this node.',
      prefixHint: 'Required. Used as the provider prefix for model IDs.',
      modelIdHint: 'If provider lacks /models endpoint, enter a model ID to validate via messages instead.',
      hasApiType: false,
    },
    'custom-embedding': {
      label: 'Custom Embedding',
      summary: 'OpenAI-compatible /embeddings endpoints',
      title: 'Add Custom Embedding',
      defaultBaseUrl: 'https://api.openai.com/v1',
      namePlaceholder: 'Voyage AI',
      prefixPlaceholder: 'voyage',
      baseUrlHint: 'Most embedding APIs are OpenAI-compatible: Voyage, Cohere, Jina, Mistral, Together...',
      modelIdPlaceholder: 'e.g. voyage-3, embed-english-v3.0, text-embedding-3-small',
      nameHint: 'Required. A friendly label for this embedding provider.',
      prefixHint: 'Required. Used as the provider prefix for model IDs (e.g. voyage/voyage-3).',
      modelIdHint: 'Required for validation. Will send a test embeddings request.',
      hasApiType: false,
    },
  }

  // The node type lives in local state so switching it inside the modal keeps
  // every field the user already typed. Picking the wrong protocol used to
  // mean closing the modal and retyping the whole form (#182).
  let selectedType = $state<CompatibleNodeType>('openai-compatible')

  let formName = $state('')
  let formPrefix = $state('')
  let formUrlSuffix = $state('')
  let formApiType = $state<'chat' | 'responses'>('chat')
  let formBaseUrl = $state('')
  let checkKey = $state('')
  let checkModelId = $state('')
  let validating = $state(false)
  let validationResult = $state<{ valid: boolean; error?: string; method?: string; dimensions?: number } | null>(null)
  let submitting = $state(false)

  let typeOptions = $derived(allowedTypes.map((t) => ({ value: t, ...VARIANT_CONFIG[t] })))
  let config = $derived(VARIANT_CONFIG[selectedType])

  // The literal the backend pins in front of the suffix, so the field can show
  // the provider id the user is about to create instead of a bare "suffix".
  let idLiteral = $derived(
    selectedType === 'anthropic-compatible'
      ? 'anthropic-compatible'
      : selectedType === 'custom-embedding'
        ? 'custom-embedding'
        : `openai-compatible-${formApiType}`
  )
  let composedID = $derived(formUrlSuffix.trim() ? `${idLiteral}-${formUrlSuffix.trim()}` : '')

  // Reset the form when the modal opens.
  $effect(() => {
    if (isOpen) {
      selectedType = type
      formName = ''
      formPrefix = ''
      formUrlSuffix = ''
      formApiType = 'chat'
      checkKey = ''
      checkModelId = ''
      validationResult = null
      submitting = false
      formBaseUrl = (VARIANT_CONFIG[type] ?? VARIANT_CONFIG[selectedType]).defaultBaseUrl
    }
  })

  // Switching type mid-form is the whole point of the unified dialog (#182), so
  // only the protocol-specific parts move: the base URL follows the new
  // protocol when it still holds the old default, everything the user typed
  // survives, and a stale Check result is dropped rather than shown as valid.
  function selectType(next: CompatibleNodeType) {
    if (next === selectedType) return
    const previousDefault = VARIANT_CONFIG[selectedType].defaultBaseUrl
    selectedType = next
    validationResult = null
    if (formBaseUrl.trim() === '' || formBaseUrl.trim() === previousDefault) {
      formBaseUrl = VARIANT_CONFIG[next].defaultBaseUrl
    }
  }


  async function handleCheck() {
    validating = true
    try {
      validationResult = await api.validateProviderNode({
        baseUrl: formBaseUrl.trim(),
        apiKey: checkKey.trim(),
        type: selectedType,
        modelId: checkModelId.trim() || undefined,
      })
    } catch (err) {
      validationResult = {
        valid: false,
        error: err instanceof Error ? err.message : String(err),
      }
    } finally {
      validating = false
    }
  }

  function handleSubmit(e?: SubmitEvent) {
    e?.preventDefault()
    if (!formName.trim() || !formPrefix.trim() || !formBaseUrl.trim() || submitting) return
    submitting = true
    Promise.resolve(onSubmit({
      name: formName.trim(),
      prefix: formPrefix.trim(),
      baseUrl: formBaseUrl.trim(),
      apiType: config.hasApiType ? formApiType : undefined,
      type: selectedType,
      urlSuffix: formUrlSuffix.trim(),
    })).finally(() => {
      submitting = false
    })
  }
</script>

<Modal {isOpen} title={config.title} {onClose}>
  <div class="flex flex-col gap-4">
    <fieldset class="flex flex-col gap-2">
      <legend class="text-sm font-medium text-text-main">Provider Type</legend>
      <div class="grid grid-cols-1 gap-2 sm:grid-cols-2" role="radiogroup" aria-label="Provider Type">
        {#each typeOptions as option (option.value)}
          <button
            type="button"
            role="radio"
            aria-checked={selectedType === option.value}
            onclick={() => selectType(option.value)}
            class="flex flex-col items-start gap-0.5 rounded-[10px] border px-3 py-2 text-left transition-colors cursor-pointer focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 {selectedType === option.value
              ? 'border-brand-500 bg-brand-500/10'
              : 'border-border bg-surface-2 hover:border-brand-500/40'}"
          >
            <span class="flex w-full items-center justify-between gap-2">
              <span class="text-sm font-medium text-text-main">{option.label}</span>
              {#if selectedType === option.value}
                <span class="material-symbols-outlined text-[16px] text-brand-500" aria-hidden="true">check</span>
              {/if}
            </span>
            <span class="text-xs text-text-muted">{option.summary}</span>
          </button>
        {/each}
      </div>
      <p class="text-xs text-text-muted">
        Already entered your endpoint? Switch type here — your name, prefix and base URL stay put.
      </p>
    </fieldset>

    <Input
      label="Name"
      bind:value={formName}
      placeholder={config.namePlaceholder}
      hint={config.nameHint}
      required
    />

    <Input
      label="Prefix"
      bind:value={formPrefix}
      placeholder={config.prefixPlaceholder}
      hint={config.prefixHint}
      required
    />

    <Input
      label="Custom URL Suffix"
      bind:value={formUrlSuffix}
      placeholder="e.g. bai"
      hint={composedID
        ? `Optional. Replaces the random suffix of this provider id: ${composedID}`
        : `Optional. Leave blank to keep a randomized suffix for '${idLiteral}-<this_is_user_custom_suffix>'.`}
      inputClass="font-mono"
    />

    {#if config.hasApiType}
      <div>
        <label for="api-type" class="text-sm font-medium text-text-main mb-1.5 block">API Type</label>
        <select
          id="api-type"
          bind:value={formApiType}
          class="w-full py-2.5 px-3 text-sm text-text-main bg-surface-2 rounded-[10px] border border-transparent focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500/40 transition-all duration-150 ease-out text-[16px] sm:text-sm"
        >
          <option value="chat">Chat Completions</option>
          <option value="responses">Responses API</option>
        </select>
      </div>
    {/if}

    <Input
      label="Base URL"
      bind:value={formBaseUrl}
      placeholder={config.defaultBaseUrl}
      hint={config.baseUrlHint}
      inputClass="font-mono"
      required
    />

    <Input
      label="API Key (for Check)"
      type="password"
      bind:value={checkKey}
    />

    <Input
      label="Model ID (for Check)"
      bind:value={checkModelId}
      placeholder={config.modelIdPlaceholder}
      hint={config.modelIdHint}
    />

    <div class="flex flex-col gap-3 sm:flex-row sm:items-center">
      <Button
        onclick={handleCheck}
        disabled={!checkKey.trim() || validating || !formBaseUrl.trim()}
        variant="secondary"
        class="w-full sm:w-auto"
      >
        {validating ? 'Checking...' : 'Check'}
      </Button>
      {#if validationResult}
        {#if validationResult.valid}
          <span class="inline-flex items-center gap-1.5">
            <Badge tone="success">Valid</Badge>
            {#if validationResult.method === 'chat'}
              <span class="text-sm text-text-muted">(via inference test)</span>
            {:else if validationResult.dimensions}
              <span class="text-sm text-text-muted">{validationResult.dimensions} dims</span>
            {/if}
          </span>
        {:else}
          <div class="flex flex-col gap-1">
            <Badge tone="error">Invalid</Badge>
            {#if validationResult.error}
              <span class="text-sm text-red-500">{validationResult.error}</span>
            {/if}
          </div>
        {/if}
      {/if}
    </div>

    <div class="flex flex-col gap-2 sm:flex-row">
      <Button
        type="button"
        onclick={handleSubmit}
        fullWidth
        disabled={!formName.trim() || !formPrefix.trim() || !formBaseUrl.trim() || submitting}
      >
        {submitting ? 'Creating...' : 'Create'}
      </Button>
      <Button onclick={onClose} variant="ghost" fullWidth>
        Cancel
      </Button>
    </div>
  </div>
</Modal>