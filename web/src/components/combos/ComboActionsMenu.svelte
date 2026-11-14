<!--
  ComboActionsMenu — the Test/Copy/Edit/Delete overflow menu for a combo card.

  The four actions used to sit in a labeled icon grid under the strategy
  selector; on a phone that grid was a 4-across row of small targets that
  pushed every card taller. The menu keeps the same four actions (plus their
  disabled states and the copied acknowledgement) behind one 44px trigger.

  The panel is placed by lib/ui/menuPosition so it is measured against the
  trigger and clamped to the viewport, and flips above the trigger when the
  space below is short — the same mechanics as analytics/SectionMenu.
-->
<script lang="ts">
  import { maxPanelWidth, placePanel, placementStyle } from '../../lib/ui/menuPosition'
  import { portal } from '../../lib/ui/portal'
  import type { Combo } from '../../api/client'

  interface Props {
    combo: Combo
    copiedId?: string | null
    isTesting?: boolean
    hasModels?: boolean
    onTest?: (combo: Combo) => void
    onCopy: (name: string, id: string) => void
    onEdit: (combo: Combo) => void
    onDelete: (combo: Combo) => void
  }

  let {
    combo,
    copiedId = null,
    isTesting = false,
    hasModels = true,
    onTest,
    onCopy,
    onEdit,
    onDelete,
  }: Props = $props()

  let open = $state(false)
  let trigger: HTMLButtonElement | null = $state(null)
  let panel: HTMLDivElement | null = $state(null)
  let panelStyle = $state('')

  const isCopied = $derived(copiedId === combo.id)

  function place(): void {
    if (!trigger || !panel) return
    panelStyle = `${placementStyle(
      placePanel({
        align: 'right',
        rect: trigger.getBoundingClientRect(),
        panelWidth: panel.offsetWidth,
      }),
    )};max-width:${maxPanelWidth()}`
  }

  function run(action: () => void): void {
    open = false
    action()
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      open = false
      trigger?.focus()
      return
    }
    if (event.key === 'Tab') open = false
  }

  $effect(() => {
    if (!open) return
    function handleDocClick(e: MouseEvent): void {
      const target = e.target as HTMLElement | null
      if (!trigger?.contains(target) && !panel?.contains(target)) open = false
    }
    function reposition(): void {
      place()
    }
    document.addEventListener('click', handleDocClick)
    document.addEventListener('keydown', onKeydown)
    window.addEventListener('resize', reposition)
    window.addEventListener('scroll', reposition, true)
    return () => {
      document.removeEventListener('click', handleDocClick)
      document.removeEventListener('keydown', onKeydown)
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
    }
  })

  $effect(() => {
    if (open && panel) place()
  })
</script>

<div class="relative shrink-0">
  <button
    bind:this={trigger}
    type="button"
    aria-haspopup="menu"
    aria-expanded={open}
    aria-label="Combo actions"
    onclick={() => (open = !open)}
    onkeydown={onKeydown}
    class="inline-flex h-11 sm:h-8 w-11 sm:w-8 items-center justify-center rounded-lg border border-border bg-surface-2 text-text-muted transition-colors hover:bg-surface-3 hover:text-text-main focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 cursor-pointer"
  >
    <span class="material-symbols-outlined text-[18px]" aria-hidden="true">
      {isTesting ? 'progress_activity animate-spin' : 'more_vert'}
    </span>
  </button>

  {#if open}
    <div
      use:portal
      bind:this={panel}
      role="menu"
      aria-label="Combo actions"
      tabindex="-1"
      style={panelStyle}
      onkeydown={onKeydown}
      class="fixed z-50 w-44 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)]"
    >
      <button
        type="button"
        role="menuitem"
        disabled={isTesting || !hasModels}
        onclick={() => run(() => onTest?.(combo))}
        class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-xs text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 disabled:opacity-40 disabled:cursor-not-allowed"
        title={hasModels ? 'Test all models in combo (Dead/Alive check)' : 'No models to test'}
      >
        <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true">science</span>
        <span>Test</span>
      </button>

      <button
        type="button"
        role="menuitem"
        onclick={() => run(() => onCopy(combo.name, combo.id))}
        class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-xs transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 cursor-pointer {isCopied
          ? 'text-success font-medium'
          : 'text-text-main'}"
        title="Copy combo name"
      >
        <span class="material-symbols-outlined text-[18px] {isCopied ? 'text-success' : 'text-text-muted'}" aria-hidden="true">
          {isCopied ? 'check' : 'content_copy'}
        </span>
        <span>{isCopied ? 'Copied' : 'Copy name'}</span>
      </button>

      <button
        type="button"
        role="menuitem"
        onclick={() => run(() => onEdit(combo))}
        class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-xs text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 cursor-pointer"
      >
        <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true">edit</span>
        <span>Edit</span>
      </button>

      <div class="my-1 border-t border-border-subtle"></div>

      <button
        type="button"
        role="menuitem"
        onclick={() => run(() => onDelete(combo))}
        class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-xs text-red-500 transition-colors hover:bg-red-500/10 focus:outline-none focus-visible:ring-2 focus-visible:ring-red-500/50 cursor-pointer"
      >
        <span class="material-symbols-outlined text-[18px]" aria-hidden="true">delete</span>
        <span>Delete</span>
      </button>
    </div>
  {/if}
</div>
