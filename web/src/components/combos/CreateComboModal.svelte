<script lang="ts">
  import {
    ArrowDown,
    ArrowUp,
    Brain,
    Eye,
    GripVertical,
    Layers,
    Plus,
    X
  } from 'lucide-svelte'
  import type { Combo } from '../../api/client'
  import { getModelCaps } from '../../lib/models'
  import Button from '../../lib/ui/Button.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import Modal from '../../lib/ui/Modal.svelte'

  interface Props {
    isOpen: boolean
    editingCombo: Combo | null
    models: string[]
    isSaving?: boolean
    /** Server-side refusal (e.g. the name collides with a combo, alias or
     * custom model id). Shown inline so a 409 is never a silent no-op. */
    saveError?: string
    onClose: () => void
    onSave: (name: string, models: string[]) => Promise<void> | void
    onOpenModelPicker: () => void
    onUpdateModels: (models: string[]) => void
  }

  let {
    isOpen,
    editingCombo,
    models,
    isSaving = false,
    saveError = '',
    onClose,
    onSave,
    onOpenModelPicker,
    onUpdateModels,
  }: Props = $props()

  let modalName = $state(editingCombo?.name || '')
  let modalNameError = $state('')
  let editingIdx = $state<number | null>(null)
  let editDraft = $state('')
  let draggedIdx = $state<number | null>(null)
  let dragOverIdx = $state<number | null>(null)

  function handleDragStart(e: DragEvent, idx: number) {
    draggedIdx = idx
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move'
      e.dataTransfer.setData('text/plain', String(idx))
    }
  }

  function handleDragOver(e: DragEvent, idx: number) {
    e.preventDefault()
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = 'move'
    }
    dragOverIdx = idx
  }

  function handleDragLeave(idx: number) {
    if (dragOverIdx === idx) {
      dragOverIdx = null
    }
  }

  function handleDrop(e: DragEvent, targetIdx: number) {
    e.preventDefault()
    if (draggedIdx === null || draggedIdx === targetIdx) {
      draggedIdx = null
      dragOverIdx = null
      return
    }
    const arr = [...models]
    const [moved] = arr.splice(draggedIdx, 1)
    arr.splice(targetIdx, 0, moved)
    onUpdateModels(arr)
    draggedIdx = null
    dragOverIdx = null
  }

  function handleDragEnd() {
    draggedIdx = null
    dragOverIdx = null
  }
  const VALID_NAME_REGEX = /^[a-zA-Z0-9_.\-]+$/

  function validateModalName(name: string): boolean {
    if (!name.trim()) {
      modalNameError = 'Name is required'
      return false
    }
    if (!VALID_NAME_REGEX.test(name.trim())) {
      modalNameError = 'Only letters, numbers, -, _ and . allowed'
      return false
    }
    modalNameError = ''
    return true
  }

  function handleSave() {
    if (!validateModalName(modalName)) return
    onSave(modalName.trim(), models)
  }

  function moveModel(idx: number, delta: number) {
    const arr = [...models]
    const target = idx + delta
    if (target < 0 || target >= arr.length) return
    const temp = arr[idx]
    arr[idx] = arr[target]
    arr[target] = temp
    onUpdateModels(arr)
  }

  function removeModel(idx: number) {
    onUpdateModels(models.filter((_, i) => i !== idx))
    if (editingIdx === idx) editingIdx = null
  }

  function startEdit(idx: number, model: string) {
    editingIdx = idx
    editDraft = model
  }

  function commitEdit(idx: number) {
    const trimmed = editDraft.trim()
    if (trimmed && trimmed !== models[idx]) {
      const arr = [...models]
      arr[idx] = trimmed
      onUpdateModels(arr)
    }
    editingIdx = null
  }
</script>

<Modal
  {isOpen}
  onClose={onClose}
  title={editingCombo ? 'Edit Combo' : 'Create Combo'}
  size="lg"
>
  <div class="flex flex-col gap-3">
    <!-- Name -->
    <div>
      <Input
        label="Combo Name"
        bind:value={modalName}
        placeholder="my-combo"
        error={modalNameError}
      />
      <p class="text-[10px] text-text-muted mt-0.5">
        Only letters, numbers, -, _ and . allowed
      </p>
    </div>

    <!-- Models -->
    <div>
      <label class="text-sm font-medium mb-1.5 block">Models</label>

      {#if models.length === 0}
        <div class="text-center py-4 border border-dashed border-black/10 dark:border-white/10 rounded-lg bg-black/[0.01] dark:bg-white/[0.01]">
          <Layers class="w-6 h-6 text-text-muted mx-auto mb-1 opacity-50" />
          <p class="text-xs text-text-muted">No models added yet</p>
        </div>
      {:else}
        <div class="flex flex-col gap-1 max-h-[55vh] overflow-y-auto sm:max-h-[350px]">
          {#each models as model, idx}
            {@const caps = getModelCaps(model)}
            <div
              class="group flex min-w-0 items-center gap-1.5 rounded-md px-2 py-1 transition-colors {draggedIdx === idx ? 'opacity-40 bg-brand-500/10' : dragOverIdx === idx ? 'border-2 border-brand-500 bg-brand-500/5' : 'bg-black/[0.02] hover:bg-black/[0.04] dark:bg-white/[0.02] dark:hover:bg-white/[0.04]'}"
              draggable="true"
              ondragstart={(e) => handleDragStart(e, idx)}
              ondragover={(e) => handleDragOver(e, idx)}
              ondragleave={() => handleDragLeave(idx)}
              ondrop={(e) => handleDrop(e, idx)}
              ondragend={handleDragEnd}
            >
              <GripVertical class="w-3.5 h-3.5 text-text-muted cursor-grab active:cursor-grabbing shrink-0" />
              <span class="text-[10px] font-medium text-text-muted w-3 text-center shrink-0">{idx + 1}</span>
              {#if editingIdx === idx}
                <input
                  autofocus
                  bind:value={editDraft}
                  onblur={() => commitEdit(idx)}
                  onkeydown={(e) => {
                    if (e.key === 'Enter') commitEdit(idx)
                    if (e.key === 'Escape') editingIdx = null
                  }}
                  class="min-w-0 flex-1 rounded border border-brand-500/40 bg-white px-1.5 py-0.5 font-mono text-xs text-text-main outline-none dark:bg-black/20"
                />
              {:else}
                <div
                  class="min-w-0 flex-1 cursor-text truncate rounded px-1.5 py-0.5 font-mono text-xs text-text-main hover:bg-black/5 dark:hover:bg-white/5"
                  onclick={() => startEdit(idx, model)}
                  onkeydown={(e) => e.key === 'Enter' && startEdit(idx, model)}
                  role="textbox"
                  tabindex="0"
                  title="Click to edit"
                >
                  {model}
                </div>
              {/if}
              {#if caps.vision}
                <Eye class="w-3 h-3 text-blue-500 shrink-0" title="Vision — Supports image input" />
              {/if}
              {#if caps.reasoning}
                <Brain class="w-3 h-3 text-amber-500 shrink-0" title="Reasoning — Supports reasoning / thinking" />
              {/if}
              <div class="flex shrink-0 items-center gap-0.5">
                <button
                  type="button"
                  onclick={() => moveModel(idx, -1)}
                  disabled={idx === 0}
                  class="p-0.5 rounded {idx === 0 ? 'text-text-muted/20 cursor-not-allowed' : 'text-text-muted hover:text-brand-500 hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer'}"
                  title="Move up"
                >
                  <ArrowUp class="w-3 h-3" />
                </button>
                <button
                  type="button"
                  onclick={() => moveModel(idx, 1)}
                  disabled={idx === models.length - 1}
                  class="p-0.5 rounded {idx === models.length - 1 ? 'text-text-muted/20 cursor-not-allowed' : 'text-text-muted hover:text-brand-500 hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer'}"
                  title="Move down"
                >
                  <ArrowDown class="w-3 h-3" />
                </button>
              </div>
              <button
                type="button"
                onclick={() => removeModel(idx)}
                class="p-0.5 hover:bg-red-500/10 rounded text-text-muted hover:text-red-500 transition-all cursor-pointer"
                title="Remove"
              >
                <X class="w-3 h-3" />
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <!-- Add Model button -->
      <button
        type="button"
        onclick={onOpenModelPicker}
        class="w-full mt-2 py-2 border border-dashed border-black/10 dark:border-white/10 rounded-lg text-xs text-brand-500 font-medium hover:text-brand-500 hover:border-brand-500/50 transition-colors flex items-center justify-center gap-1 cursor-pointer"
      >
        <Plus class="w-4 h-4" />
        <span>Add Model</span>
      </button>
    </div>

    {#if saveError}
      <p class="text-xs text-danger break-words" role="alert">{saveError}</p>
    {/if}

    <!-- Actions -->
    <div class="flex flex-col gap-2 pt-1 sm:flex-row">
      <Button onclick={onClose} variant="ghost" fullWidth size="sm">
        Cancel
      </Button>
      <Button
        onclick={handleSave}
        fullWidth
        size="sm"
        disabled={!modalName.trim() || !!modalNameError || isSaving}
        loading={isSaving}
      >
        {isSaving ? 'Saving...' : editingCombo ? 'Save' : 'Create'}
      </Button>
    </div>
  </div>
</Modal>
