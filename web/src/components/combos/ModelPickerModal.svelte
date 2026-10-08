<script module lang="ts">
  import type { ResolvedPickerExtras } from './pickerExtras'
  let cachedPickerExtras: ResolvedPickerExtras | null = null
</script>

<script lang="ts">
  import { Info, Search, X } from 'lucide-svelte'
  import { api, type Combo, type ProviderConnection, type ProviderNode } from '../../api/client'
  import ModelPill from './ModelPill.svelte'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import {
    resolveFilteredCombos,
    resolveFilteredGroups,
    resolveModelPickerGroups,
  } from './pickerData'
  import { EMPTY_PICKER_EXTRAS, loadPickerExtras, type ResolvedPickerExtras } from './pickerExtras'

  interface Props {
    isOpen: boolean
    target: 'combo' | 'vision' | 'audio' | 'judge'
    connections?: ProviderConnection[]
    combos?: Combo[]
    providerNodes?: ProviderNode[]
    modelAliases?: Record<string, string>
    customModels?: Array<{ providerAlias?: string; id: string; name?: string; type?: string }>
    disabledModels?: Record<string, string[]>
    /** Overrides the batched fetch; the provider page passes its own cache. */
    deprecations?: Record<string, unknown>
    currentComboName?: string
    addedModelValues?: string[]
    onSelect: (modelValue: string) => void
    onDeselect?: (modelValue: string) => void
    onClose: () => void
  }

  let {
    isOpen,
    target,
    connections = [],
    combos = [],
    providerNodes = [],
    modelAliases,
    customModels,
    disabledModels,
    deprecations,
    currentComboName,
    addedModelValues = [],
    onSelect,
    onDeselect,
    onClose,
  }: Props = $props()

  let searchQuery = $state('')
  let fetchedExtras = $state<ResolvedPickerExtras>(cachedPickerExtras ?? EMPTY_PICKER_EXTRAS)

  $effect(() => {
    if (isOpen) {
      searchQuery = ''
      // Upstream parity (ComboFormModal fetchModalData): aliases drive
      // passthrough + custom-node rows; customs/disabled complete the merge.
      // One batch, one write: three chained fetches each published their own
      // $state and rebuilt the whole pill list on every settle (#61).
      void loadPickerExtras(api).then((extras) => {
        cachedPickerExtras = extras
        fetchedExtras = extras
      })
    }
  })

  let groups = $derived(
    resolveModelPickerGroups(connections, providerNodes, {
      modelAliases: modelAliases ?? fetchedExtras.modelAliases,
      customModels: customModels ?? fetchedExtras.customModels,
      disabledModels: disabledModels ?? fetchedExtras.disabledModels,
      deprecations: deprecations ?? fetchedExtras.deprecations,
    })
  )
  let filteredCombos = $derived(
    resolveFilteredCombos(combos, currentComboName, searchQuery, target)
  )
  let filteredGroups = $derived(resolveFilteredGroups(groups, searchQuery, target))

  function handleToggle(val: string) {
    if (addedModelValues.includes(val)) {
      if (onDeselect) {
        onDeselect(val)
      } else {
        onSelect(val)
      }
    } else {
      onSelect(val)
    }
  }
</script>

{#if isOpen}
  <div class="fixed inset-0 z-[70] flex items-center justify-center p-4">
    <!-- Overlay -->
    <div class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in" onclick={onClose} aria-hidden="true"></div>

    <div
      class="relative w-full max-w-md bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in overflow-hidden flex flex-col max-h-[85vh] z-10 p-4!"
      role="dialog"
      aria-modal="true"
    >
      <!-- Header (traffic lights + title) -->
      <div class="flex items-center justify-between p-2 border-b border-border-subtle -m-4 mb-4">
        <div class="flex items-center">
          <div class="hidden md:flex items-center gap-2 mr-4 ml-2">
            <button
              type="button"
              onclick={onClose}
              aria-label="Close"
              title="Close"
              class="w-4 h-4 rounded-full bg-[#FF5F56] hover:brightness-90 transition-all cursor-pointer flex items-center justify-center"
            >
              <span class="text-[9px] font-bold text-white leading-none">✕</span>
            </button>
            <div class="w-4 h-4 rounded-full bg-[#3a3a3a]/20 dark:bg-white/15 cursor-not-allowed"></div>
            <div class="w-4 h-4 rounded-full bg-[#3a3a3a]/20 dark:bg-white/15 cursor-not-allowed"></div>
          </div>
          <h2 class="text-lg font-semibold text-text-main">
            {target === 'vision'
              ? 'Add Vision Model'
              : target === 'audio'
                ? 'Add Audio Model'
                : target === 'judge'
                  ? 'Select Judge Model'
                  : 'Add Model to Combo'}
          </h2>
        </div>
        <button
          type="button"
          onclick={onClose}
          aria-label="Close"
          class="md:hidden p-1.5 rounded-[10px] text-text-muted hover:bg-surface-2 hover:text-text-main transition-colors"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <!-- Info bar -->
      <div class="flex items-center gap-2 mb-3 px-2.5 py-2 bg-brand-500/10 border border-brand-500/20 rounded-lg text-xs text-text-muted">
        <Info class="w-3.5 h-3.5 text-brand-500 shrink-0" />
        <span>Click to add, click again to remove. Changes are saved automatically.</span>
      </div>

      <!-- Search -->
      <div class="mb-3">
        <div class="relative">
          <Search class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-text-muted pointer-events-none" />
          <input
            type="text"
            placeholder="Search..."
            bind:value={searchQuery}
            class="w-full bg-surface border border-border rounded pl-8 pr-3 py-1.5 text-xs text-text-main placeholder:text-text-muted focus:outline-none focus:ring-1 focus:ring-brand-500/50"
          />
        </div>
      </div>

      <!-- Categories & Models List -->
      <div class="max-h-[400px] overflow-y-auto space-y-3 custom-scrollbar">
        <!-- Combos section - always first -->
        {#if filteredCombos.length > 0}
          <div>
            <div class="flex items-center gap-1.5 mb-1.5 sticky top-0 bg-surface py-0.5 z-10">
              <span class="text-xs font-medium text-brand-500">Combos</span>
              <span class="text-[10px] text-text-muted">({filteredCombos.length})</span>
            </div>
            <div class="flex flex-wrap gap-1.5">
              {#each filteredCombos as combo (combo.id)}
                <ModelPill
                  label={combo.name}
                  value={combo.name}
                  isAdded={addedModelValues.includes(combo.name)}
                  onToggle={handleToggle}
                />
              {/each}
            </div>
          </div>
        {/if}

        <!-- Provider sections -->
        {#each filteredGroups as group (group.id)}
          <div>
            <div class="flex items-center gap-1.5 mb-1.5 sticky top-0 bg-surface py-0.5 z-10">
              <ProviderArtwork
                id={group.id}
                alt={group.name}
                class="w-3.5 h-3.5 object-contain rounded-sm text-[9px] leading-none font-semibold"
              />
              <span class="text-xs font-medium text-brand-500">
                {group.name}
              </span>
              <span class="text-[10px] text-text-muted">
                ({group.models.length})
              </span>
            </div>
            <div class="flex flex-wrap gap-1.5">
              {#each group.models as model (model.value)}
                <ModelPill
                  label={model.name}
                  value={model.value}
                  isAdded={addedModelValues.includes(model.value)}
                  vision={model.caps.vision}
                  reasoning={model.caps.reasoning}
                  deprecated={model.deprecated}
                  onToggle={handleToggle}
                />
              {/each}
            </div>
          </div>
        {/each}

        {#if filteredCombos.length === 0 && filteredGroups.length === 0}
          <div class="text-center py-4 text-text-muted">
            <Search class="w-6 h-6 mx-auto mb-1 opacity-50" />
            <p class="text-xs">No models found</p>
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}
