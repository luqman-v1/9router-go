<script lang="ts">
  import { Layers } from 'lucide-svelte'
  import { api, type Combo, type ProviderConnection, type ProviderNode } from '../../api/client'
  import {
    clearJudgeModel,
    getComboModels,
    parseCapacityAdapterSettings,
    updateComboStrategy,
    updateJudgeModel,
    type CapacityAdapterState,
    type ComboStrategyInfo
  } from './types'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import ComboCard from './ComboCard.svelte'
  import CombosHeader from './CombosHeader.svelte'
  import CreateComboModal from './CreateComboModal.svelte'
  import ModelPickerModal from './ModelPickerModal.svelte'
  import CapacityAdapterSection from './CapacityAdapterSection.svelte'
  import ConfirmModal from '../../lib/ui/ConfirmModal.svelte'
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
  let copiedId = $state<string | null>(null)

  // Edit / Create Modal state
  let editingCombo = $state<Combo | null>(null)
  let modalModels = $state<string[]>([])
  let isSavingCombo = $state(false)
  let comboSaveError = $state('')
  let modalNameResetKey = $state(0)

  // Model Picker Modal state
  let showModelPicker = $state(false)
  let modelPickerTarget = $state<'combo' | 'vision' | 'audio' | 'judge'>('combo')

  // Confirm Delete Modal state (upstream confirmState parity)
  let confirmState = $state<{ name: string; id: string } | null>(null)

  async function loadSettings() {
    try {
      const s = await api.getSettings()
      if (s?.comboStrategies && typeof s.comboStrategies === 'object') {
        comboStrategies = s.comboStrategies as Record<string, ComboStrategyInfo>
      }
      if (s?.capacityAdapter && typeof s.capacityAdapter === 'object') {
        capacityAdapter = parseCapacityAdapterSettings(s.capacityAdapter as Record<string, unknown>)
      }
    } catch (e) {
      console.error('Failed to load settings:', e)
    }
  }

  $effect(() => { loadSettings() })
  $effect(() => {
    if (isCreatingOpen && !editingCombo) {
      modalModels = []
      modalNameResetKey += 1
    }
  })

  function copyName(name: string, id: string) {
    navigator.clipboard.writeText(name)
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
      await api.updateCombo(combo.id, { strategy: newStrategy })
      onRefresh()
    } catch (e) {
      console.error('Failed to update combo strategy:', e)
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

  async function handleSaveCombo(name: string, models: string[]) {
    isSavingCombo = true
    comboSaveError = ''
    try {
      if (editingCombo) {
        await api.updateCombo(editingCombo.id, { name, models })
      } else {
        await api.createCombo({ name, models, strategy: 'fallback' })
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

  function openModelPicker(target: 'combo' | 'vision' | 'audio' | 'judge') {
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
    }
  }
  async function handleDeleteCombo() {
    if (!confirmState) return
    const id = confirmState.id
    try {
      await api.deleteCombo(id)
      confirmState = null
      onRefresh()
    } catch (e) {
      console.error('Failed to delete combo:', e)
    }
  }

  let isBuildingAutoFree = $state(false)

  async function handleBuildAutoFree() {
    isBuildingAutoFree = true
    try {
      const res = await api.buildAutoFreeCombo()
      if (res.models.length === 0) {
        alert('No free-tier models found among providers you have connections for.')
      }
      onRefresh()
    } catch (e) {
      alert(
        'Failed to build auto free-tier combo: ' +
          (e instanceof Error ? e.message : String(e))
      )
    } finally {
      isBuildingAutoFree = false
    }
  }

  async function handleReorderCombo(combo: Combo, models: string[]) {
    try {
      await api.updateCombo(combo.id, {
        name: combo.name,
        kind: combo.kind ?? undefined,
        models,
      })
      onRefresh()
    } catch (e) {
      console.error('Failed to reorder combo:', e)
    }
  }

</script>

<div class="flex min-w-0 flex-col gap-6 px-1 sm:px-0">
  <CombosHeader
    onCreateClick={openCreateModal}
    onAutoFreeClick={handleBuildAutoFree}
    {isBuildingAutoFree}
  />

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
    <div class="flex flex-col gap-4">
      {#each llmCombos as combo (combo.id)}
        <ComboCard
          {combo}
          strategyInfo={comboStrategies[combo.name]}
          {copiedId}
          onSetStrategy={handleSetStrategy}
          onOpenJudgePicker={(c) => { editingCombo = c; openModelPicker('judge') }}
          onClearJudge={clearJudge}
          onCopy={copyName}
          onEdit={openEditModal}
          onDelete={(c) => (confirmState = { name: c.name, id: c.id })}
          onReorder={handleReorderCombo}
        />
      {/each}
    </div>
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

<!-- Confirm Delete Modal (upstream ConfirmModal parity) -->
<ConfirmModal
  isOpen={!!confirmState}
  title="Delete Combo"
  message={confirmState ? `Delete combo "${confirmState.name}"?` : 'Delete this combo?'}
  onClose={() => (confirmState = null)}
  onConfirm={handleDeleteCombo}
/>
