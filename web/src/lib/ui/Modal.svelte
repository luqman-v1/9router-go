<script lang="ts">
  // Port of decolua/9router src/shared/components/Modal.js
  import type { Snippet } from 'svelte'

  let {
    isOpen = false,
    onClose,
    title = '',
    icon = '',
    size = 'md',
    closeOnOverlay = true,
    showTrafficLights = true,
    class: klass = '',
    children,
    footer
  }: {
    isOpen?: boolean
    onClose?: () => void
    title?: string
    icon?: string
    size?: 'sm' | 'md' | 'lg' | 'xl' | 'full'
    closeOnOverlay?: boolean
    showTrafficLights?: boolean
    class?: string
    children?: Snippet
    footer?: Snippet
  } = $props()

  const sizes = {
    sm: 'max-w-sm',
    md: 'max-w-md',
    lg: 'max-w-lg',
    xl: 'max-w-xl',
    full: 'max-w-4xl'
  }

  // Lock background scroll while the modal is open.
  $effect(() => {
    if (!isOpen) return
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = previous
    }
  })

  $effect(() => {
    if (!isOpen) return
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose?.()
    }
    document.addEventListener('keydown', handleEscape)
    return () => document.removeEventListener('keydown', handleEscape)
  })
</script>

{#if isOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in"
      onclick={closeOnOverlay ? () => onClose?.() : undefined}
      onkeydown={(e) => e.key === 'Escape' && onClose?.()}
      role="presentation"
    ></div>

    <div
      class="relative w-full bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in {sizes[size]} {klass}"
    >
      {#if title || showTrafficLights}
        <div class="flex items-center justify-between p-2 border-b border-border-subtle">
          <div class="flex items-center">
            {#if showTrafficLights}
              <div class="hidden md:flex items-center gap-2 mr-4 ml-2">
                <button
                  type="button"
                  onclick={() => onClose?.()}
                  aria-label="Close"
                  title="Close"
                  class="w-4 h-4 rounded-full bg-[#FF5F56] hover:brightness-90 transition-all cursor-pointer flex items-center justify-center"
                >
                  <span class="text-[9px] font-bold text-white leading-none">✕</span>
                </button>
                <div class="w-4 h-4 rounded-full bg-[#3a3a3a]/20 dark:bg-white/15 cursor-not-allowed"></div>
                <div class="w-4 h-4 rounded-full bg-[#3a3a3a]/20 dark:bg-white/15 cursor-not-allowed"></div>
              </div>
            {/if}
            {#if title}
              <h2 class="flex items-center gap-2 text-lg font-semibold text-text-main">
                {#if icon}
                  <span class="material-symbols-outlined text-[20px] text-brand-500">{icon}</span>
                {/if}
                {title}
              </h2>
            {/if}
          </div>
          <button
            type="button"
            onclick={() => onClose?.()}
            aria-label="Close"
            class="md:hidden p-1.5 rounded-[10px] text-text-muted hover:bg-surface-2 hover:text-text-main transition-colors"
          >
            <span class="material-symbols-outlined text-[20px]">close</span>
          </button>
        </div>
      {/if}

      <div class="p-6 max-h-[calc(85vh-100px)] overflow-y-auto custom-scrollbar">
        {@render children?.()}
      </div>

      {#if footer}
        <div class="flex items-center justify-end gap-3 p-6 border-t border-border-subtle">
          {@render footer()}
        </div>
      {/if}
    </div>
  </div>
{/if}
