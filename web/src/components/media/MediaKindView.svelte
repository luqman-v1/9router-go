<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type APIKey, type Combo, type ProviderConnection, type ProviderNode, type Settings } from '../../api/client'
  import { getProvidersByKind, type ProviderCatalogItem } from '../../lib/providers'
  import Badge from '../../lib/ui/Badge.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import AddCompatibleNodeModal from '../connections/AddCompatibleNodeModal.svelte'
  import MediaProviderCard from './MediaProviderCard.svelte'
  import MediaProviderDetail from './MediaProviderDetail.svelte'
  import { MEDIA_KIND_INFO, type MediaKind } from './mediaTypes'

  interface Props {
    kind: MediaKind
    connections?: ProviderConnection[]
    apiKeys?: APIKey[]
    settings?: Settings
    combos?: Combo[]
    onRefresh: () => void
    onSelectProvider?: (kind: string, id: string) => void
    initialProviderId?: string | null
  }

  let {
    kind,
    connections = [],
    apiKeys = [],
    settings = {},
    combos = [],
    onRefresh,
    onSelectProvider,
    initialProviderId = null,
  }: Props = $props()

  let selectedProvider = $state<ProviderCatalogItem | null>(null)
  let customNodes = $state<ProviderNode[]>([])
  let showCustomModal = $state(false)

  const COMBO_KINDS = new Set<string>([])
  const COMBO_BASE_NAMES: Record<string, string> = { image: 'image-combo', tts: 'tts-combo' }

  let kindConfig = $derived(MEDIA_KIND_INFO[kind])
  let isEmbedding = $derived(kind === 'embedding')
  let supportsCombo = $derived(COMBO_KINDS.has(kind))
  let kindCombos = $derived(combos.filter((c) => (c as any).kind === kind))

  let providers = $derived(getProvidersByKind(kind))

  $effect(() => {
    if (initialProviderId && providers.length > 0) {
      const match = providers.find((p) => p.id === initialProviderId)
      if (match) selectedProvider = match
    }
  })

  onMount(() => {
    if (isEmbedding) {
      api.getProviderNodes().then((nodes) => {
        customNodes = (nodes || []).filter((n) => n.type === 'custom-embedding')
      }).catch(() => {})
    }
  })

  let customProviders = $derived(
    customNodes.map((n) => ({
      id: n.id,
      name: n.name || 'Custom Embedding',
      category: 'custom' as const,
      alias: n.prefix || n.id,
      color: '#6366F1',
      icon: 'data_array',
      serviceKinds: ['embedding'],
      noAuth: true,
    } as ProviderCatalogItem))
  )

  let allProviders = $derived([...providers, ...customProviders])

  async function handleToggleProvider(providerId: string, newActive: boolean) {
    const list = connections.filter((c) => c.provider === providerId)
    await Promise.allSettled(
      list.map((c) => api.updateConnection(c.id, { isActive: newActive ? 1 : 0 }))
    )
    onRefresh()
  }

  async function handleCreateCombo() {
    const base = COMBO_BASE_NAMES[kind] || `${kind}-combo`
    let name = base
    let i = 1
    const existing = new Set(combos.map((c) => c.name))
    while (existing.has(name)) {
      name = `${base}-${i++}`
    }
    try {
      await api.createCombo({ name, models: [], kind })
      onRefresh()
    } catch (err: any) {
      alert(err.message || 'Failed to create combo')
    }
  }

  async function handleCreateCustomNode(data: {
    name: string
    prefix: string
    baseUrl: string
    type: string
    urlSuffix?: string
  }) {
    const node = await api.createProviderNode({ ...data, urlSuffix: data.urlSuffix || undefined })
    customNodes = [...customNodes, node]
    showCustomModal = false
    onRefresh()
  }
</script>

{#if selectedProvider}
  <MediaProviderDetail
    provider={selectedProvider}
    {kind}
    {connections}
    {apiKeys}
    {settings}
    onBack={() => (selectedProvider = null)}
    {onRefresh}
  />
{:else}
  <div class="flex flex-col gap-6 animate-fade-in">
    {#if isEmbedding || supportsCombo}
      <div class="flex items-center justify-end gap-2">
        {#if supportsCombo}
          <button
            type="button"
            onclick={handleCreateCombo}
            class="inline-flex items-center gap-1.5 h-8 px-3 rounded-lg bg-surface border border-border hover:bg-surface-2 text-text-main text-xs font-medium transition-colors cursor-pointer shadow-sm"
          >
            <span class="material-symbols-outlined text-sm">add</span>
            Create Combo
          </button>
        {/if}
        {#if isEmbedding}
          <button
            type="button"
            onclick={() => (showCustomModal = true)}
            class="inline-flex items-center gap-1.5 h-8 px-3 rounded-lg bg-primary hover:bg-primary-hover text-white text-xs font-semibold transition-colors cursor-pointer shadow-sm"
          >
            <span class="material-symbols-outlined text-sm">add</span>
            Add Custom Embedding
          </button>
        {/if}
      </div>
    {/if}

    {#if supportsCombo && kindCombos.length > 0}
      <div class="flex flex-col gap-2">
        <h2 class="text-xs font-semibold text-text-muted uppercase tracking-wider">Combos</h2>
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
          {#each kindCombos as combo (combo.id)}
            <a href={`/dashboard/media-providers/combo/${combo.id}`}>
              <Card padding="xs" class="h-full hover:bg-black/[0.01] dark:hover:bg-white/[0.01] transition-colors cursor-pointer">
                <div class="flex items-center gap-3">
                  <div class="size-8 rounded-lg flex items-center justify-center shrink-0 bg-primary/10 text-primary">
                    <span class="material-symbols-outlined text-lg">alt_route</span>
                  </div>
                  <div class="min-w-0 flex-1">
                    <h3 class="font-semibold text-sm truncate">{combo.name}</h3>
                    <div class="flex items-center gap-2 mt-0.5">
                      <Badge variant="default" size="sm">{combo.models?.length ?? 0} models</Badge>
                    </div>
                  </div>
                </div>
              </Card>
            </a>
          {/each}
        </div>
      </div>
    {/if}

    {#if allProviders.length === 0}
      <div class="text-center py-12 border border-dashed border-border rounded-xl text-text-muted text-sm">
        No providers support <strong>{kindConfig?.title || kind}</strong> yet.
      </div>
    {:else}
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
        {#each providers as provider (provider.id)}
          <MediaProviderCard
            {provider}
            {kind}
            {connections}
            onToggle={handleToggleProvider}
            onSelect={() => {
              if (onSelectProvider) {
                onSelectProvider(kind, provider.id)
              } else {
                selectedProvider = provider
              }
            }}
          />
        {/each}
        {#each customProviders as provider (provider.id)}
          <MediaProviderCard
            {provider}
            {kind}
            {connections}
            isCustom
            onToggle={handleToggleProvider}
            onSelect={() => {
              if (onSelectProvider) {
                onSelectProvider(kind, provider.id)
              } else {
                selectedProvider = provider
              }
            }}
          />
        {/each}
      </div>
    {/if}

    {#if isEmbedding && showCustomModal}
      <AddCompatibleNodeModal
        isOpen={showCustomModal}
        type="custom-embedding"
        onClose={() => (showCustomModal = false)}
        onSubmit={handleCreateCustomNode}
      />
    {/if}
  </div>
{/if}
