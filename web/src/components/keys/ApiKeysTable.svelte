<!--
  ApiKeysTable — the gateway's client tokens.

  Two changes in issue #224 reshaped this table. The seven per-row controls
  (policy, rotate, reveal, copy, pause/resume, delete) collapsed into one "Menu"
  per row, and a selection checkbox per row added a batch bar, so an operator
  can pause, resume or delete many keys at once.

  Reveal and copy stay as inline controls next to the secret, because those are
  the verbs used constantly and hiding them behind a menu made the common path
  slower. The menu holds the verbs that are occasional or destructive.

  A key's policy modal now also renames the key, so editing a key is one dialog
  rather than a rename field in the table plus a separate policy dialog.
-->
<script lang="ts">
  import { Check, Copy, Eye, EyeOff, Key, Plus, Trash2 } from 'lucide-svelte'
  import { api, type APIKey } from '../../api/client'
  import { notifications } from '../../lib/notifications'
  import { copyToClipboard } from '../../lib/clipboard'
  import Card from '../../lib/ui/Card.svelte'
  import Menu from '../../lib/ui/Menu.svelte'
  import MenuItem from '../../lib/ui/MenuItem.svelte'
  import ApiKeyPolicyModal from './ApiKeyPolicyModal.svelte'
  import {
    batchSummary,
    isBatchBusy,
    runBatch,
    selectAllState,
    toggleAll,
    toggleOne,
  } from './tableActions'

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
  let selectedIds = $state<string[]>([])
  let batchBusy = $state(false)

  const allIds = $derived(apiKeys.map((k) => k.id))
  const allSelected = $derived(selectAllState(selectedIds, allIds) === 'all')

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

  function keyLabel(k: APIKey): string {
    return k.name || k.keyDisplay || k.id
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
      notifications.error('Clipboard access was blocked by the browser.')
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
      notifications.error(err instanceof Error ? err.message : String(err))
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
    // Rotating invalidates the current secret immediately, so it is confirmed
    // rather than fired from a menu click.
    if (!confirm(`Replace the secret for "${keyLabel(k)}"? The current key stops working immediately.`))
      return
    return runExclusive(k.id, async () => {
      await api.rotateApiKey(k.id)
      // The row's displayed secret is stale the moment it is replaced; drop the
      // reveal so the old value cannot linger on screen.
      if (shownKeyIds.has(k.id)) toggleShow(k.id)
      onRefresh?.()
    })
  }

  async function handleDelete(k: APIKey) {
    if (!confirm(`Permanently delete "${keyLabel(k)}"? This cannot be undone.`)) return
    await runExclusive(k.id, async () => {
      await api.deleteApiKey(k.id)
      onRefresh?.()
    })
  }


  // Batch runs issue one request per key: there is no bulk endpoint, and a
  // single rejected key must not roll back the keys that already succeeded.
  async function runBatchAction(verb: string, action: (id: string) => Promise<unknown>) {
    if (batchBusy) return
    batchBusy = true
    try {
      // runBatch works in ids, so the failure list is re-read as labels here
      // rather than handing the table's id-keyed helper a mismatched signature.
      const outcome = await runBatch(selectedIds, action, (id) => {
        const row = apiKeys.find((k) => k.id === id)
        return row ? keyLabel(row) : id
      })
      selectedIds = outcome.failed > 0 ? outcome.failedIds : []
      const summary = batchSummary(verb, outcome)
      if (outcome.failed > 0) notifications.error(summary)
      else notifications.success(summary)
      onRefresh?.()
    } finally {
      batchBusy = false
    }
  }

  function batchDelete() {
    const names = selectedIds
      .map((id) => apiKeys.find((k) => k.id === id))
      .filter((k): k is APIKey => Boolean(k))
      .map(keyLabel)
    if (!confirm(`Delete ${names.length} key(s)? ${names.join(', ')}. This cannot be undone.`)) return
    return runBatchAction('deleted', (id) => api.deleteApiKey(id))
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
    <!-- Batch bar. It replaces nothing: it only appears while something is
         selected, so the unselected table looks the same as before. -->
    {#if selectedIds.length > 0}
      <div class="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-surface-2 px-3 py-2">
        <span class="text-xs text-text-main">{selectedIds.length} selected</span>
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            disabled={isBatchBusy(batchBusy, selectedIds)}
            onclick={() => runBatchAction('enabled', (id) => api.setApiKeyActive(id, true))}
            class="flex items-center gap-1 rounded-lg border border-emerald-500/30 px-2 py-1 text-[11px] text-emerald-600 transition-colors hover:bg-emerald-500/10 disabled:opacity-50 dark:text-emerald-400"
          >
            <span class="material-symbols-outlined text-[14px]">check_circle</span>
            Enable
          </button>
          <button
            type="button"
            disabled={isBatchBusy(batchBusy, selectedIds)}
            onclick={() => runBatchAction('paused', (id) => api.setApiKeyActive(id, false))}
            class="flex items-center gap-1 rounded-lg border border-amber-500/30 px-2 py-1 text-[11px] text-amber-600 transition-colors hover:bg-amber-500/10 disabled:opacity-50 dark:text-amber-400"
          >
            <span class="material-symbols-outlined text-[14px]">pause</span>
            Pause
          </button>
          <button
            type="button"
            disabled={isBatchBusy(batchBusy, selectedIds)}
            onclick={batchDelete}
            class="flex items-center gap-1 rounded-lg border border-red-500/30 px-2 py-1 text-[11px] text-red-500 transition-colors hover:bg-red-500/10 disabled:opacity-50"
          >
            <Trash2 class="w-3 h-3" />
            Delete
          </button>
          <button
            type="button"
            onclick={() => (selectedIds = [])}
            class="rounded-lg px-2 py-1 text-[11px] text-text-muted transition-colors hover:bg-surface-3 hover:text-text-main"
          >
            Clear
          </button>
        </div>
      </div>
    {/if}

    <!--
      Below `md` this renders as cards, not a table. A row is five columns wide
      by construction, so at a phone width (390px, ~292px of card width) the
      table's scroll box held 500px of content: the policy and actions columns
      sat outside the viewport with no way to reach them, and the secret cell
      broke one character per line. The card stacks the same facts in the width
      a phone actually has.

      The pieces both layouts render are snippets, so a card cannot drift from
      its table row.
    -->
    {#snippet secretCell(k: APIKey)}
      {@const shown = shownKeyIds.has(k.id)}
      <div class="flex items-center gap-1 min-w-0">
        <code
          class="font-mono text-[11px] text-text-muted bg-surface-2 px-2 py-1 rounded border border-border/50 select-all truncate"
        >
          {shown ? renderedKey(k) : maskSecret(renderedKey(k))}
        </code>

        {#if isRevealable(k)}
          <button
            type="button"
            onclick={() => toggleShow(k.id)}
            class="shrink-0 p-1.5 rounded hover:bg-surface-2 text-text-muted hover:text-text-main transition-colors cursor-pointer"
            aria-label={shown ? `Hide key ${k.name || ''}` : `Show key ${k.name || ''}`}
            title="Hide key"
          >
            {#if shown}
              <EyeOff class="w-3.5 h-3.5" />
            {:else}
              <Eye class="w-3.5 h-3.5" />
            {/if}
          </button>
        {/if}

        <button
          type="button"
          onclick={() => copy(renderedKey(k), k.id)}
          class="shrink-0 p-1.5 rounded hover:bg-surface-2 text-text-muted hover:text-brand-500 transition-colors cursor-pointer"
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
    {/snippet}

    {#snippet keyActions(k: APIKey, fullWidth = false)}
      {@const active = isActive(k)}
      <Menu
        label={`Actions for ${keyLabel(k)}`}
        triggerIcon="more_horiz"
        hideLabel={!fullWidth}
        {fullWidth}
        minWidth="13rem"
      >
        <MenuItem label="Edit key &amp; policy" icon="tune" onSelect={() => (policyKey = k)} />
        <MenuItem
          label="Regenerate secret"
          icon="autorenew"
          disabled={busyId !== null}
          onSelect={() => handleRotate(k)}
        />

        <div class="my-1 border-t border-border-subtle" role="separator"></div>

        <MenuItem
          label={active ? 'Pause key' : 'Resume key'}
          icon={active ? 'pause' : 'play_arrow'}
          disabled={busyId !== null}
          onSelect={() => handleToggle(k)}
        />

        <div class="my-1 border-t border-border-subtle" role="separator"></div>

        <MenuItem
          label="Delete key"
          icon="delete"
          danger
          disabled={busyId !== null}
          onSelect={() => handleDelete(k)}
        />
      </Menu>
    {/snippet}

    <!-- Issue #199 collapses status, policy and created date into one
         multi-line cell. KeiRouter keeps plan/access as a single
         middot-separated line (Keys.tsx:206-212); the stacked layout here is
         the requested divergence, with the same rhythm: content line first,
         muted `Created …` beneath. -->
    {#snippet policyCell(k: APIKey)}
      {@const badges = policyBadges(k)}
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
    {/snippet}

    <!-- Mobile: one card per key. -->
    <ul class="md:hidden space-y-3">
      {#each apiKeys as k (k.id)}
        {@const active = isActive(k)}
        <li class="rounded-xl border border-border bg-surface-2/40 p-3 space-y-2.5 {active
          ? ''
          : 'opacity-70'}">
          <div class="flex items-center gap-2 min-w-0">
            <input
              type="checkbox"
              checked={selectedIds.includes(k.id)}
              aria-label={`Select ${keyLabel(k)}`}
              onchange={() => (selectedIds = toggleOne(selectedIds, k.id))}
              class="mt-1 size-3.5 shrink-0 rounded border-border/60 accent-[var(--primary)] cursor-pointer"
            />
            <p class="text-sm font-semibold text-text-main truncate">
              {k.name || 'Client Token'}
            </p>
            <!-- KeiRouter's StatusPill: a bare dot plus a label, not a
                 filled badge. A paused key reads at a glance without
                 spending a column on it. -->
            <span
              class="inline-flex shrink-0 items-center gap-1.5 text-[11px] font-medium {active
                ? 'text-success'
                : 'text-text-muted'}"
            >
              <span class="w-1.5 h-1.5 rounded-full {active ? 'bg-success' : 'bg-danger opacity-60'}"></span>
              {active ? 'Active' : 'Paused'}
            </span>
          </div>

          {@render secretCell(k)}
          {@render policyCell(k)}
          {@render keyActions(k, true)}
        </li>
      {/each}
    </ul>

    <!-- Desktop: the table. -->
    <div class="hidden md:block overflow-x-auto">
      <table class="w-full text-left text-xs">
        <thead>
          <tr class="border-b border-border text-text-subtle font-code uppercase text-[10px] tracking-wider">
            <th class="w-8 py-2.5 pl-3">
              <input
                type="checkbox"
                checked={allSelected}
                aria-label="Select all keys"
                onchange={() => (selectedIds = toggleAll(selectedIds, allIds))}
                class="size-3.5 rounded border-border/60 accent-[var(--primary)] cursor-pointer"
              />
            </th>
            <th class="py-2.5 px-3">Key</th>
            <th class="py-2.5 px-3">Token</th>
            <th class="py-2.5 px-3">Policy &amp; created</th>
            <th class="py-2.5 px-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/40">
          {#each apiKeys as k (k.id)}
            {@const active = isActive(k)}
            <tr class="align-top transition hover:bg-surface-2/40 {active ? '' : 'opacity-70'}">
              <td class="py-3 pl-3">
                <input
                  type="checkbox"
                  checked={selectedIds.includes(k.id)}
                  aria-label={`Select ${keyLabel(k)}`}
                  onchange={() => (selectedIds = toggleOne(selectedIds, k.id))}
                  class="mt-1 size-3.5 rounded border-border/60 accent-[var(--primary)] cursor-pointer"
                />
              </td>

              <td class="py-3 px-3">
                <div class="flex items-center gap-2 min-w-0">
                  <p class="text-sm font-semibold text-text-main truncate max-w-[150px]">
                    {k.name || 'Client Token'}
                  </p>
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
                {@render secretCell(k)}
              </td>

              <td class="py-3 px-3">
                {@render policyCell(k)}
              </td>

              <td class="py-3 px-3">
                <div class="flex items-center justify-end">
                  {@render keyActions(k)}
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
  <ApiKeyPolicyModal
    apiKey={policyKey}
    onClose={() => (policyKey = null)}
    onSaved={() => {
      // The rename lives in the same dialog, so the list is stale until the
      // parent re-reads it.
      policyKey = null
      onRefresh?.()
    }}
  />
{/if}