<script lang="ts">
  import { Layers, Search, X } from 'lucide-svelte'
  import { api, type Combo, type ProviderConnection, type ProviderNode, type SmartRule } from '../../api/client'
  import {
    clearJudgeModel,
    comboStrategyOptions,
    getComboModels,
    INHERIT_STRATEGY,
    parseCapacityAdapterSettings,
    resolveComboStrategy,
    updateComboStrategy,
    updateJudgeModel,
    updateRouterModel,
    updateSmartPolicy,
    type CapacityAdapterState,
    type ComboStrategyInfo
  } from './types'
  import SmartPolicyModal from './SmartPolicyModal.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import ComboCard from './ComboCard.svelte'
  import CombosHeader from './CombosHeader.svelte'
  import CreateComboModal from './CreateComboModal.svelte'
  import ModelPickerModal from './ModelPickerModal.svelte'
  import CapacityAdapterSection from './CapacityAdapterSection.svelte'
  import ConfirmModal from '../../lib/ui/ConfirmModal.svelte'
  import { copyToClipboard } from '../../lib/clipboard'
  interface Props {
    combos?: Combo[]
    connections?: ProviderConnection[]
    providerNodes?: ProviderNode[]
    onRefresh: () => void
    isCreatingOpen?: boolean
  }

  let {
    combos = [],
    connections = [],
    providerNodes = [],
    onRefresh,
    isCreatingOpen = $bindable(false),
  }: Props = $props()

  // Upstream parity (combos page.js fetchData): webSearch/webFetch combos
  // (notably search-combo) live under media-providers/web, not here. The
  // auto-generated kinds are llm combos too and stay visible here.
  function isLlmCombo(c: Combo): boolean {
    if (c.kind && c.kind !== 'llm' && !c.kind.startsWith('auto-')) return false
    if (c.name === 'search-combo' || c.name.startsWith('search-combo-')) return false
    return true
  }
  let llmCombos = $derived(combos.filter(isLlmCombo))

  let comboStrategies = $state<Record<string, ComboStrategyInfo>>({})
  let capacityAdapter = $state<CapacityAdapterState>({
    vision: { enabled: true, roundRobin: false, models: ['ag/gemini-3.8-flash-high'] },
    audioInput: { enabled: true, roundRobin: false, models: [] },
  })
  // Upstream #4576: the inherit option has to name what it inherits, and an
  // inheriting combo still routes with the global mode, so the value has to be
  // here even though nothing else on this page reads it.
  let globalComboStrategy = $state('first-model')
  let copiedId = $state<string | null>(null)

  // Edit / Create Modal state
  let editingCombo = $state<Combo | null>(null)
  let modalModels = $state<string[]>([])
  let isSavingCombo = $state(false)
  let comboSaveError = $state('')
  let modalNameResetKey = $state(0)

  // Model Picker Modal state
  let showModelPicker = $state(false)
  let modelPickerTarget = $state<'combo' | 'vision' | 'audio' | 'judge' | 'router'>('combo')
  let policyCombo = $state<Combo | null>(null)

  // Confirm Delete Modal state (upstream confirmState parity). Carries the ids
  // to remove.
  let confirmState = $state<{ name: string; ids: string[] } | null>(null)

  // Selection drives the bulk bar below the list, matching upstream: a checkbox
  // per card plus a "Select all" that names the count, and bulk strategy /
  // delete / clear actions that only exist while something is selected. The
  // Set is re-created on every change so Svelte 5 sees it (mutating in place is
  // not tracked).
  let selectedIds = $state<Set<string>>(new Set())

  // Upstream 9b4ea9eb: the list takes a search box filtering on the combo name
  // and its member model ids, case-insensitively, so both "deepseek" and
  // "deepseek-v4.1-flash" find the right rows.
  let searchQuery = $state('')
  let normalizedQuery = $derived(searchQuery.trim().toLowerCase())
  let filteredCombos = $derived(
    normalizedQuery
      ? llmCombos.filter(
          (c) =>
            (c.name || '').toLowerCase().includes(normalizedQuery) ||
            getComboModels(c).some((m) => m.toLowerCase().includes(normalizedQuery))
        )
      : llmCombos
  )

  function toggleSelect(combo: Combo) {
    const next = new Set(selectedIds)
    if (next.has(combo.id)) next.delete(combo.id)
    else next.add(combo.id)
    selectedIds = next
  }

  let selectedCombos = $derived(llmCombos.filter((c) => selectedIds.has(c.id)))
  // Select-all follows the search: with a query active it must only reach the
  // rows on screen, otherwise a bulk Delete would take out hidden combos.
  let allSelected = $derived(
    filteredCombos.length > 0 && filteredCombos.every((c) => selectedIds.has(c.id))
  )

  function toggleSelectAll() {
    selectedIds = allSelected ? new Set() : new Set(filteredCombos.map((c) => c.id))
  }

  function clearSelection() {
    selectedIds = new Set()
  }

  async function loadSettings() {
    try {
      const s = await api.getSettings()
      if (s?.comboStrategies && typeof s.comboStrategies === 'object') {
        comboStrategies = s.comboStrategies as Record<string, ComboStrategyInfo>
      }
      if (typeof s?.comboStrategy === 'string') {
        globalComboStrategy = s.comboStrategy
      }
      if (s?.capacityAdapter && typeof s.capacityAdapter === 'object') {
        capacityAdapter = parseCapacityAdapterSettings(s.capacityAdapter as Record<string, unknown>)
      }
    } catch (e) {
      console.error('Failed to load settings:', e)
    }
  }

  $effect(() => { loadSettings() })
  // Reset the create form once per open. Unguarded, this looped forever: the
  // effect bumps `modalNameResetKey`, which the `{#key}` block below reads, that
  // block re-creates CreateComboModal, and that re-queues the effect. Svelte
  // aborted the flush with effect_update_depth_exceeded after 1000 rounds, so
  // "Add Model" never opened the picker at all.
  let createSessionOpen = $state(false)
  $effect(() => {
    if (!isCreatingOpen) {
      createSessionOpen = false
      return
    }
    if (editingCombo || createSessionOpen) return
    createSessionOpen = true
    modalModels = []
    modalNameResetKey += 1
  })

  function copyName(name: string, id: string) {
    copyToClipboard(name)
    copiedId = id
    setTimeout(() => {
      if (copiedId === id) copiedId = null
    }, 2000)
  }

  async function handleSetStrategy(combo: Combo, newStrategy: string) {
    const updated = updateComboStrategy(comboStrategies, combo.name, newStrategy)
    comboStrategies = updated
    try {
      await api.patchSettings({ comboStrategies: updated })
      // `inherit` is a dashboard-only value: the combo row has no override to
      // write, and the server would read the literal string as a strategy the
      // router does not implement.
      if (newStrategy !== INHERIT_STRATEGY) {
        await api.updateCombo(combo.id, { strategy: newStrategy })
      }
      onRefresh()
    } catch (e) {
      console.error('Failed to update combo strategy:', e)
    }
  }
  // Respect stored order lives beside the strategy dropdown because it
  // modifies how that strategy behaves: the rotation is skipped and the
  // stored list is walked as-is. The capability reorder still runs, so a
  // request that needs vision floats a capable model to the front.
  async function handleToggleStoredOrder(combo: Combo, respect: boolean) {
    const updated = { ...comboStrategies }
    updated[combo.name] = { ...(updated[combo.name] || {}), respectStoredOrder: respect }
    comboStrategies = updated
    try {
      await api.patchSettings({ comboStrategies: updated })
      onRefresh()
    } catch (e) {
      console.error('Failed to update stored-order flag:', e)
    }
  }

  async function handleSetJudge(comboName: string, judgeModel: string) {
    const updated = updateJudgeModel(comboStrategies, comboName, judgeModel)
    comboStrategies = updated
    try {
      await api.patchSettings({ comboStrategies: updated })
    } catch (e) {
      console.error('Failed to update judge model:', e)
    }
  }

  async function clearJudge(comboName: string) {
    const updated = clearJudgeModel(comboStrategies, comboName)
    comboStrategies = updated
    try {
      await api.patchSettings({ comboStrategies: updated })
    } catch (e) {
      console.error('Failed to clear judge:', e)
    }
  }

  async function handleSetRouterModel(comboName: string, routerModel: string) {
    const updated = updateRouterModel(comboStrategies, comboName, routerModel)
    comboStrategies = updated
    try {
      await api.patchSettings({ comboStrategies: updated })
    } catch (e) {
      console.error('Failed to set smart router model:', e)
    }
  }

  async function handleSaveSmartPolicy(
    comboName: string,
    cfg: { smartPolicy: string; sessionSticky: boolean; confidenceThreshold: number }
  ) {
    const updated = updateSmartPolicy(comboStrategies, comboName, cfg)
    comboStrategies = updated
    try {
      await api.patchSettings({ comboStrategies: updated })
    } catch (e) {
      console.error('Failed to save smart policy:', e)
    }
  }

  async function saveCapacityAdapter(next: CapacityAdapterState) {
    capacityAdapter = next
    try {
      await api.patchSettings({ capacityAdapter: next })
    } catch (e) {
      console.error('Failed to update capacity adapter:', e)
    }
  }

  function openCreateModal() {
    editingCombo = null
    modalModels = []
    comboSaveError = ''
    isCreatingOpen = true
  }
  function openEditModal(combo: Combo) {
    editingCombo = combo
    modalModels = [...getComboModels(combo)]
    comboSaveError = ''
    isCreatingOpen = true
  }
  function closeModal() {
    isCreatingOpen = false
    editingCombo = null
    comboSaveError = ''
  }

  async function handleSaveCombo(name: string, models: string[], matchRules: SmartRule[]) {
    isSavingCombo = true
    comboSaveError = ''
    try {
      if (editingCombo) {
        await api.updateCombo(editingCombo.id, { name, models, matchRules })
      } else {
        await api.createCombo({ name, models, strategy: 'fallback', matchRules })
      }
      closeModal()
      onRefresh()
    } catch (e) {
      // A refused write (the name already addresses a combo, a model alias or
      // a custom model id) must be visible, or the guard looks inert.
      comboSaveError = e instanceof Error ? e.message : String(e)
      console.error('Failed to save combo:', e)
    } finally {
      isSavingCombo = false
    }
  }

  // Applying the rules on demand covers what no model write announces: a
  // catalogue sync that published new models, or a restore.
  let isReconciling = $state(false)
  let reconcileNote = $state('')
  async function handleReconcile() {
    isReconciling = true
    reconcileNote = ''
    try {
      const res = await api.reconcileCombos()
      const parts: string[] = [`${res.reconciled} combo${res.reconciled === 1 ? '' : 's'} updated`]
      if (res.added?.length) parts.push(`+${res.added.length} added`)
      if (res.removed?.length) parts.push(`-${res.removed.length} removed`)
      reconcileNote = parts.join(' · ')
      onRefresh()
    } catch (e) {
      reconcileNote = e instanceof Error ? e.message : String(e)
      console.error('Failed to reconcile combos:', e)
    } finally {
      isReconciling = false
    }
  }

  function openModelPicker(target: 'combo' | 'vision' | 'audio' | 'judge' | 'router') {
    modelPickerTarget = target
    showModelPicker = true
  }

  let addedModelValues = $derived.by(() => {
    if (modelPickerTarget === 'combo') return modalModels
    if (modelPickerTarget === 'vision') return capacityAdapter.vision.models
    if (modelPickerTarget === 'audio') return capacityAdapter.audioInput.models
    if (modelPickerTarget === 'judge') {
      const judge = editingCombo?.name ? comboStrategies[editingCombo.name]?.judgeModel : undefined
      return judge ? [judge] : []
    }
    if (modelPickerTarget === 'router') {
      const router = editingCombo?.name ? comboStrategies[editingCombo.name]?.routerModel : undefined
      return router ? [router] : []
    }
    return []
  })

  function handleSelectModel(val: string) {
    if (modelPickerTarget === 'combo') {
      if (!modalModels.includes(val)) modalModels = [...modalModels, val]
    } else if (modelPickerTarget === 'vision') {
      const cur = capacityAdapter.vision.models
      if (!cur.includes(val)) {
        saveCapacityAdapter({ ...capacityAdapter, vision: { ...capacityAdapter.vision, models: [...cur, val] } })
      }
    } else if (modelPickerTarget === 'audio') {
      const cur = capacityAdapter.audioInput.models
      if (!cur.includes(val)) {
        saveCapacityAdapter({ ...capacityAdapter, audioInput: { ...capacityAdapter.audioInput, models: [...cur, val] } })
      }
    } else if (modelPickerTarget === 'judge' && editingCombo) {
      handleSetJudge(editingCombo.name, val)
      showModelPicker = false
    } else if (modelPickerTarget === 'router' && editingCombo) {
      handleSetRouterModel(editingCombo.name, val)
      showModelPicker = false
    }
  }

  function handleDeselectModel(val: string) {
    if (modelPickerTarget === 'combo') {
      modalModels = modalModels.filter((m) => m !== val)
    } else if (modelPickerTarget === 'vision') {
      const cur = capacityAdapter.vision.models
      saveCapacityAdapter({ ...capacityAdapter, vision: { ...capacityAdapter.vision, models: cur.filter((m) => m !== val) } })
    } else if (modelPickerTarget === 'audio') {
      const cur = capacityAdapter.audioInput.models
      saveCapacityAdapter({ ...capacityAdapter, audioInput: { ...capacityAdapter.audioInput, models: cur.filter((m) => m !== val) } })
    } else if (modelPickerTarget === 'judge' && editingCombo) {
      clearJudge(editingCombo.name)
      showModelPicker = false
    } else if (modelPickerTarget === 'router' && editingCombo) {
      handleSetRouterModel(editingCombo.name, '')
      showModelPicker = false
    }
  }
  async function handleDeleteCombos() {
    if (!confirmState) return
    const ids = confirmState.ids
    let deleted = 0
    for (const id of ids) {
      try {
        await api.deleteCombo(id)
        deleted++
      } catch (e) {
        console.error(`Failed to delete combo ${id}:`, e)
      }
    }
    confirmState = null
    clearSelection()
    onRefresh()
    if (deleted < ids.length) {
      alert(`Deleted ${deleted} of ${ids.length} combo(s). The rest could not be removed.`)
    }
  }

  function handleDeleteSelected() {
    if (selectedCombos.length === 0) return
    confirmState = {
      name: selectedCombos.map((c) => c.name).join(', '),
      ids: selectedCombos.map((c) => c.id),
    }
  }

  let bulkStrategy = $state('')

  // Upstream writes the strategy through the same settings patch a single card
  // uses, then one updateCombo per row: comboStrategies is keyed by combo NAME,
  // so it has to be rewritten as a whole or the last write wins.
  async function handleApplyBulkStrategy() {
    if (!bulkStrategy || selectedCombos.length === 0) return
    const targets = selectedCombos
    let next = comboStrategies
    for (const combo of targets) {
      next = updateComboStrategy(next, combo.name, bulkStrategy)
    }
    comboStrategies = next
    try {
      await api.patchSettings({ comboStrategies: next })
      // `inherit` is dashboard-only: it removes the override, so there is no
      // strategy to write on the combo row. Sending the literal string would
      // hand the router a mode it does not implement.
      if (bulkStrategy !== INHERIT_STRATEGY) {
        for (const combo of targets) {
          await api.updateCombo(combo.id, { strategy: bulkStrategy })
        }
      }
      bulkStrategy = ''
      onRefresh()
    } catch (e) {
      alert(
        'Failed to set strategy: ' + (e instanceof Error ? e.message : String(e))
      )
    }
  }

</script>

<div class="flex min-w-0 flex-col gap-6 px-1 sm:px-0">
  <CombosHeader onCreateClick={openCreateModal} />
  <div class="flex flex-wrap items-center justify-end gap-2">
    <div class="relative w-full sm:w-64">
      <Search class="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-text-muted" />
      <input
        type="text"
        bind:value={searchQuery}
        placeholder="Search combos…"
        aria-label="Search combos"
        class="w-full rounded-[10px] border border-transparent bg-surface-2 py-2 pl-9 pr-9 text-sm text-text-main transition-all duration-150 ease-out placeholder-text-muted/70 focus:border-brand-500/40 focus:outline-none focus:ring-2 focus:ring-brand-500/30"
      />
      {#if searchQuery}
        <button
          type="button"
          onclick={() => (searchQuery = '')}
          aria-label="Clear search"
          class="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-text-muted transition-colors hover:text-text-main"
        >
          <X class="h-4 w-4" />
        </button>
      {/if}
    </div>
    <Button onclick={handleReconcile} variant="ghost" size="sm" loading={isReconciling}>
      Reconcile rules
    </Button>
    {#if reconcileNote}
      <span class="text-[11px] text-text-muted">{reconcileNote}</span>
    {/if}
  </div>

  <!-- Upstream selection bar: the count is on the checkbox, and the bulk
       actions only exist while something is selected, so the destructive
       Delete is never sitting next to Create. -->
  {#if llmCombos.length > 0}
    <div
      class="flex min-w-0 flex-col gap-2 rounded-lg border border-black/5 bg-black/[0.015] px-3 py-2 dark:border-white/5 dark:bg-white/[0.02] sm:flex-row sm:items-center sm:justify-between"
    >
      <label class="flex cursor-pointer items-center gap-2 text-xs text-text-muted hover:text-primary select-none">
        <input
          type="checkbox"
          checked={allSelected}
          onchange={toggleSelectAll}
          class="h-3.5 w-3.5 rounded border-gray-300 text-primary focus:ring-primary"
        />
        <span>
          {selectedCombos.length > 0 ? `${selectedCombos.length} selected` : `Select all (${filteredCombos.length})`}
        </span>
      </label>

      {#if selectedCombos.length > 0 || normalizedQuery}
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          {#if normalizedQuery}
            <span class="text-xs text-text-muted">
              {filteredCombos.length} of {llmCombos.length} shown
            </span>
          {/if}
          {#if selectedCombos.length > 0}
            <div class="w-full min-w-[160px] sm:w-[200px]">
              <select
                bind:value={bulkStrategy}
                class="w-full bg-surface-2 border border-border rounded-lg px-2.5 py-1.5 text-xs text-text-main focus:outline-none focus:ring-brand-500 focus:border-brand-500 cursor-pointer"
              >
                <option value="" disabled>Set strategy…</option>
                {#each comboStrategyOptions(globalComboStrategy) as strategy (strategy.value)}
                  <option value={strategy.value}>{strategy.label}</option>
                {/each}
              </select>
            </div>
            <Button
              size="sm"
              variant="outline"
              onclick={handleApplyBulkStrategy}
              disabled={!bulkStrategy}
              class="whitespace-nowrap"
            >
              Apply Strategy
            </Button>
            <Button
              icon="delete"
              size="sm"
              variant="danger"
              onclick={handleDeleteSelected}
              class="whitespace-nowrap"
            >
              Delete ({selectedCombos.length})
            </Button>
            <Button size="sm" variant="ghost" onclick={clearSelection} class="whitespace-nowrap">
              Clear
            </Button>
          {/if}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Combos List -->
  {#if llmCombos.length === 0}
    <Card>
      <div class="text-center py-12">
        <div class="inline-flex items-center justify-center w-16 h-16 rounded-full bg-brand-500/10 text-brand-500 mb-4">
          <Layers class="w-8 h-8" />
        </div>
        <p class="text-text-main font-medium mb-1">No combos yet</p>
        <p class="text-sm text-text-muted mb-4">Create model combos with fallback support</p>
        <Button icon="add" onclick={openCreateModal} class="w-full sm:w-auto">
          Create Combo
        </Button>
      </div>
    </Card>
  {:else}
    {#if filteredCombos.length === 0}
      <Card>
        <div class="text-center py-10">
          <p class="text-text-main font-medium mb-1">No combos match “{searchQuery.trim()}”</p>
          <p class="text-sm text-text-muted mb-4">
            Try a shorter query, or clear the search to see all {llmCombos.length} combos.
          </p>
          <Button variant="secondary" onclick={() => (searchQuery = '')}>Clear search</Button>
        </div>
      </Card>
    {:else}
      <div class="flex flex-col gap-4">
        {#each filteredCombos as combo (combo.id)}
          <ComboCard
            {combo}
            strategyInfo={comboStrategies[combo.name]}
            globalStrategy={globalComboStrategy}
            {copiedId}
            onSetStrategy={handleSetStrategy}
            onToggleStoredOrder={handleToggleStoredOrder}
            onOpenJudgePicker={(c) => { editingCombo = c; openModelPicker('judge') }}
            onClearJudge={clearJudge}
            onOpenRouterPicker={(c) => { editingCombo = c; openModelPicker('router') }}
            onOpenPolicyModal={(c) => (policyCombo = c)}
            onCopy={copyName}
            onEdit={openEditModal}
            onDelete={(c) => (confirmState = { name: c.name, ids: [c.id] })}
            isSelected={selectedIds.has(combo.id)}
            onToggleSelect={toggleSelect}
          />
        {/each}
      </div>
    {/if}
  {/if}

  <!-- Vision / Audio Adapter Section -->
  <CapacityAdapterSection
    {capacityAdapter}
    onSaveAdapter={saveCapacityAdapter}
    onOpenModelPicker={openModelPicker}
  />
</div>

<!-- Create / Edit Combo Modal (key forces remount = upstream remount reset) -->
{#key editingCombo?.id || modalNameResetKey}
  <CreateComboModal
    isOpen={isCreatingOpen}
    {editingCombo}
    models={modalModels}
    isSaving={isSavingCombo}
    saveError={comboSaveError}
    onClose={closeModal}
    onSave={handleSaveCombo}
    onOpenModelPicker={() => openModelPicker('combo')}
    onUpdateModels={(newModels) => (modalModels = newModels)}
  />
{/key}

<!-- Model Picker Modal -->
<ModelPickerModal
  isOpen={showModelPicker}
  target={modelPickerTarget}
  {connections}
  combos={llmCombos}
  {providerNodes}
  currentComboName={editingCombo?.name}
  {addedModelValues}
  onSelect={handleSelectModel}
  onDeselect={handleDeselectModel}
  onClose={() => (showModelPicker = false)}
/>

<!-- Smart Router policy editor (upstream SmartPolicyModal parity) -->
<SmartPolicyModal
  isOpen={!!policyCombo}
  onClose={() => (policyCombo = null)}
  comboName={policyCombo?.name || ''}
  currentPolicy={policyCombo ? comboStrategies[policyCombo.name]?.smartPolicy : undefined}
  currentThreshold={policyCombo ? comboStrategies[policyCombo.name]?.confidenceThreshold : undefined}
  onSave={(cfg) => policyCombo && handleSaveSmartPolicy(policyCombo.name, cfg)}
/>

<!-- Confirm Delete Modal (upstream ConfirmModal parity) -->
<ConfirmModal
  isOpen={!!confirmState}
  title="Delete Combo"
  message={
    confirmState
      ? confirmState.ids.length === 1
        ? `Delete combo "${confirmState.name}"?`
        : `Delete ${confirmState.ids.length} combo(s)? (${confirmState.name})`
      : 'Delete this combo?'
  }
  onClose={() => (confirmState = null)}
  onConfirm={handleDeleteCombos}
/>
