<script lang="ts">
  import { copyToClipboard } from '../../lib/clipboard'
  import { api } from '../../api/client'

  interface Props {
    isOpen?: boolean
    providerId: string
    providerName: string
    onClose: () => void
  }

  let {
    isOpen = false,
    providerId,
    providerName,
    onClose,
  }: Props = $props()

  let keys = $state<Array<{ id: string; name: string; authType: string; key: string }>>([])
  let loading = $state(false)
  let copiedId = $state<string | null>(null)

  $effect(() => {
    if (!isOpen) return
    let cancelled = false
    loading = true
    copiedId = null
    api.exportProviderKeys(providerId)
      .then((res) => {
        if (!cancelled) keys = res?.connections || []
      })
      .catch(() => {
        if (!cancelled) keys = []
      })
      .finally(() => {
        if (!cancelled) loading = false
      })
    return () => {
      cancelled = true
    }
  })

  async function copy(item: { id: string; key: string }) {
    await copyToClipboard(item.key)
    copiedId = item.id
    setTimeout(() => {
      if (copiedId === item.id) copiedId = null
    }, 1500)
  }

  async function copyAll() {
    const text = keys.filter((k) => k.key).map((k) => `${k.name}: ${k.key}`).join('\n')
    await copyToClipboard(text)
    copiedId = '__all__'
    setTimeout(() => {
      if (copiedId === '__all__') copiedId = null
    }, 1500)
  }

  const withKeys = $derived(keys.filter((k) => k.key))
</script>

{#if isOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="fixed inset-0 bg-black/40 backdrop-blur-sm"
      role="button"
      tabindex="-1"
      aria-label="Close modal"
      onclick={onClose}
      onkeydown={(e) => {
        if (e.key === 'Escape') onClose()
      }}
    ></div>
    <div class="relative w-full max-w-lg rounded-2xl border border-border bg-surface p-6 shadow-xl max-h-[85vh] flex flex-col">
      <div class="flex items-center justify-between pb-4 border-b border-border">
        <h3 class="text-base font-semibold text-text-main">
          Export Keys — {providerName || providerId}
        </h3>
        <button
          type="button"
          onclick={onClose}
          class="rounded-lg p-1 text-text-muted hover:bg-surface-2 hover:text-text-main"
        >
          <span class="material-symbols-outlined text-[20px]">close</span>
        </button>
      </div>

      <div class="flex-1 overflow-y-auto py-4 flex flex-col gap-3 min-h-0">
        {#if loading}
          <p class="text-sm text-text-muted">Loading…</p>
        {:else if withKeys.length === 0}
          <p class="text-sm text-text-muted">No keys stored for this provider.</p>
        {:else}
          {#each withKeys as item (item.id)}
            <div class="flex items-center justify-between gap-3 rounded-lg border border-border-subtle bg-surface-2 px-3 py-2">
              <div class="min-w-0 flex-1">
                <p class="truncate text-xs text-text-muted">{item.name}</p>
                <p class="break-all font-mono text-sm text-text-main">{item.key}</p>
              </div>
              <button
                type="button"
                onclick={() => copy(item)}
                title="Copy key"
                class="shrink-0 rounded p-1.5 hover:bg-black/5 dark:hover:bg-white/5 {copiedId === item.id ? 'text-green-500' : 'text-text-muted hover:text-brand-500'}"
              >
                <span class="material-symbols-outlined text-[18px]">
                  {copiedId === item.id ? 'check' : 'content_copy'}
                </span>
              </button>
            </div>
          {/each}
        {/if}
      </div>

      {#if !loading && withKeys.length > 0}
        <div class="flex justify-between items-center pt-4 border-t border-border">
          <button
            type="button"
            onclick={copyAll}
            class="inline-flex items-center gap-1.5 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">
              {copiedId === '__all__' ? 'check' : 'content_copy'}
            </span>
            {copiedId === '__all__' ? 'Copied' : 'Copy All'}
          </button>
          <button
            type="button"
            onclick={onClose}
            class="inline-flex items-center gap-1.5 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-8 px-4 text-xs rounded-[8px]"
          >
            Close
          </button>
        </div>
      {/if}
    </div>
  </div>
{/if}
