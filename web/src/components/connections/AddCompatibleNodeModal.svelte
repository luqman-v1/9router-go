<script lang="ts">
  import { api } from '../../api/client'
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import Modal from '../../lib/ui/Modal.svelte'

  interface Props {
    isOpen: boolean
    type?: 'openai-compatible' | 'anthropic-compatible' | 'custom-embedding'
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
    onClose,
    onSubmit,
  }: Props = $props()

  const VARIANT_CONFIG = {
    'openai-compatible': {
      title: 'Add OpenAI Compatible',
      defaultBaseUrl: 'https://api.openai.com/v1',
      namePlaceholder: 'OpenAI Compatible (Prod)',
      prefixPlaceholder: 'oc-prod',
      baseUrlHint: 'Use the base URL (ending in /v1) for your OpenAI-compatible API.',
      modelIdPlaceholder: 'e.g. gpt-4, claude-3-opus',
      hasApiType: true,
    },
    'anthropic-compatible': {
      title: 'Add Anthropic Compatible',
      defaultBaseUrl: 'https://api.anthropic.com/v1',
      namePlaceholder: 'Anthropic Compatible (Prod)',
      prefixPlaceholder: 'ac-prod',
      baseUrlHint: 'Use the base URL (ending in /v1) for your Anthropic-compatible API. The system will append /messages.',
      modelIdPlaceholder: 'e.g. claude-3-opus',
      hasApiType: false,
    },
    'custom-embedding': {
      title: 'Add Custom Embedding',
      defaultBaseUrl: 'https://api.openai.com/v1',
      namePlaceholder: 'Voyage AI',
      prefixPlaceholder: 'voyage',
      baseUrlHint: 'Most embedding APIs are OpenAI-compatible: Voyage, Cohere, Jina, Mistral, Together...',
      modelIdPlaceholder: 'e.g. voyage-3, embed-english-v3.0, text-embedding-3-small',
      hasApiType: false,
    },
  }

  let config = $derived(VARIANT_CONFIG[type] || VARIANT_CONFIG['openai-compatible'])

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

  // The literal the backend pins in front of the suffix, so the field can show
  // the provider id the user is about to create instead of a bare "suffix".
  let idLiteral = $derived(
    type === 'anthropic-compatible'
      ? 'anthropic-compatible'
      : type === 'custom-embedding'
        ? 'custom-embedding'
        : `openai-compatible-${formApiType}`
  )
  let composedID = $derived(formUrlSuffix.trim() ? `${idLiteral}-${formUrlSuffix.trim()}` : '')

  // Reset the form when the modal opens.
  $effect(() => {
    if (isOpen) {
      formName = ''
      formPrefix = ''
      formUrlSuffix = ''
      formApiType = 'chat'
      checkKey = ''
      checkModelId = ''
      validationResult = null
      submitting = false
      formBaseUrl = config.defaultBaseUrl
    }
  })

  // Mirrors upstream: when the API type changes (OpenAI only), snap the base
  // URL back to the provider default so a stale path isn't reused across API shapes.

  $effect(() => {
    if (config.hasApiType) {
      formBaseUrl = config.defaultBaseUrl
    }
  })

  async function handleCheck() {
    validating = true
    try {
      validationResult = await api.validateProviderNode({
        baseUrl: formBaseUrl.trim(),
        apiKey: checkKey.trim(),
        type,
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
      type,
      urlSuffix: formUrlSuffix.trim(),
    })).finally(() => {
      submitting = false
    })
  }
</script>

<Modal {isOpen} title={config.title} {onClose}>
  <div class="flex flex-col gap-4">
    <Input
      label="Name"
      bind:value={formName}
      placeholder={config.namePlaceholder}
      hint={type === 'custom-embedding' ? 'Required. A friendly label for this embedding provider.' : 'Required. A friendly label for this node.'}
      required
    />

    <Input
      label="Prefix"
      bind:value={formPrefix}
      placeholder={config.prefixPlaceholder}
      hint={type === 'custom-embedding' ? 'Required. Used as the provider prefix for model IDs (e.g. voyage/voyage-3).' : 'Required. Used as the provider prefix for model IDs.'}
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
      hint={type === 'custom-embedding' ? 'Required for validation. Will send a test embeddings request.' : 'If provider lacks /models endpoint, enter a model ID to validate via chat/completions instead.'}
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