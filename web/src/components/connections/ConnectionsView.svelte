<script lang="ts">
  import { api, type ProviderConnection, type ProviderNode, type ProviderNodeLimitsPatch } from '../../api/client'
  import { hasAnyNodeLimitPatch } from '../../lib/node-limits'
  import ProvidersOverviewGrid from './ProvidersOverviewGrid.svelte'
  import ProviderDetailView from './ProviderDetailView.svelte'
  import AddCompatibleNodeModal from './AddCompatibleNodeModal.svelte'

  interface Props {
    connections?: ProviderConnection[]
    providerNodes?: ProviderNode[]
    onRefresh: () => void
    selectedProviderId?: string | null
    onSelectProvider?: (id: string) => void
    onBackToOverview?: () => void
  }

  let {
    connections = [],
    providerNodes = [],
    onRefresh,
    selectedProviderId = $bindable(null),
    onSelectProvider,
    onBackToOverview
  }: Props = $props()

  let showAddNodeModal = $state(false)

  // The dialog owns the protocol choice now (#182), so opening it takes no
  // argument — passing one through onclick would hand this a MouseEvent.
  function openAddNodeModal() {
    showAddNodeModal = true
  }

  async function handleToggleAll(providerId: string, newActive: boolean) {
    const conns = connections.filter((c) => c.provider === providerId)
    await Promise.allSettled(
      conns.map((c) => api.updateConnection(c.id, { isActive: newActive ? 1 : 0 }))
    )
    onRefresh()
  }

  async function handleCreateNode(data: {
    name: string
    prefix: string
    baseUrl: string
    apiType?: 'chat' | 'responses'
    type: string
    urlSuffix: string
    limits: ProviderNodeLimitsPatch
  }) {
    try {
      const node = await api.createProviderNode({
        name: data.name,
        prefix: data.prefix,
        baseUrl: data.baseUrl,
        apiType: data.apiType,
        type: data.type as unknown as string,
        urlSuffix: data.urlSuffix || undefined
      })
      // The limits endpoint needs the id the create just answered with, so this is
      // deliberately a second request: a node whose limits failed to save is still a
      // working node, and saying so beats losing the create with it.
      if (hasAnyNodeLimitPatch(data.limits)) {
        try {
          await api.setProviderNodeLimits(node.id, data.limits)
        } catch (err) {
          alert(
            `Provider node created, but its capacity limits did not save: ${err instanceof Error ? err.message : String(err)}`,
          )
        }
      }
      showAddNodeModal = false
      onRefresh()
      selectedProviderId = node.id
      onSelectProvider?.(node.id)
    } catch (err) {
      alert(`Failed to add provider node: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
</script>

{#if selectedProviderId}
  <ProviderDetailView
    providerId={selectedProviderId}
    {connections}
    {providerNodes}
    onBack={() => {
      selectedProviderId = null
      onBackToOverview?.()
    }}
    {onRefresh}
    {onSelectProvider}
  />
{:else}
  <ProvidersOverviewGrid
    {connections}
    {providerNodes}
    onSelectProvider={(id) => {
      selectedProviderId = id
      onSelectProvider?.(id)
    }}
    onToggleAll={handleToggleAll}
    onAddCustom={openAddNodeModal}
  />
{/if}

<AddCompatibleNodeModal
  isOpen={showAddNodeModal}
  type="openai-compatible"
  allowedTypes={['openai-compatible', 'anthropic-compatible']}
  onClose={() => (showAddNodeModal = false)}
  onSubmit={handleCreateNode}
/>
