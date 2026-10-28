<script lang="ts">
  import { PauseCircle } from 'lucide-svelte'
  import Badge from '../../lib/ui/Badge.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import ProviderIcon from './ProviderIcon.svelte'
  import { type ProviderStats } from './types'
  import { isProviderTogglePersistent as readProviderTogglePersistent } from '../../lib/compactNumber'

  interface Props {
    id: string
    name: string
    apiType?: string
    stats: ProviderStats
    noAuth?: boolean
    onClick: () => void
    onToggleAll?: (active: boolean) => void
  }

  let {
    id,
    name,
    apiType,
    stats,
    noAuth = false,
    onClick,
    onToggleAll,
  }: Props = $props()

  let isProviderTogglePersistent = $state(readProviderTogglePersistent())
  let isAllDisabled = $derived(stats.allDisabled)
  let toggleClass = $derived(
    isProviderTogglePersistent ? 'opacity-100 transition-opacity' : 'opacity-100 sm:opacity-0 sm:group-hover:opacity-100 transition-opacity'
  )
</script>

<div
  onclick={onClick}
  onkeydown={(e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onClick()
    }
  }}
  role="button"
  tabindex="0"
  class="group min-w-0 cursor-pointer text-left focus:outline-none"
>
  <Card
    padding="xs"
    class="h-full hover:bg-black/[0.02] dark:hover:bg-white/[0.02] transition-colors p-3 rounded-xl border {isAllDisabled
      ? 'border-border bg-surface opacity-50'
      : stats.errorCount > 0
        ? 'border-red-500/50 bg-red-500/[0.02]'
        : stats.connected > 0 || noAuth
          ? 'border-emerald-500/40 bg-emerald-500/[0.02]'
          : 'border-border bg-surface'}"
  >
    <div class="flex min-w-0 items-center justify-between gap-3">
      <div class="flex min-w-0 items-center gap-3">
        <ProviderIcon {id} {apiType} size="md" />
        <div class="min-w-0">
          <h3 class="truncate font-semibold text-sm text-text-main group-hover:text-brand-500 transition-colors">
            {name}
          </h3>
          <div class="flex min-w-0 items-center gap-1.5 text-xs flex-wrap mt-0.5">
            {#if isAllDisabled}
              <Badge variant="default" size="sm">
                <span class="flex items-center gap-1">
                  <PauseCircle class="w-3 h-3" />
                  Disabled
                </span>
              </Badge>
            {:else if stats.errorCount > 0}
              <Badge variant="error" size="sm" dot>
                {stats.errorCount} Error
              </Badge>
              {#if stats.connected > 0}
                <Badge variant="success" size="sm" dot>
                  {stats.connected} Connected
                </Badge>
              {/if}
            {:else if noAuth}
              <Badge variant="success" size="sm" dot>Ready</Badge>
            {:else if stats.connected > 0}
              <Badge variant="success" size="sm" dot>
                {stats.connected} Connected
              </Badge>
              {#if apiType}
                <Badge variant="default" size="sm">
                  {apiType === 'responses' ? 'Responses' : 'Chat'}
                </Badge>
              {/if}
            {:else}
              <span class="text-text-muted">No connections</span>
            {/if}
          </div>
        </div>
      </div>

      <div class="flex shrink-0 items-center gap-2">
        {#if stats.total > 0}
          <div
            class={toggleClass}
            onclick={(e) => {
              e.stopPropagation()
              onToggleAll?.(isAllDisabled)
            }}
            onkeydown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.stopPropagation()
                onToggleAll?.(isAllDisabled)
              }
            }}
            role="button"
            tabindex="0"
          >
            <Toggle size="sm" checked={!isAllDisabled} onChange={() => {}} />
          </div>
        {/if}
      </div>
    </div>
  </Card>
</div>