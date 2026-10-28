<script lang="ts">
  // Port of upstream EditCompatibleNodeModal.js (dashboard/providers/[id]).
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import { api, type ProviderNode, type ProviderNodeLimits, type ProviderNodeLimitsPatch } from '../../api/client'
  import { nodeFormSeed } from './nodeFormSeed'
  import ProviderNodeLimitsField from './ProviderNodeLimitsField.svelte'
  import { nodeLimitsDraftOf, nodeLimitsPatchOf, type NodeLimitsDraft } from '../../lib/node-limits'

  interface Props {
    isOpen: boolean
    node?: ProviderNode | null
    isAnthropic?: boolean
    onClose: () => void
    onSave: (data: {
      name: string
      prefix: string
      apiType?: string
      baseUrl: string
      urlSuffix: string
      /** The policy to store; the parent writes it once the node id is final. */
      limits: ProviderNodeLimitsPatch
    }) => Promise<void> | void
    /** Called with the stored policy after a token-count reset, so the page updates. */
    onLimitsSaved?: (limits: ProviderNodeLimits) => void
  }

  let { isOpen, node = null, isAnthropic = false, onClose, onSave, onLimitsSaved }: Props = $props()

  let formName = $state('')
  let formPrefix = $state('')
  let formUrlSuffix = $state('')
  let formApiType = $state<'chat' | 'responses'>('chat')
  let formBaseUrl = $state('')
  let checkKey = $state('')
  let checkModelId = $state('')
  let validating = $state(false)
  let validation = $state<'success' | 'failed' | null>(null)
  let saving = $state(false)
  let limitsDraft = $state<NodeLimitsDraft>(nodeLimitsDraftOf(null))
  let limitsError = $state('')
  // A reset answers with the fresh policy before the page's poll does, so show that
  // instead of mutating the `node` prop.
  let limitsAfterReset = $state<ProviderNodeLimits | null>(null)

  // Seed once per open, not once per `node` object.
  //
  // The dashboard polls /api/provider-nodes every 10s and replaces the array,
  // so `selectedNode` arrives here as a brand-new object with identical
  // contents every tick. Keying the seed on that identity re-ran this effect
  // while the user was still typing, overwriting every unsaved field with the
  // stored value (issue #234). The stable identity of a node is its id, and a
  // genuine edit — a different node, or a reopen — still has to reseed, or the
  // form would open carrying the previous node's values.
  //
  // Plain variables, not $state: writing them must not be something the effect
  // reads back and re-trigger on.
  let seededNodeId: string | null = null
  let wasOpen = false

  $effect(() => {
    const open = isOpen
    const id = node?.id ?? null
    // A node that only arrives after the modal opened (id goes null -> id) is
    // a first seed too, which is why identity is compared against the last
    // seeded one rather than only against `wasOpen`.
    const shouldSeed = open && !!node && (!wasOpen || id !== seededNodeId)
    wasOpen = open
    if (!shouldSeed || !node) return

    seededNodeId = id
    const seed = nodeFormSeed(node, isAnthropic)
    formName = seed.name
    formPrefix = seed.prefix
    formUrlSuffix = seed.urlSuffix
    formApiType = seed.apiType
    formBaseUrl = seed.baseUrl
    checkKey = ''
    checkModelId = ''
    validation = null
    saving = false
    limitsDraft = nodeLimitsDraftOf(node.limits ?? null)
    limitsError = ''
    limitsAfterReset = null
  })

  let saveDisabled = $derived(
    saving || !formName.trim() || !formPrefix.trim() || !formBaseUrl.trim()
  )

  async function handleValidate() {
    if (!checkKey || validating || !formBaseUrl.trim()) return
    validating = true
    try {
      const res = await api.validateProviderNode({
        baseUrl: formBaseUrl.trim(),
        apiKey: checkKey,
        type: isAnthropic ? 'anthropic-compatible' : 'openai-compatible',
        modelId: checkModelId.trim() || undefined,
      })
      validation = res.valid ? 'success' : 'failed'
    } catch {
      validation = 'failed'
    } finally {
      validating = false
    }
  }

  async function handleSubmit() {
    if (saveDisabled) return
    saving = true
    limitsError = ''
    try {
      const payload: {
        name: string
        prefix: string
        apiType?: string
        baseUrl: string
        urlSuffix: string
        limits: ProviderNodeLimitsPatch
      } = {
        name: formName.trim(),
        prefix: formPrefix.trim(),
        baseUrl: formBaseUrl.trim(),
        urlSuffix: formUrlSuffix.trim(),
        // The policy travels with the node save, and the page writes it *after* the
        // node: an edited URL suffix renames the provider id, and the limits columns
        // live on that row. Writing them here, against the id this dialog still
        // holds, is how an edit would leave the policy on an id that no longer
        // exists — the node would come back unlimited.
        limits: nodeLimitsPatchOf(limitsDraft),
      }
      if (!isAnthropic) payload.apiType = formApiType
      await onSave(payload)
    } catch (err) {
      limitsError = err instanceof Error ? err.message : String(err)
    } finally {
      saving = false
    }
  }
</script>

{#if node}
<Modal {isOpen} title={`Edit ${isAnthropic ? 'Anthropic' : 'OpenAI'} Compatible`} {onClose}>
  <div class="flex flex-col gap-4">
    <Input
      label="Name"
      bind:value={formName}
      placeholder={`${isAnthropic ? 'Anthropic' : 'OpenAI'} Compatible (Prod)`}
      hint="Required. A friendly label for this node."
    />
    <Input
      label="Prefix"
      bind:value={formPrefix}
      placeholder={isAnthropic ? 'ac-prod' : 'oc-prod'}
      hint="Required. Used as the provider prefix for model IDs."
    />
    <Input
      label="Custom URL Suffix"
      bind:value={formUrlSuffix}
      placeholder="e.g. bai"
      hint={node.urlSuffixGenerated
        ? 'Optional. This provider still carries a randomized suffix; fill this in to name it.'
        : 'Optional. Changing this renames the provider id and carries this node\'s credentials, models and history with it.'}
      inputClass="font-mono"
    />
    {#if !isAnthropic}
      <div>
        <label for="edit-node-api-type" class="text-sm font-medium text-text-main mb-1.5 block">API Type</label>
        <select
          id="edit-node-api-type"
          bind:value={formApiType}
          class="w-full py-2.5 px-3 pr-10 text-sm text-text-main bg-surface-2 border border-transparent rounded-[10px] appearance-none focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500/40 transition-all duration-150"
        >
          <option value="chat">Chat Completions</option>
          <option value="responses">Responses API</option>
        </select>
      </div>
    {/if}
    <Input
      label="Base URL"
      bind:value={formBaseUrl}
      placeholder={isAnthropic ? 'https://api.anthropic.com/v1' : 'https://api.openai.com/v1'}
      hint={`Use the base URL (ending in /v1) for your ${isAnthropic ? 'Anthropic' : 'OpenAI'}-compatible API.`}
      inputClass="font-mono"
    />
    <div class="flex gap-2">
      <div class="flex-1">
        <Input
          label="API Key (for Check)"
          type="password"
          bind:value={checkKey}
        />
      </div>
      <div class="pt-6">
        <Button
          variant="secondary"
          onclick={handleValidate}
          disabled={!checkKey || validating || !formBaseUrl.trim()}
        >
          {validating ? 'Checking...' : 'Check'}
        </Button>
      </div>
    </div>
    <Input
      label="Model ID (optional)"
      bind:value={checkModelId}
      placeholder="e.g. my-model-id"
      hint="If provider lacks /models endpoint, enter a model ID to validate via chat/completions instead."
    />
    {#if validation}
      <Badge variant={validation === 'success' ? 'success' : 'error'}>
        {validation === 'success' ? 'Valid' : 'Invalid'}
      </Badge>
    {/if}
    <!-- Capacity limits use the same fields as the ones above, separated by a rule
         and collapsed until needed; the Save below stores both, so these are not a
         second form with a second button. -->
    <ProviderNodeLimitsField
      bind:draft={limitsDraft}
      usage={limitsAfterReset ?? node.limits ?? null}
      nodeId={node.id}
      onReset={(limits) => {
        limitsAfterReset = limits
        onLimitsSaved?.(limits)
      }}
    />
    {#if limitsError}
      <p class="text-xs text-red-500" role="alert">{limitsError}</p>
    {/if}
    <div class="flex gap-2">
      <Button onclick={handleSubmit} fullWidth disabled={saveDisabled}>
        {saving ? 'Saving...' : 'Save'}
      </Button>
      <Button onclick={onClose} variant="ghost" fullWidth>Cancel</Button>
    </div>
  </div>
</Modal>
{/if}
