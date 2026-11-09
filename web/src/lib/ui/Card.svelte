<script lang="ts">
  // Port of decolua/9router src/shared/components/Card.js
  import type { Snippet } from 'svelte'

  let {
    title,
    subtitle,
    icon,
    action,
    padding = 'md',
    hover = false,
    elev = false,
    id,
    class: klass = '',
    children
  }: {
    title?: string
    subtitle?: string
    icon?: Snippet
    action?: Snippet
    padding?: 'none' | 'xs' | 'sm' | 'md' | 'lg'
    hover?: boolean
    elev?: boolean
    id?: string
    class?: string
    children?: Snippet
  } = $props()

  const paddings = {
    none: '',
    xs: 'p-3',
    sm: 'p-4',
    md: 'p-6',
    lg: 'p-8',
  }
</script>

<div
  {id}
  class="bg-surface border border-border-subtle rounded-[14px] {elev
    ? 'shadow-[var(--shadow-elev)]'
    : 'shadow-[var(--shadow-soft)]'} {hover
    ? 'hover:shadow-[var(--shadow-warm)] hover:border-brand-500/30 transition-all cursor-pointer'
    : ''} {paddings[padding]} {klass}"
>
  {#if title || action}
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-3">
        {#if icon}
          <div class="p-2 rounded-[10px] bg-bg text-text-muted">
            {@render icon()}
          </div>
        {/if}
        <div>
          {#if title}
            <h3 class="text-text-main font-semibold">{title}</h3>
          {/if}
          {#if subtitle}
            <p class="text-sm text-text-muted">{subtitle}</p>
          {/if}
        </div>
      </div>
      {#if action}
        <div>{@render action()}</div>
      {/if}
    </div>
  {/if}
  {@render children?.()}
</div>
