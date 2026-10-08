<script lang="ts">
  import { ChevronRight, Globe, Layers, Plus, Search, Trash2, X } from 'lucide-svelte'
  import { api, type Combo, type ProviderConnection } from '../../api/client'
  import { getProvidersByKind, type ProviderCatalogItem } from '../../lib/providers'
  import Badge from '../../lib/ui/Badge.svelte'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import Button from '../../lib/ui/Button.svelte'
  interface Props {
    connections?: ProviderConnection[]
    combos?: Combo[]
    onRefresh: () => void
    onSelectProvider: (kind: 'webSearch' | 'webFetch', providerId: string) => void
  }

  let { connections = [], combos = [], onRefresh, onSelectProvider }: Props = $props()

  let searchProviders = $derived(getProvidersByKind('webSearch'))
  let fetchProviders = $derived(getProvidersByKind('webFetch'))
  let searchCombos = $derived(combos.filter((c) => c.kind === 'webSearch'))
  let fetchCombos = $derived(combos.filter((c) => c.kind === 'webFetch'))
  let creatingKind = $state<'webSearch' | 'webFetch' | null>(null)
  let newComboName = $state('')
  let managingCombo = $state<Combo | null>(null)
  let manageName = $state('')
  let manageModels = $state<string[]>([])
  let isSubmitting = $state(false)

  function parseModels(combo: Combo): string[] {
    if (Array.isArray(combo.models)) return combo.models
    try {
      const p = JSON.parse(combo.models || '[]')
      return Array.isArray(p) ? p : [combo.models]
    } catch {
      return combo.models ? [combo.models] : []
    }
  }

  function getStats(providerId: string, noAuth = false) {
    const list = connections.filter((c) => c.provider === providerId)
    const connected = list.filter((c) => (c.testStatus === 'active' || c.testStatus === 'success' || !c.testStatus) && c.isActive === 1 && !c.lastError).length
    const error = list.filter((c) => c.testStatus === 'error' || !!c.lastError).length
    return { total: list.length, connected, error, allDisabled: list.length > 0 && list.every((c) => c.isActive === 0), noAuth }
  }

  function openCreateDialog(kind: 'webSearch' | 'webFetch') {
    creatingKind = kind
    const base = kind === 'webSearch' ? 'search-combo' : 'fetch-combo'
    let name = base, i = 1
    const existing = new Set(combos.map((c) => c.name))
    while (existing.has(name)) name = `${base}-${i++}`
    newComboName = name
  }

  async function handleCreateCombo() {
    if (!creatingKind || !newComboName.trim()) return
    isSubmitting = true
    try {
      await api.createCombo({ name: newComboName.trim(), kind: creatingKind, models: [] })
      creatingKind = null
      onRefresh()
    } catch (err) {
      alert(`Failed to create combo: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSubmitting = false
    }
  }

  function openManageDialog(combo: Combo) {
    managingCombo = combo
    manageName = combo.name
    manageModels = parseModels(combo)
  }
  function addModelToManage(id: string) {
    if (!manageModels.includes(id)) manageModels = [...manageModels, id]
  }
  function removeModelFromManage(idx: number) {
    manageModels = manageModels.filter((_, i) => i !== idx)
  }

  async function handleSaveCombo() {
    if (!managingCombo || !manageName.trim()) return
    isSubmitting = true
    try {
      await api.updateCombo(managingCombo.id, { name: manageName.trim(), models: manageModels, kind: managingCombo.kind })
      managingCombo = null
      onRefresh()
    } catch (err) {
      alert(`Failed to update combo: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSubmitting = false
    }
  }

  async function handleDeleteCombo() {
    if (!managingCombo || !confirm(`Delete combo "${managingCombo.name}"?`)) return
    isSubmitting = true
    try {
      await api.deleteCombo(managingCombo.id)
      managingCombo = null
      onRefresh()
    } catch (err) {
      alert(`Failed to delete combo: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSubmitting = false
    }
  }
</script>

{#snippet sectionBlock(title: string, Icon: any, kind: 'webSearch' | 'webFetch', providers: ProviderCatalogItem[], sectionCombos: Combo[])}
  <div class="space-y-4">
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
      <div class="flex items-center gap-2.5">
        <div class="size-8 rounded-lg bg-brand-500/10 flex items-center justify-center text-brand-500">
          <Icon class="w-4 h-4" />
        </div>
        <div class="flex items-center gap-2">
          <h2 class="text-base font-semibold text-text-main">{title}</h2>
          <span class="text-xs text-text-muted">({providers.length} providers · {sectionCombos.length} combos)</span>
        </div>
      </div>
      <Button size="sm" onclick={() => openCreateDialog(kind)}>
        <Plus class="w-3.5 h-3.5 mr-1" /> Create Combo
      </Button>
    </div>

    {#if sectionCombos.length > 0}
      <div class="flex flex-col gap-2">
        {#each sectionCombos as combo (combo.id)}
          {@const models = parseModels(combo)}
          <div
            role="button"
            tabindex="0"
            onclick={() => openManageDialog(combo)}
            onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && openManageDialog(combo)}
            class="group cursor-pointer text-left focus:outline-none"
          >
            <div class="p-2.5 rounded-xl border border-border bg-surface hover:border-brand-500/40 hover:bg-black/[0.01] dark:hover:bg-white/[0.01] transition-all flex min-w-0 items-center gap-3">
              <div class="size-6 rounded-md bg-brand-500/10 flex items-center justify-center shrink-0">
                <Layers class="w-3.5 h-3.5 text-brand-500" />
              </div>
              <code class="text-sm font-mono font-medium flex-1 truncate text-text-main group-hover:text-brand-500 transition-colors">
                {combo.name}
              </code>
              <div class="flex flex-wrap items-center gap-1 sm:shrink-0">
                {#each models.slice(0, 6) as model}
                  {@const pid = model.includes('/') ? model.split('/')[0] : model}
                  <div class="size-5 rounded flex items-center justify-center bg-black/5 dark:bg-white/5 border border-border/40 overflow-hidden" title={pid}>
                    <ProviderArtwork id={pid} alt={pid} class="size-3.5 object-contain text-[8px] leading-none font-semibold" />
                  </div>
                {/each}
                {#if models.length > 6}
                  <span class="text-[10px] text-text-muted ml-1">+{models.length - 6}</span>
                {/if}
              </div>
              <span class="text-[11px] text-text-muted shrink-0 font-medium">{models.length} {models.length === 1 ? 'model' : 'models'}</span>
              <ChevronRight class="w-4 h-4 text-text-muted group-hover:text-text-main transition-colors shrink-0" />
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <p class="text-xs text-text-muted italic py-1">No combos yet.</p>
    {/if}

    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4 pt-1">
      {#each providers as p (p.id)}
        {@const stats = getStats(p.id, p.noAuth)}
        <a
          href="/dashboard/media-providers/{kind}/{p.id}"
          onclick={(e) => {
            e.preventDefault()
            onSelectProvider(kind, p.id)
          }}
          class="group block text-left focus:outline-none"
        >
          <div class="p-3 rounded-xl border border-border bg-surface hover:border-brand-500/40 hover:bg-black/[0.01] dark:hover:bg-white/[0.01] transition-all {stats.allDisabled ? 'opacity-50' : ''}">
            <div class="flex min-w-0 items-center gap-3">
              <div class="size-8 rounded-lg flex items-center justify-center shrink-0 border border-border/40 overflow-hidden" style="background-color: {p.color && p.color.length > 7 ? p.color : (p.color ?? '#888') + '15'}">
                <ProviderArtwork id={p.id} alt={p.name} class="size-5 object-contain rounded text-[10px] leading-none font-semibold" />
              </div>
              <div class="min-w-0 flex-1">
                <h3 class="font-semibold text-sm text-text-main group-hover:text-brand-500 transition-colors truncate">{p.name}</h3>
                <div class="flex items-center gap-1.5 mt-0.5 flex-wrap">
                  {#if p.noAuth}
                    <Badge variant="success" size="sm">Ready</Badge>
                  {:else if stats.allDisabled}
                    <Badge variant="default" size="sm">Disabled</Badge>
                  {:else if stats.total === 0}
                    <span class="text-xs text-text-muted">No connections</span>
                  {:else}
                    {#if stats.connected > 0}
                      <Badge variant="success" size="sm" dot>{stats.connected} Connected</Badge>
                    {/if}
                    {#if stats.error > 0}
                      <Badge variant="error" size="sm" dot>{stats.error} Error</Badge>
                    {/if}
                    {#if stats.connected === 0 && stats.error === 0}
                      <Badge variant="default" size="sm">{stats.total} Added</Badge>
                    {/if}
                  {/if}
                </div>
              </div>
            </div>
          </div>
        </a>
      {/each}
    </div>
  </div>
{/snippet}

<div class="flex flex-col gap-8 animate-fade-in">
  {@render sectionBlock('Web Search', Search, 'webSearch', searchProviders, searchCombos)}
  <div class="border-t border-border"></div>
  {@render sectionBlock('Web Fetch', Globe, 'webFetch', fetchProviders, fetchCombos)}
</div>

{#if creatingKind}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
    <div class="w-full max-w-md rounded-2xl border border-border bg-surface p-6 shadow-xl space-y-4">
      <div class="flex items-center justify-between">
        <h3 class="text-base font-semibold text-text-main">Create {creatingKind === 'webSearch' ? 'Web Search' : 'Web Fetch'} Combo</h3>
        <button onclick={() => (creatingKind = null)} class="text-text-muted hover:text-text-main cursor-pointer"><X class="w-5 h-5" /></button>
      </div>
      <div>
        <label for="new-combo-name" class="block text-xs font-medium text-text-muted mb-1">Combo Name</label>
        <input id="new-combo-name" type="text" bind:value={newComboName} class="w-full px-3 py-2 text-sm rounded-lg border border-border bg-bg text-text-main focus:outline-none focus:border-brand-500" placeholder="e.g. search-combo-1" />
      </div>
      <div class="flex justify-end gap-2 pt-2">
        <Button variant="secondary" size="sm" onclick={() => (creatingKind = null)}>Cancel</Button>
        <Button size="sm" loading={isSubmitting} onclick={handleCreateCombo}>Create</Button>
      </div>
    </div>
  </div>
{/if}

{#if managingCombo}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
    <div class="w-full max-w-lg rounded-2xl border border-border bg-surface p-6 shadow-xl space-y-4 max-h-[90vh] overflow-y-auto">
      <div class="flex items-center justify-between">
        <h3 class="text-base font-semibold text-text-main">Manage Combo</h3>
        <button onclick={() => (managingCombo = null)} class="text-text-muted hover:text-text-main cursor-pointer"><X class="w-5 h-5" /></button>
      </div>
      <div>
        <label for="edit-combo-name" class="block text-xs font-medium text-text-muted mb-1">Combo Name</label>
        <input id="edit-combo-name" type="text" bind:value={manageName} class="w-full px-3 py-2 text-sm rounded-lg border border-border bg-bg text-text-main focus:outline-none focus:border-brand-500" />
      </div>
      <div>
        <span class="block text-xs font-medium text-text-muted mb-2">Provider Models in Combo</span>
        {#if manageModels.length === 0}
          <p class="text-xs text-text-muted italic mb-2">No provider models in this combo yet.</p>
        {:else}
          <div class="space-y-1.5 mb-3">
            {#each manageModels as model, idx}
              <div class="flex items-center justify-between px-3 py-1.5 rounded-lg border border-border bg-bg text-xs">
                <span class="font-mono">{model}</span>
                <button type="button" onclick={() => removeModelFromManage(idx)} class="text-red-500 hover:text-red-600 cursor-pointer" title="Remove"><X class="w-4 h-4" /></button>
              </div>
            {/each}
          </div>
        {/if}
        <div class="flex flex-wrap gap-2">
          {#each (managingCombo.kind === 'webFetch' ? fetchProviders : searchProviders) as p}
            <button type="button" onclick={() => addModelToManage(p.id)} class="px-2.5 py-1 text-xs rounded-lg border border-border bg-surface-2 hover:bg-surface-3 text-text-main flex items-center gap-1.5 cursor-pointer">
              <Plus class="w-3.5 h-3.5" /> {p.name}
            </button>
          {/each}
        </div>
      </div>
      <div class="flex items-center justify-between pt-4 border-t border-border">
        <Button variant="danger" size="sm" onclick={handleDeleteCombo}><Trash2 class="w-4 h-4 mr-1" /> Delete</Button>
        <div class="flex gap-2">
          <Button variant="secondary" size="sm" onclick={() => (managingCombo = null)}>Cancel</Button>
          <Button size="sm" loading={isSubmitting} onclick={handleSaveCombo}>Save Changes</Button>
        </div>
      </div>
    </div>
  </div>
{/if}
