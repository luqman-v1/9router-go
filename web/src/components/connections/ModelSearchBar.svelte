<script lang="ts">
  import { parseSearchQuery } from '../../lib/modelSearchFilter'

  interface Props {
    value: string
    onSearch: (query: string) => void
    placeholder?: string
  }

  let { value = $bindable(''), onSearch, placeholder = 'Search models (id or name)...' }: Props = $props()

  // Same badge vocabulary as the combo model picker: an operator who
  // learned "/regex/" there should not have to relearn it here.
  let parsed = $derived(parseSearchQuery(value))

  function clear() {
    value = ''
    onSearch('')
  }
</script>

<div class="relative">
  <span class="material-symbols-outlined absolute left-2.5 top-1/2 -translate-y-1/2 text-text-muted pointer-events-none text-[16px]">search</span>
  <input
    type="text"
    {placeholder}
    bind:value
    oninput={() => onSearch(value)}
    class="w-full bg-surface border border-border rounded pl-8 pr-24 py-1.5 text-xs text-text-main placeholder:text-text-muted focus:outline-none focus:ring-1 focus:ring-brand-500/50"
  />
  <div class="absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-1">
    {#if parsed.isRegex}
      <span class="text-[10px] px-1 py-0.2 rounded bg-brand-500/10 text-brand-500 font-mono font-medium">regex</span>
    {:else if parsed.negativeTerms.length > 0}
      <span class="text-[10px] px-1 py-0.2 rounded bg-amber-500/10 text-amber-500 font-mono font-medium">!exclude</span>
    {/if}
    {#if value}
      <button
        type="button"
        onclick={clear}
        aria-label="Clear search"
        class="p-1 rounded text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
      >
        <span class="material-symbols-outlined text-[14px]">close</span>
      </button>
    {/if}
  </div>
</div>
