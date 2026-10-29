<script lang="ts">
  import type { SelectionState } from './modelListFilter'

  interface Props {
    selection: SelectionState
    busy?: boolean
    /** Compatible nodes cannot disable models; the caller hides those actions. */
    canDisable?: boolean
    /** True when at least one selected row is a custom model (deletable). */
    canDelete?: boolean
    onToggleAll: () => void
    onTest: () => void
    onDisable: () => void
    onEnable: () => void
    onDelete: () => void
    onClear: () => void
  }

  let {
    selection,
    busy = false,
    canDisable = true,
    canDelete = true,
    onToggleAll,
    onTest,
    onDisable,
    onEnable,
    onDelete,
    onClear,
  }: Props = $props()

  let active = $derived(selection.selected > 0)

  function actionClass(danger = false): string {
    const base =
      'inline-flex items-center gap-1 rounded-md border border-border bg-surface-2 px-2 py-1 text-[11px] font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer'
    return danger ? `${base} hover:border-red-500/40 hover:bg-red-500/10 hover:text-red-500` : base
  }
</script>

<div class="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-surface-2/60 px-3 py-2">
  <label class="flex items-center gap-1.5 text-[11px] font-medium text-text-main cursor-pointer select-none">
    <input
      type="checkbox"
      checked={selection.allSelected}
      indeterminate={selection.indeterminate}
      onchange={onToggleAll}
      disabled={selection.total === 0}
      class="size-3.5 accent-brand-500 cursor-pointer disabled:cursor-not-allowed"
      aria-label="Select all visible models"
    />
    {#if active}
      <span>{selection.selected} of {selection.total} selected</span>
    {:else}
      <span>Select all ({selection.total})</span>
    {/if}
  </label>

  {#if active}
    <span class="h-3.5 w-px bg-border" aria-hidden="true"></span>

    <button type="button" class={actionClass()} onclick={onTest} disabled={busy} title="Send a minimal request to each selected model">
      <span class="material-symbols-outlined text-[14px]">science</span>
      Test selected
    </button>

    {#if canDisable}
      <button type="button" class={actionClass()} onclick={onDisable} disabled={busy} title="Hide the selected models from this provider">
        <span class="material-symbols-outlined text-[14px]">block</span>
        Disable selected
      </button>
      <button type="button" class={actionClass()} onclick={onEnable} disabled={busy} title="Show the selected models again">
        <span class="material-symbols-outlined text-[14px]">visibility</span>
        Enable selected
      </button>
    {/if}

    {#if canDelete}
      <button type="button" class={actionClass(true)} onclick={onDelete} disabled={busy} title="Delete the selected custom models; registry models are disabled instead">
        <span class="material-symbols-outlined text-[14px]">delete</span>
        Delete selected
      </button>
    {/if}

    <button type="button" class="ml-auto inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] text-text-muted transition-colors hover:text-text-main cursor-pointer" onclick={onClear}>
      Clear
    </button>
  {/if}
</div>
