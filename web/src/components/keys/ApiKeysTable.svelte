<script lang="ts">
  import { Check, Copy, Eye, EyeOff, Key, Plus, RefreshCw, Shield, ToggleLeft, ToggleRight, Trash2 } from 'lucide-svelte'
  import { api, type APIKey } from '../../api/client'
  import { copyToClipboard } from '../../lib/clipboard'
  import Card from '../../lib/ui/Card.svelte'
  import ApiKeyPolicyModal from './ApiKeyPolicyModal.svelte'

  interface Props {
    apiKeys?: APIKey[]
    onRefresh?: () => void
    onCreate?: () => void
  }

  let { apiKeys = [], onRefresh, onCreate }: Props = $props()

  let shownKeyIds = $state<Set<string>>(new Set())
  let copiedId = $state<string | null>(null)
  let policyKey = $state<APIKey | null>(null)
  let busyId = $state<string | null>(null)

  // The full secret, when the server had one to give. Empty for a caller
  // authenticated with an engine client key, and for rows minted before #199
  // whose plaintext was hashed away — both fall back to keyDisplay, and the
  // reveal button is hidden rather than shown disabled, because there is
  // nothing behind it.
  function secretOf(k: APIKey): string {
    return k.key || ''
  }

  function isRevealable(k: APIKey): boolean {
    return secretOf(k) !== ''
  }

  function renderedKey(k: APIKey): string {
    if (shownKeyIds.has(k.id)) return secretOf(k) || k.keyDisplay || ''
    return k.keyDisplay || secretOf(k)
  }

  function maskSecret(value: string): string {
    if (!value) return ''
    if (value.length <= 10) return value
    return value.slice(0, 6) + '••••••' + value.slice(-4)
  }

  function toggleShow(id: string) {
    const next = new Set(shownKeyIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    shownKeyIds = next
  }

  async function copy(text: string, id: string) {
    const ok = await copyToClipboard(text)
    if (!ok) {
      alert('Clipboard access was blocked by the browser.')
      return
    }
    copiedId = id
    setTimeout(() => {
      if (copiedId === id) copiedId = null
    }, 2000)
  }

  function isActive(k: APIKey): boolean {
    return k.isActive === 1
  }

  function isExpired(k: APIKey): boolean {
    if (!k.expiresAt) return false
    const parsed = new Date(k.expiresAt)
    return !Number.isNaN(parsed.getTime()) && parsed.getTime() < Date.now()
  }

  // Status, policy and creation date share one multi-line cell rather than
  // three columns: an operator reads them as facts about the same key, and the
  // separate columns made the table wider than the row content justified.
  function policyBadges(k: APIKey): { label: string; tone: 'info' | 'danger' }[] {
    const badges: { label: string; tone: 'info' | 'danger' }[] = []
    if ((k.rateLimitRpm ?? 0) > 0) badges.push({ label: `${k.rateLimitRpm}/min`, tone: 'info' })
    if ((k.rateLimitTpm ?? 0) > 0) {
      badges.push({ label: `${(k.rateLimitTpm ?? 0).toLocaleString()} tok/min`, tone: 'info' })
    }
    if ((k.rateLimitConcurrency ?? 0) > 0) {
      badges.push({ label: `${(k.rateLimitConcurrency ?? 0).toLocaleString()} conc`, tone: 'info' })
    }
    if ((k.expiresAt ?? '') !== '') {
      badges.push({ label: isExpired(k) ? 'expired' : 'expires', tone: isExpired(k) ? 'danger' : 'info' })
    }
    return badges
  }

  async function runExclusive(id: string, work: () => Promise<void>) {
    if (busyId !== null) return
    busyId = id
    try {
      await work()
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err))
    } finally {
      busyId = null
    }
  }

  function handleToggle(k: APIKey) {
    return runExclusive(k.id, async () => {
      await api.setApiKeyActive(k.id, !isActive(k))
      onRefresh?.()
    })
  }

  function handleRotate(k: APIKey) {
    if (!confirm(`Replace the secret for "${k.name || 'this key'}"? The current key stops working immediately.`)) return
    return runExclusive(k.id, async () => {
      await api.rotateApiKey(k.id)
      // The row's displayed secret is stale the moment it is replaced; drop the
      // reveal so the old value cannot linger on screen.
      if (shownKeyIds.has(k.id)) toggleShow(k.id)
      onRefresh?.()
    })
  }

  function handleDelete(k: APIKey) {
    if (!confirm(`Permanently delete "${k.name || 'this key'}"? This cannot be undone.`)) return
    return runExclusive(k.id, async () => {
      await api.deleteApiKey(k.id)
      onRefresh?.()
    })
  }
</script>

<Card padding="md" class="space-y-4">
  <div class="flex items-center justify-between gap-4">
    <div class="flex items-center gap-2">
      <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
        <Key class="w-5 h-5" />
      </div>
      <div>
        <h2 class="text-lg font-semibold text-text-main">API Keys</h2>
        <p class="text-xs text-text-muted">Manage Bearer tokens for clients connecting to this endpoint</p>
      </div>
    </div>

    <button
      type="button"
      onclick={() => onCreate?.()}
      class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs shadow-md shadow-brand-500/20 transition cursor-pointer"
    >
      <Plus class="w-4 h-4" />
      <span>Create Key</span>
    </button>
  </div>

  {#if apiKeys.length === 0}
    <div class="text-center py-12 space-y-3">
      <div class="inline-flex items-center justify-center w-14 h-14 rounded-full bg-brand-500/10 text-brand-500">
        <Key class="w-7 h-7" />
      </div>
      <div class="space-y-1">
        <p class="text-text-main font-medium text-sm">No API keys yet</p>
        <p class="text-xs text-text-muted">Create your first API key to get started</p>
      </div>
    </div>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-left text-xs">
        <thead>
          <tr class="border-b border-border text-text-subtle font-code uppercase text-[10px] tracking-wider">
            <th class="py-2.5 px-3">Key</th>
            <th class="py-2.5 px-3">Token</th>
            <th class="py-2.5 px-3">Policy &amp; created</th>
            <th class="py-2.5 px-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/40">
          {#each apiKeys as k (k.id)}
            {@const active = isActive(k)}
            {@const revealable = isRevealable(k)}
            {@const badges = policyBadges(k)}
            <tr class="align-top transition hover:bg-surface-2/40 {active ? '' : 'opacity-70'}">
              <td class="py-3 px-3">
                <div class="flex items-center gap-2 min-w-0">
                  <p class="text-sm font-semibold text-text-main truncate max-w-[150px]">
                    {k.name || 'Client Token'}
                  </p>
                  <!-- KeiRouter's StatusPill: a bare dot plus a label, not a
                       filled badge. A paused key reads at a glance without
                       spending a column on it. -->
                  <span
                    class="inline-flex items-center gap-1.5 text-[11px] font-medium {active
                      ? 'text-success'
                      : 'text-text-muted'}"
                  >
                    <span class="w-1.5 h-1.5 rounded-full {active ? 'bg-success' : 'bg-danger opacity-60'}"></span>
                    {active ? 'Active' : 'Paused'}
                  </span>
                </div>
              </td>

              <td class="py-3 px-3">
                <div class="flex items-center gap-1">
                  <code
                    class="font-mono text-[11px] text-text-muted bg-surface-2 px-2 py-1 rounded border border-border/50 select-all break-all"
                  >
                    {shownKeyIds.has(k.id) ? renderedKey(k) : maskSecret(renderedKey(k))}
                  </code>

                  {#if revealable}
                    <button
                      type="button"
                      onclick={() => toggleShow(k.id)}
                      class="p-1.5 rounded hover:bg-surface-2 text-text-muted hover:text-text-main transition-colors cursor-pointer"
                      aria-label={shownKeyIds.has(k.id) ? `Hide key ${k.name || ''}` : `Show key ${k.name || ''}`}
                      title={shownKeyIds.has(k.id) ? 'Hide key' : 'Show key'}
                    >
                      {#if shownKeyIds.has(k.id)}
                        <EyeOff class="w-3.5 h-3.5" />
                      {:else}
                        <Eye class="w-3.5 h-3.5" />
                      {/if}
                    </button>
                  {/if}

                  <button
                    type="button"
                    onclick={() => copy(renderedKey(k), k.id)}
                    class="p-1.5 rounded hover:bg-surface-2 text-text-muted hover:text-brand-500 transition-colors cursor-pointer"
                    aria-label={`Copy key ${k.name || ''}`}
                    title="Copy key"
                  >
                    {#if copiedId === k.id}
                      <Check class="w-3.5 h-3.5 text-success" />
                    {:else}
                      <Copy class="w-3.5 h-3.5" />
                    {/if}
                  </button>
                </div>
              </td>

              <!-- Issue #199 collapses status, policy and created date into one
                   multi-line cell. KeiRouter keeps plan/access as a single
                   middot-separated line (Keys.tsx:206-212); the stacked layout
                   here is the requested divergence, with the same rhythm:
                   content line first, muted `Created …` beneath. -->
              <td class="py-3 px-3">
                <div class="flex flex-wrap items-center gap-1">
                  {#if badges.length === 0}
                    <span class="text-text-subtle text-[11px]">unrestricted</span>
                  {:else}
                    {#each badges as badge (badge.label)}
                      <span
                        class="px-1.5 py-0.5 rounded text-[10px] border {badge.tone === 'danger'
                          ? 'bg-danger/10 text-danger border-danger/20'
                          : 'bg-info/10 text-info border-info/20'}"
                      >
                        {badge.label}
                      </span>
                    {/each}
                  {/if}
                </div>
                <p class="mt-0.5 text-[11px] text-text-subtle">
                  Created {k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '—'}
                  {#if (k.usedCount ?? 0) > 0}
                    · {(k.usedCount ?? 0).toLocaleString()} req
                  {/if}
                </p>
              </td>

              <td class="py-3 px-3">
                <div class="flex items-center justify-end gap-1.5">
                  <button
                    type="button"
                    onclick={() => (policyKey = k)}
                    class="p-1.5 rounded-lg border border-border bg-surface-2 text-text-subtle hover:text-text-main transition cursor-pointer"
                    title="Configure rate limits, expiry, model access"
                  >
                    <Shield class="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onclick={() => handleRotate(k)}
                    disabled={busyId !== null}
                    class="p-1.5 rounded-lg border border-border bg-surface-2 text-text-subtle hover:text-text-main transition cursor-pointer disabled:opacity-50"
                    title="Replace this key's secret, keeping its policy and usage history"
                  >
                    <RefreshCw class="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onclick={() => handleToggle(k)}
                    disabled={busyId !== null}
                    aria-label={active ? 'Pause key' : 'Resume key'}
                    class="p-1.5 rounded-lg border transition cursor-pointer disabled:opacity-50 {active
                      ? 'bg-success/10 border-success/20 text-success'
                      : 'bg-surface-2 border-border text-text-subtle hover:text-text-main'}"
                    title={active ? 'Pause key' : 'Resume key'}
                  >
                    {#if active}
                      <ToggleRight class="w-3.5 h-3.5" />
                    {:else}
                      <ToggleLeft class="w-3.5 h-3.5" />
                    {/if}
                  </button>
                  <button
                    type="button"
                    onclick={() => handleDelete(k)}
                    disabled={busyId !== null}
                    class="p-1.5 rounded-lg text-text-subtle hover:bg-danger/10 hover:text-danger transition cursor-pointer disabled:opacity-50"
                    title="Delete key"
                  >
                    <Trash2 class="w-3.5 h-3.5" />
                  </button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>

{#if policyKey}
  <ApiKeyPolicyModal apiKey={policyKey} onClose={() => (policyKey = null)} onSaved={() => onRefresh?.()} />
{/if}