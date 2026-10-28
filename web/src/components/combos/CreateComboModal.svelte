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
  import type { Combo, ComboRulesPreview, SmartRule } from '../../api/client'
  import { api } from '../../api/client'
  import { getModelCaps } from '../../lib/models'
  import Button from '../../lib/ui/Button.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import {
    COMBO_SORT_OPTIONS,
    DEFAULT_SMART_RULE_MAX_MEMBERS,
    parseSmartRules,
    SMART_RULE_MODES,
    smartRuleSummary,
    sortComboMembers
  } from './types'
  import type { ComboSortKey, ModelMetaMap } from './types'

  interface Props {
    isOpen: boolean
    editingCombo: Combo | null
    models: string[]
    isSaving?: boolean
    /** Server-side refusal (e.g. the name collides with a combo, alias or
     * custom model id). Shown inline so a 409 is never a silent no-op. */
    saveError?: string
    onClose: () => void
    onSave: (name: string, models: string[], matchRules: SmartRule[]) => Promise<void> | void
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

  // Smart rules. The modal is remounted per open (the {#key} block in
  // CombosView), so reading the stored list once here is the reset.
  let rules = $state<SmartRule[]>(parseSmartRules(editingCombo?.matchRules))
  let rulePreview = $state<ComboRulesPreview | null>(null)
  let rulePreviewError = $state('')
  // Load matching models: the write half of the preview. The button only
  // exists while a rule has something to say, and the delta it reports is
  // what the operator sees after pressing it.
  let isLoadingModels = $state(false)
  let loadError = $state('')
  let loadDelta = $state<{ added: number; removed: number } | null>(null)
  // A rule with no pattern cannot match anything, so the load button stays
  // hidden until there is something to load. This is the same "appears after
  // input, disappears when empty" rule the operator asked for.
  let rulesHavePattern = $derived(rules.some((r) => r.pattern.trim().length > 0))
  // The membership the rules would produce, straight from the preview. It is
  // what the per-model exclude acts on, so the operator sees the effect of an
  // exclusion on the same list they excluded it from.
  let previewMembers = $derived(rulePreview?.models ?? [])
  // The rule that currently claims a model, so an exclusion lands on the rule
  // that would otherwise re-add it. A model no rule claims is already a member
  // and has nothing to exclude.
  function ruleOwning(member: string): SmartRule | undefined {
    return rules.find(
      (r) => r.effect !== 'exclude' && Array.isArray(r.resolved) && r.resolved.includes(member)
    )
  }
  function excludeMember(member: string) {
    const owner = ruleOwning(member)
    if (!owner) return
    if (!Array.isArray(owner.excluded)) owner.excluded = []
    if (!owner.excluded.includes(member)) owner.excluded = [...owner.excluded, member]
    // The preview is a function of the rules, so it re-runs on its own; the
    // excluded model drops out of the list above without a manual refresh.
    loadDelta = null
  }

  function addRule() {
    rules = [
      ...rules,
      {
        id: `r${rules.length + 1}-${Date.now().toString(36)}`,
        pattern: '',
        mode: 'glob',
        effect: 'include',
        enabled: true,
        maxMembers: DEFAULT_SMART_RULE_MAX_MEMBERS,
        allKinds: false
      }
    ]
  }

  function removeRule(idx: number) {
    rules = rules.filter((_, i) => i !== idx)
  }
  // Load matching models: preview first so the operator sees the delta, then
  // write it. The merge is the server's job — it keeps the models the operator
  // pinned by hand and drops only the ones no rule claims any more — so this
  // function never computes the membership itself.
  async function loadMatchingModels() {
    if (!editingCombo || isLoadingModels) return
    isLoadingModels = true
    loadError = ''
    try {
      const res = await api.loadComboRules(editingCombo.id, rules)
      loadDelta = { added: res.added.length, removed: res.removed.length }
      // The server's answer is the new membership; adopting it keeps the
      // editor's list and the stored row in step without a refetch.
      if (res.changed && Array.isArray(res.models)) {
        onUpdateModels(res.models)
      }
    } catch (e) {
      loadDelta = null
      loadError = e instanceof Error ? e.message : String(e)
    } finally {
      isLoadingModels = false
    }
  }
  // Sort: a one-shot reorder of the stored list. The operator picks an option,
  // the list is rewritten, and anything can be dragged back afterwards — none
  // of these is a standing order the gateway re-applies on every rule run,
  // which is what kept the alphabetical maxMembers cut from fighting the
  // manual ordering.
  let sortKey = $state<ComboSortKey>('manual')
  let sortNote = $state('')
  let modelMeta = $state<ModelMetaMap | null>(null)
  // Four of the options need data the editor does not already hold, so it is
  // fetched lazily on first use rather than on every open.
  async function ensureMeta() {
    if (modelMeta) return
    try {
      const res = await api.getModelMeta()
      modelMeta = res.meta
    } catch {
      // A failed read must not block the other sorts: the options simply
      // behave as "nothing known", which leaves the order unchanged rather
      // than guessing at it.
      modelMeta = {}
    }
  }
  async function applySort(key: ComboSortKey) {
    sortKey = key
    if (key === 'latency' || key === 'context-window' || key === 'capabilities' || key === 'kind') {
      await ensureMeta()
    }
    const sorted = sortComboMembers(models, key, modelMeta ?? undefined)
    if (sorted.length !== models.length) return
    let changed = false
    for (let i = 0; i < sorted.length; i++) {
      if (sorted[i] !== models[i]) {
        changed = true
        break
      }
    }
    if (!changed) {
      sortNote = 'already in this order'
      return
    }
    onUpdateModels(sorted)
    sortNote = key === 'shuffle' ? 'shuffled once' : 'sorted'
  }

  function rulePreviewFor(id: string) {
    return rulePreview?.rules.find((r) => r.id === id)
  }

  // The preview is a dry run against the live model set, so it needs a saved
  // combo to ask about. While creating there is nothing to preview yet.
  let rulesKey = $derived(JSON.stringify(rules))
  $effect(() => {
    const key = rulesKey
    if (!editingCombo) {
      rulePreview = null
      rulePreviewError = ''
      return
    }
    const timer = setTimeout(() => {
      void runRulePreview(key)
    }, 400)
    return () => clearTimeout(timer)
  })

  async function runRulePreview(key: string) {
    // A newer edit supersedes the run already in flight, so its answer is
    // dropped rather than shown as if it described the current rules.
    if (key !== rulesKey) return
    if (!editingCombo) return
    rulePreviewError = ''
    try {
      rulePreview = await api.previewComboRules(editingCombo.id, rules)
    } catch (e) {
      rulePreview = null
      rulePreviewError = e instanceof Error ? e.message : String(e)
    }
  }

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
  const VALID_NAME_REGEX = /^[a-zA-Z0-9_.-]+$/

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
    onSave(modalName.trim(), models, rules)
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

    <!-- Smart rules: the source of membership, so it sits above the list it
         feeds. With twenty members the list fills the modal, and a rules
         editor below it was only reachable by scrolling past the result to
         reach the mechanism that produced it. -->
    <div>
      <div class="flex items-center justify-between mb-1.5">
        <label class="text-sm font-medium">Smart rules</label>
        <button
          type="button"
          onclick={addRule}
          class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium text-brand-500 hover:bg-brand-500/10 transition-colors cursor-pointer"
        >
          <Plus class="w-3 h-3" />
          <span>Add rule</span>
        </button>
      </div>
      <p class="text-[10px] text-text-muted mb-2">
        A model that matches a rule joins this combo by itself, and leaves it when it disappears.
        Models you pick below are yours to keep.
      </p>
      {#if rules.length === 0}
        <div
          class="text-center py-3 border border-dashed border-black/10 dark:border-white/10 rounded-lg bg-black/[0.01] dark:bg-white/[0.01] text-[11px] text-text-muted italic"
        >
          No rules — this combo holds only the models you picked.
        </div>
      {:else}
        <div class="flex flex-col gap-2">
          {#each rules as rule, idx (rule.id)}
            <div class="rounded-lg border border-border bg-surface-2 p-2 flex flex-col gap-1.5">
              <div class="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={rule.enabled}
                  onchange={(e) => (rule.enabled = e.currentTarget.checked)}
                  class="size-3.5 shrink-0 cursor-pointer accent-brand-500"
                  title={rule.enabled ? 'Rule is on' : 'Rule is off'}
                />
                <input
                  type="text"
                  value={rule.pattern}
                  oninput={(e) => (rule.pattern = e.currentTarget.value)}
                  placeholder="kenari/deepseek*"
                  class="min-w-0 flex-1 rounded border border-border bg-surface px-2 py-1 font-mono text-xs text-text-main focus:outline-none focus:border-brand-500"
                />
                <button
                  type="button"
                  onclick={() => removeRule(idx)}
                  class="shrink-0 rounded p-1 text-text-muted hover:bg-red-500/10 hover:text-red-500 transition-colors cursor-pointer"
                  title="Remove rule"
                >
                  <X class="w-3.5 h-3.5" />
                </button>
              </div>
              <div class="flex flex-wrap items-center gap-2">
                <select
                  value={rule.mode}
                  onchange={(e) => (rule.mode = e.currentTarget.value as SmartRule['mode'])}
                  class="rounded border border-border bg-surface px-1.5 py-0.5 text-[11px] text-text-main focus:outline-none focus:border-brand-500 cursor-pointer"
                  title="How the pattern is matched"
                >
                  {#each SMART_RULE_MODES as mode (mode.value)}
                    <option value={mode.value}>{mode.label}</option>
                  {/each}
                </select>
                <select
                  value={rule.effect}
                  onchange={(e) => (rule.effect = e.currentTarget.value as SmartRule['effect'])}
                  class="rounded border border-border bg-surface px-1.5 py-0.5 text-[11px] text-text-main focus:outline-none focus:border-brand-500 cursor-pointer"
                  title="Add matching models, or keep them out"
                >
                  <option value="include">Add</option>
                  <option value="exclude">Exclude</option>
                </select>
                <label class="flex items-center gap-1 text-[10px] text-text-muted" title="0 means no cap">
                  <span>max</span>
                  <input
                    type="number"
                    min="0"
                    value={rule.maxMembers}
                    oninput={(e) => (rule.maxMembers = Math.max(0, Number(e.currentTarget.value) || 0))}
                    class="w-14 rounded border border-border bg-surface px-1.5 py-0.5 text-[11px] text-text-main focus:outline-none focus:border-brand-500"
                  />
                </label>
                <label class="flex items-center gap-1 text-[10px] text-text-muted" title="Also match embedding, image, tts and stt models">
                  <input
                    type="checkbox"
                    checked={rule.allKinds}
                    onchange={(e) => (rule.allKinds = e.currentTarget.checked)}
                    class="size-3 cursor-pointer accent-brand-500"
                  />
                  <span>all kinds</span>
                </label>
              </div>
              <p class="text-[10px] text-text-muted">
                {smartRuleSummary(rule, rulePreviewFor(rule.id))}
              </p>
            </div>
          {/each}
        </div>
      {/if}
      <!-- Load matching models: the write half of the preview above. It only
           exists while a rule has something to say, so an operator who is not
           using rules never sees a button that would do nothing. -->
      {#if rulesHavePattern && editingCombo}
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <button
            type="button"
            onclick={loadMatchingModels}
            disabled={isLoadingModels}
            class="inline-flex items-center gap-1.5 rounded-lg border border-brand-500/40 bg-brand-500/10 px-2.5 py-1.5 text-[11px] font-medium text-brand-500 hover:bg-brand-500/20 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {#if isLoadingModels}
              <span class="size-3 animate-spin rounded-full border-2 border-brand-500/30 border-t-brand-500"></span>
              <span>Loading…</span>
            {:else}
              <Layers class="w-3.5 h-3.5" />
              <span>Load matching models</span>
            {/if}
          </button>
          {#if loadDelta}
            <span class="text-[10px] {loadDelta.added > 0 || loadDelta.removed > 0 ? 'text-text-muted' : 'text-text-subtle'}">
              {loadDelta.added > 0 || loadDelta.removed > 0
                ? `+${loadDelta.added} match${loadDelta.added === 1 ? '' : 'es'}${loadDelta.removed > 0 ? `, −${loadDelta.removed} stale` : ''}`
                : 'already up to date'}
            </span>
          {/if}
        </div>
      {/if}
      {#if loadError}
        <p class="mt-1 text-[10px] text-red-600 dark:text-red-400 break-words">{loadError}</p>
      {/if}
      {#if rulePreviewError}
        <p class="mt-1 text-[10px] text-red-600 dark:text-red-400 break-words">{rulePreviewError}</p>
      {/if}
      <!-- What the rules would put in the combo, with a per-model exclude.
           Excluding here records the model in the owning rule's excluded list
           rather than rewriting the pattern: the pattern stays readable, and
           the exclusion survives every later reconcile. -->
      {#if previewMembers.length > 0}
        <div class="mt-2 rounded-lg border border-border bg-surface-2 p-2">
          <div class="flex items-center justify-between mb-1">
            <span class="text-[10px] font-medium text-text-muted">
              {previewMembers.length} model{previewMembers.length === 1 ? '' : 's'} would join
            </span>
            <button
              type="button"
              onclick={loadMatchingModels}
              disabled={isLoadingModels}
              class="text-[10px] font-medium text-brand-500 hover:underline cursor-pointer disabled:opacity-50"
            >
              Load all
            </button>
          </div>
          <div class="flex flex-wrap gap-1">
            {#each previewMembers as member}
              {@const owner = ruleOwning(member)}
              <span
                class="inline-flex items-center gap-1 rounded-md border border-border bg-surface px-1.5 py-0.5 font-mono text-[10px] text-text-main"
                title={owner ? `owned by rule ${owner.id}` : 'already a member'}
              >
                <span class="truncate max-w-[220px]">{member}</span>
                {#if owner}
                  <button
                    type="button"
                    onclick={() => excludeMember(member)}
                    class="shrink-0 rounded p-0.5 text-text-muted hover:bg-red-500/10 hover:text-red-500 transition-colors cursor-pointer"
                    title="Exclude this model from the rule that claims it"
                  >
                    <X class="w-2.5 h-2.5" />
                  </button>
                {/if}
              </span>
            {/each}
          </div>
        </div>
      {/if}
    </div>
    <!-- Models -->
    <div>
      <div class="flex items-center justify-between mb-1.5">
        <label class="text-sm font-medium">Models</label>
        <div class="flex items-center gap-1.5">
          <!-- Sort: a one-shot reorder of the stored list. The operator picks
               an option, the list is rewritten, and anything can be dragged
               back afterwards — none of these is a standing order the gateway
               re-applies on every rule run. -->
          <select
            value={sortKey}
            onchange={(e) => applySort(e.currentTarget.value as ComboSortKey)}
            class="rounded border border-border bg-surface px-1.5 py-0.5 text-[11px] text-text-main focus:outline-none focus:border-brand-500 cursor-pointer"
            title="Reorder the members once. You can drag anything back afterwards."
          >
            {#each COMBO_SORT_OPTIONS as opt (opt.value)}
              <option value={opt.value} title={opt.hint}>{opt.label}</option>
            {/each}
          </select>
          {#if sortNote}
            <span class="text-[10px] text-text-subtle">{sortNote}</span>
          {/if}
          <button
            type="button"
            onclick={onOpenModelPicker}
            class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium text-brand-500 hover:bg-brand-500/10 transition-colors cursor-pointer"
          >
            <Plus class="w-3 h-3" />
            <span>Add Models Manually</span>
          </button>
        </div>
      </div>

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
    </div>

    {#if saveError}
      <p class="text-xs text-red-600 dark:text-red-400 break-words" role="alert">{saveError}</p>
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
