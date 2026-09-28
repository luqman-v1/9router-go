<script lang="ts">
  import {
    Check,
    ChevronDown,
    ChevronUp,
    Copy,
    Eye,
    Gavel,
    Layers,
    Lock,
    Pencil,
    Sparkles,
    Trash2,
    Wand2,
    X
  } from 'lucide-svelte'
  import type { Combo } from '../../api/client'
  import Card from '../../lib/ui/Card.svelte'
  import {
    getComboModels,
    hasReasoning,
    hasVision,
    isAutoFreeCombo,
    type ComboStrategyInfo
  } from './types'

  interface Props {
    combo: Combo
    strategyInfo?: ComboStrategyInfo
    copiedId?: string | null
    onSetStrategy: (combo: Combo, strategy: string) => void
    onOpenJudgePicker: (combo: Combo) => void
    onClearJudge: (comboName: string) => void
    onCopy: (name: string, id: string) => void
    onEdit: (combo: Combo) => void
    onDelete: (combo: Combo) => void
    onReorder?: (combo: Combo, models: string[]) => void
    isSelected?: boolean
    onToggleSelect?: (combo: Combo) => void
  }

  let {
    combo,
    strategyInfo = {},
    copiedId = null,
    onSetStrategy,
    onOpenJudgePicker,
    onClearJudge,
    onCopy,
    onEdit,
    onDelete,
    onReorder,
    isSelected = false,
    onToggleSelect,
  }: Props = $props()

  let modelsList = $derived(getComboModels(combo))

  function handleToggleSelect(e: Event) {
    e.stopPropagation()
    onToggleSelect?.(combo)
  }

  function moveModel(index: number, delta: -1 | 1) {
    const next = modelsList.slice()
    const target = index + delta
    if (target < 0 || target >= next.length) return
    ;[next[index], next[target]] = [next[target], next[index]]
    onReorder?.(combo, next)
  }
  let isLocked = $derived(isAutoFreeCombo(combo))
  let currentStrategy = $derived(strategyInfo.fallbackStrategy || combo.strategy || 'fallback')
  let judgeModel = $derived(strategyInfo.judgeModel || '')
  let isFusion = $derived(currentStrategy === 'fusion')
</script>

<Card padding="sm" class="group">
  <div class="flex min-w-0 flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
    <!-- Left: Icon, Name, Model Badges & Fusion Judge -->
    <div class="flex min-w-0 flex-1 items-start gap-3 sm:items-center">
      <input
        type="checkbox"
        checked={isSelected}
        onchange={handleToggleSelect}
        disabled={isLocked}
        class="mt-1 size-4 shrink-0 cursor-pointer accent-brand-500 disabled:cursor-not-allowed disabled:opacity-30"
        title={isLocked ? 'Locked auto free-tier combo cannot be deleted' : 'Select for bulk delete'}
      />
      <div class="size-8 rounded-lg bg-brand-500/10 flex items-center justify-center shrink-0">
        <Layers class="w-4 h-4 text-brand-500" />
      </div>
      <div class="min-w-0 flex-1">
        <code class="block truncate font-mono text-sm font-medium text-text-main">{combo.name}</code>
        {#if isLocked}
          <span class="mt-1 inline-flex items-center gap-1 rounded-full bg-brand-500/10 px-2 py-0.5 text-[10px] font-medium text-brand-500 border border-brand-500/30">
            <Lock class="w-2.5 h-2.5" />
            Auto Free Tier · reorder only
          </span>
        {:else if combo.kind?.startsWith('auto-')}
          <span class="mt-1 inline-flex items-center gap-1 rounded-full bg-surface-3 px-2 py-0.5 text-[10px] font-medium text-text-muted border border-border">
            <Wand2 class="w-2.5 h-2.5" />
            Auto-generated
          </span>
        {/if}
        <div class="mt-1 flex min-w-0 flex-wrap items-center gap-1">
          {#if modelsList.length === 0}
            <span class="text-xs text-text-muted italic">No models</span>
          {:else}
            {#each modelsList.slice(0, 3) as model, i}
              <code class="inline-flex items-center gap-1 rounded bg-black/5 dark:bg-white/5 px-1.5 py-0.5 font-mono text-xs text-text-muted">
                {#if isLocked}
                  <span class="inline-flex items-center">
                    <button
                      type="button"
                      onclick={() => moveModel(i, -1)}
                      disabled={i === 0}
                      class="rounded p-0.5 text-text-muted/70 hover:text-primary disabled:opacity-20 disabled:cursor-not-allowed cursor-pointer"
                      title="Move up"
                    >
                      <ChevronUp class="w-2.5 h-2.5" />
                    </button>
                    <button
                      type="button"
                      onclick={() => moveModel(i, 1)}
                      disabled={i === Math.min(modelsList.length, 3) - 1}
                      class="rounded p-0.5 text-text-muted/70 hover:text-primary disabled:opacity-20 disabled:cursor-not-allowed cursor-pointer"
                      title="Move down"
                    >
                      <ChevronDown class="w-2.5 h-2.5" />
                    </button>
                  </span>
                {/if}
                <span>{model}</span>
                {#if hasVision(model)}
                  <Eye class="w-3 h-3 text-blue-500 shrink-0" title="Vision — Supports image input" />
                {/if}
                {#if hasReasoning(model)}
                  <Sparkles class="w-3 h-3 text-amber-500 shrink-0" title="Reasoning — Supports reasoning / thinking" />
                {/if}
              </code>
            {/each}
            {#if modelsList.length > 3}
              <span class="text-[10px] text-text-muted">+{modelsList.length - 3} more</span>
            {/if}
          {/if}
        </div>

        <!-- Fusion: judge picker (Auto = first model) -->
        {#if isFusion}
          <div class="mt-2 flex min-w-0 flex-wrap items-center gap-1.5">
            <span class="text-[11px] font-medium text-text-muted">Judge</span>
            <button
              type="button"
              onclick={() => onOpenJudgePicker(combo)}
              class="inline-flex max-w-full items-center gap-1 rounded border border-dashed border-brand-500/40 px-1.5 py-0.5 font-mono text-[11px] text-brand-500 hover:border-brand-500 hover:bg-brand-500/5 transition-colors cursor-pointer"
              title="Pick the model that fuses panel answers"
            >
              <Gavel class="w-3 h-3" />
              <span class="truncate">{judgeModel || `Auto — ${modelsList[0] || 'first model'}`}</span>
            </button>
            {#if judgeModel}
              <button
                type="button"
                onclick={() => onClearJudge(combo.name)}
                class="p-0.5 rounded text-text-muted hover:text-red-500 hover:bg-red-500/10 transition-colors cursor-pointer"
                title="Reset judge to Auto"
              >
                <X class="w-3 h-3" />
              </button>
            {/if}
          </div>
        {/if}
      </div>
    </div>

    <!-- Actions: Strategy selector + Copy/Edit/Delete -->
    <div class="flex w-full flex-col gap-2 sm:w-auto sm:flex-row sm:items-center sm:gap-3 sm:shrink-0">
      <!-- Strategy dropdown -->
      <div class="w-full sm:w-[200px]">
        <select
          value={currentStrategy}
          onchange={(e) => onSetStrategy(combo, e.currentTarget.value)}
          class="w-full bg-surface-2 border border-border rounded-lg px-2.5 py-1.5 text-xs text-text-main focus:outline-none focus:border-brand-500 cursor-pointer font-body"
        >
          <option value="fallback">Fallback — try in order</option>
          <option value="round-robin">Round Robin — rotate</option>
          <option value="fusion">Fusion — panel + judge</option>
        </select>
      </div>

      <!-- Icon buttons with labels -->
      <div class="grid grid-cols-3 gap-1 sm:flex sm:items-center">
        <button
          type="button"
          onclick={() => onCopy(combo.name, combo.id)}
          class="flex flex-col items-center justify-center rounded px-2.5 py-1 text-text-muted transition-colors hover:bg-black/5 dark:hover:bg-white/5 hover:text-brand-500 cursor-pointer"
          title="Copy combo name"
        >
          {#if copiedId === combo.id}
            <Check class="w-4 h-4 text-success" />
            <span class="text-[10px] leading-tight text-success font-medium">Copied</span>
          {:else}
            <Copy class="w-4 h-4" />
            <span class="text-[10px] leading-tight">Copy</span>
          {/if}
        </button>

        <button
          type="button"
          onclick={() => onEdit(combo)}
          class="flex flex-col items-center justify-center rounded px-2.5 py-1 text-text-muted transition-colors hover:bg-black/5 dark:hover:bg-white/5 hover:text-brand-500 cursor-pointer"
          title="Edit"
          disabled={isLocked}
        >
          <Pencil class="w-4 h-4" />
          <span class="text-[10px] leading-tight">Edit</span>
        </button>

        {#if isLocked}
          <button
            type="button"
            onclick={() => onDelete(combo)}
            class="flex flex-col items-center justify-center rounded px-2.5 py-1 text-text-muted/40 cursor-not-allowed"
            title="Auto free-tier combo is locked"
          >
            <Trash2 class="w-4 h-4" />
            <span class="text-[10px] leading-tight">Locked</span>
          </button>
        {:else}
          <button
            type="button"
            onclick={() => onDelete(combo)}
            class="flex flex-col items-center justify-center rounded px-2.5 py-1 text-red-500 transition-colors hover:bg-red-500/10 cursor-pointer"
            title="Delete"
          >
            <Trash2 class="w-4 h-4" />
            <span class="text-[10px] leading-tight">Delete</span>
          </button>
        {/if}
      </div>
    </div>
  </div>
</Card>
