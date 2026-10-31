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
  import { onDestroy } from 'svelte'
  import { Check, ChevronDown, Copy, Eye, EyeOff, Key, Plus, RotateCcw, RotateCw, Search, Trash2 } from 'lucide-svelte'
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
    toggleOne,
  } from './tableActions'

  interface Props {
    apiKeys?: APIKey[]
    onRefresh?: () => void
    onCreate?: () => void
  }

  let { apiKeys = [], onRefresh, onCreate }: Props = $props()

  let shownKeyIds = $state<Set<string>>(new Set())
  // Which cards are open. Empty by default: a list of keys is read as names and
  // status, and the secret, policy and usage are detail for the key being
  // worked on. An operator opening one key does not open the other eleven.
  let expandedIds = $state<Set<string>>(new Set())
  let copiedId = $state<string | null>(null)
  let policyKey = $state<APIKey | null>(null)
  let busyId = $state<string | null>(null)
  let selectedIds = $state<string[]>([])
  let batchBusy = $state(false)
  // Search, filter and sort. All three are client-side over the list this
  // component was already handed: a key list is small (tens, not thousands), so
  // a round trip per keystroke would cost more than the filtering does. The
  // derived list below is what both layouts render, so the table and the cards
  // can never disagree about which keys are showing.
  let searchTerm = $state('')
  // Each pill cycles three phases, which is what makes a single tap target
  // enough for both directions of a comparison:
  //
  //   off      — no filter, the list is everything
  //   forward  — the natural reading order (most requests first, active before
  //              paused, limited before unrestricted)
  //   reverse  — the same set, the other way round
  //
  // The third phase matters because "show me the busiest" and "show me the
  // idle ones" are the same question asked twice, and a two-state toggle would
  // need a second control to ask it.
  type PillPhase = 'off' | 'forward' | 'reverse'
  let activePill = $state<PillPhase>('off')
  let requestsPill = $state<PillPhase>('off')
  let tokensPill = $state<PillPhase>('off')
  let unusedPill = $state<PillPhase>('off')
  // Sort direction is derived from the pill rather than held separately, so the
  // two can never disagree: pressing Requests twice must reverse the list, and
  // there is no state in which the pill says "reverse" while the list is
  // forward.
  let sortKey = $state<'newest' | 'name' | 'requests'>('newest')
  const sortDesc = $derived(
    requestsPill === 'forward' || tokensPill === 'forward'
      ? true
      : requestsPill === 'reverse' || tokensPill === 'reverse'
        ? false
        : true,
  )
  // off -> forward -> reverse -> off. A single helper so all four pills advance
  // identically; a pill that skipped a phase would be a bug nobody could see
  // from the UI.
  function cycle(current: PillPhase): PillPhase {
    if (current === 'off') return 'forward'
    if (current === 'forward') return 'reverse'
    return 'off'
  }
  // The list after search, filter and sort. `apiKeys` stays untouched so the
  // metrics ribbon and the select-all control still describe every key the
  // operator has, not just the ones currently visible — otherwise filtering to
  // "paused" would make the ribbon claim there are no active keys.
  const visibleKeys = $derived.by(() => {
    const term = searchTerm.trim().toLowerCase()
    let list = apiKeys.filter((k) => {
      if (term) {
        const haystack = `${k.name || ''} ${k.keyDisplay || ''}`.toLowerCase()
        if (!haystack.includes(term)) return false
      }
      // Active: forward shows live keys, reverse shows paused ones. Both phases
      // narrow, because "which of my keys is dead" is as real a question as
      // "which are serving".
      if (activePill === 'forward' && !isActive(k)) return false
      if (activePill === 'reverse' && isActive(k)) return false
      // Tokens: forward is the heaviest spenders, reverse the lightest. A
      // personal operator reads this pill as "which key is eating my quota",
      // and the answer spans orders of magnitude on a real deployment — one key
      // idle, another at two billion tokens. Unused is the other end of the
      // same question: a credential that has never been spent is either a
      // spare or a key left behind, and both are worth seeing on their own.
      if (tokensPill === 'forward' && (k.usedTokens ?? 0) <= 0) return false
      if (tokensPill === 'reverse' && (k.usedTokens ?? 0) > 0) return false
      if (unusedPill === 'forward' && (k.usedCount ?? 0) > 0) return false
      if (unusedPill === 'reverse' && (k.usedCount ?? 0) === 0) return false
      return true
    })
    list = [...list]
    // The Requests pill owns the ordering while it is engaged; the select takes
    // over the moment the pill is off. That way pressing Requests is a sort, not
    // just a filter, and releasing it hands the list back to whatever the select
    // was already set to.
    // Requests and Tokens both order by a spend figure, so they share one
    // branch: whichever is engaged owns the comparator. Releasing both hands
    // ordering back to the select, so neither pill becomes a sort the operator
    // cannot get out of.
    if (requestsPill !== 'off' || tokensPill !== 'off') {
      const dir = sortDesc ? -1 : 1
      const spend = requestsPill !== 'off'
        ? (k: APIKey) => k.usedCount ?? 0
        : (k: APIKey) => k.usedTokens ?? 0
      list.sort((a, b) => (spend(a) - spend(b)) * dir)
    } else {
      switch (sortKey) {
        case 'name':
          list.sort((a, b) => keyLabel(a).localeCompare(keyLabel(b)))
          break
        case 'requests':
          list.sort((a, b) => (b.usedCount ?? 0) - (a.usedCount ?? 0))
          break
        default:
          // Newest first. A missing or unparseable date sorts last rather than
          // first: an unknown age is not "most recent".
          list.sort((a, b) => dateValue(b) - dateValue(a))
      }
    }
    return list
  })
  // Whether any control is narrowing or reordering the list, so the empty state
  // can say "no match" instead of "you have no keys" — the two need different
  // actions.
  const isFiltered = $derived(
    searchTerm.trim() !== '' ||
      activePill !== 'off' ||
      requestsPill !== 'off' ||
      tokensPill !== 'off' ||
      unusedPill !== 'off',
  )


  // The width of the widest token chip, so every row's chip is the same size.
  //
  // Without this, revealing a key makes its row's chip wider than the others —
  // the plaintext is longer than the mask — and the eye and copy buttons slide
  // right as the text grows. An operator reaching for copy on the row below
  // hits it in a different place than the row above, and the column stops
  // reading as one thing.
  //
  // The width is measured from the longest value any row can show, which is the
  // plaintext rather than the mask: a chip sized to the mask would still jump
  // the first time anything is revealed. The two buttons and the chip's own
  // padding are added because they occupy the same box as the text.
  const tokenChipWidth = $derived.by(() => {
    let longest = 0
    for (const k of visibleKeys) {
      const raw = secretOf(k) || k.keyDisplay || ''
      if (raw.length > longest) longest = raw.length
    }
    if (longest === 0) return undefined
    // 11px mono at ~6.6px per character, plus the two 22px buttons, the gaps
    // between the three items, and the chip's horizontal padding and border.
    const textW = longest * 6.6
    const chromeW = 22 * 2 + 1.5 * 2 + 10 + 1 + 4
    return `${Math.ceil(textW + chromeW)}px`
  })
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

  // What the cell renders. Revealed, it is the plaintext; hidden, it is the
  // row's own mask, so a hidden cell never renders a usable credential.
  function renderedKey(k: APIKey): string {
    if (shownKeyIds.has(k.id)) return secretOf(k) || k.keyDisplay || ''
    return k.keyDisplay || secretOf(k)
  }

  // Issue #310: Copy used to take `renderedKey`, which resolves to the masked
  // `keyDisplay` for every row that was not already revealed — so copying a key
  // straight off a freshly loaded dashboard put `sk-c07…c93e` on the clipboard
  // and the pasted credential 401'd. Copying a secret must not depend on whether
  // it happened to be on screen, so it reads the plaintext and ignores the
  // reveal state.
  //
  // A row with no plaintext (an engine-key caller, or a pre-#199 row whose
  // secret was hashed away) has nothing to copy. Falling back to the mask would
  // put an unusable string on the clipboard silently, so it is reported instead.
  async function handleCopy(k: APIKey) {
    const secret = secretOf(k)
    if (!secret) {
      notifications.error(
        'The full key for this entry is not stored, so it cannot be copied. Regenerate it to get a usable secret.',
      )
      return
    }
    await copy(secret, k.id)
  }

  // The mask keeps the secret's exact length.
  //
  // It used to collapse everything between the first six and last four
  // characters into a fixed run of six bullets, which told the reader two
  // things it had no business knowing: that the secret was longer than ten
  // characters, and roughly how much longer. A mask whose length tracks the
  // thing it hides is not a mask — it is a length oracle.
  //
  // Keeping the count also keeps the row from reflowing: a revealed secret and
  // its mask occupy the same width, so the chip neither grows nor shrinks and
  // the buttons beside it stay put.
  //
  // The first and last few characters stay visible because that is how an
  // operator tells one key from another in a list without revealing anything —
  // `sk-4ff…` against `sk-fa5…` is enough to recognise the row they want.
  function maskSecret(value: string): string {
    if (!value) return ''
    if (value.length <= 10) return value
    const head = value.slice(0, 6)
    const tail = value.slice(-4)
    const middle = value.length - head.length - tail.length
    return head + '•'.repeat(middle) + tail
  }

  function keyLabel(k: APIKey): string {
    return k.name || k.keyDisplay || k.id
  }

  // Revealing a secret is temporary. An operator reveals a key to read it or
  // paste it, then walks away from the screen — and a plaintext left on a
  // dashboard is the failure mode this whole mask exists to prevent. So the
  // reveal closes itself after REVEAL_MS unless the operator hides it sooner.
  //
  // The timer is per key and replaced on each reveal, so revealing the same key
  // twice does not leave a stale timer that hides it mid-read.
  const REVEAL_MS = 5000
  let revealTimers = new Map<string, ReturnType<typeof setTimeout>>()
  function toggleShow(id: string) {
    const next = new Set(shownKeyIds)
    if (next.has(id)) {
      next.delete(id)
      clearRevealTimer(id)
    } else {
      next.add(id)
      clearRevealTimer(id)
      revealTimers.set(
        id,
        setTimeout(() => {
          const after = new Set(shownKeyIds)
          after.delete(id)
          shownKeyIds = after
          revealTimers.delete(id)
        }, REVEAL_MS),
      )
    }
    shownKeyIds = next
  }
  function clearRevealTimer(id: string) {
    const existing = revealTimers.get(id)
    if (existing !== undefined) {
      clearTimeout(existing)
      revealTimers.delete(id)
    }
  }
  // A reveal timer must not outlive the component: navigating away with a key
  // revealed would otherwise fire into an unmounted tree.
  onDestroy(() => {
    for (const timer of revealTimers.values()) clearTimeout(timer)
    revealTimers.clear()
  })
  function toggleExpanded(id: string) {
    const next = new Set(expandedIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    expandedIds = next
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

  // A sortable age for a key. An unparseable or missing date becomes 0 so it
  // sorts to the bottom of "newest first" instead of the top — an unknown age
  // is not the most recent thing.
  function dateValue(k: APIKey): number {
    if (!k.createdAt) return 0
    const t = new Date(k.createdAt).getTime()
    return Number.isNaN(t) ? 0 : t
  }
  function isActive(k: APIKey): boolean {
    return k.isActive === 1
  }

  function isExpired(k: APIKey): boolean {
    if (!k.expiresAt) return false
    const parsed = new Date(k.expiresAt)
    return !Number.isNaN(parsed.getTime()) && parsed.getTime() < Date.now()
  }

  // Tokens are shown compactly: a budget is read as a scale ("12.3k of 250k"),
  // not as a seven-digit number the eye has to parse.
  function compactTokens(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
    return String(n)
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
    // The spent side travels with the ceiling (#365): a badge naming only the
    // limit leaves an operator unable to tell a key at 2% of its budget from one
    // that is already refusing traffic, and exhausted reads as danger.
    if ((k.tokenQuota ?? 0) > 0) {
      const quota = k.tokenQuota ?? 0
      const used = k.usedTokens ?? 0
      badges.push({
        label: `${compactTokens(used)} / ${compactTokens(quota)} tok`,
        tone: used >= quota ? 'danger' : 'info',
      })
    }
    if ((k.expiresAt ?? '') !== '') {
      badges.push({ label: isExpired(k) ? 'expired' : 'expires', tone: isExpired(k) ? 'danger' : 'info' })
    }
    return badges
  }

  // Clearing a spent budget is the recoverable direction — the usage history
  // stays, the key just stops being charged for it — so it runs without a
  // confirmation, unlike a rotate or a delete.
  function handleResetQuota(k: APIKey) {
    return runExclusive(k.id, async () => {
      const res = await api.resetApiKeyQuota(k.id)
      notifications.success(
        `Token budget reset for "${keyLabel(k)}": ${compactTokens(res.usedTokens)} of ${compactTokens(res.tokenQuota)} spent.`,
      )
      onRefresh?.()
    })
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
  <div class="flex items-center gap-2">
    <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
      <Key class="w-5 h-5" />
    </div>
    <div>
      <h2 class="text-lg font-semibold text-text-main">API Keys</h2>
      <p class="text-xs text-text-muted">Manage Bearer tokens for clients connecting to this endpoint</p>
    </div>
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
      <!-- The action the empty state invites. It used to live only in the
           populated branch, so an operator with no keys read "create your
           first API key" with no button to do it from — the ribbon, the search
           row and Create Key are all inside the {:else} below. -->
      <button
        type="button"
        onclick={() => onCreate?.()}
        aria-label="Create Key"
        class="inline-flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white text-xs font-semibold transition cursor-pointer"
      >
        <Plus class="w-3.5 h-3.5" />
        <span>Create Key</span>
      </button>
    </div>
  {:else}

    <!-- Metrics ribbon, and on a phone it doubles as the filter control.

         Each pill is a count an operator would otherwise get by reading the
         table and adding up rows. The four questions they answer are the ones
         that actually come up on a personal deployment: is it live, is it busy,
         what is it costing, and is anything sitting idle. Tokens and Unused
         replaced Expiring and Expired, and all four replaced Limited and
         Unrestricted — which always read 0 and 4 here, because a pill that
         never changes tells you nothing.
         Every pill cycles three phases: off, then the natural order, then the
         reverse. That makes one tap target enough for both directions of a
         comparison — "busiest" and "idlest" are the same question asked twice.

         Two lines per pill: label above, value below. Side by side they were
         two short strings fighting for a 216px-wide phone card; stacked, the
         value gets the emphasis it deserves.
         Every count is a total over the whole list, never over the filtered
         view: the ribbon is the operator's fixed frame of reference while they
         narrow the table, so filtering to "paused" must not make Active
         read 0. -->
    {@const activeCount = apiKeys.filter((k) => isActive(k)).length}
    {@const totalReqs = apiKeys.reduce((sum, k) => sum + (k.usedCount ?? 0), 0)}
    {@const totalTokens = apiKeys.reduce((sum, k) => sum + (k.usedTokens ?? 0), 0)}
    {@const unusedCount = apiKeys.filter((k) => (k.usedCount ?? 0) === 0).length}
    <div class="grid grid-cols-2 sm:grid-cols-4 gap-2">
      <!-- Active: filters to live keys on a phone. -->
      <button
        type="button"
        onclick={() => (activePill = cycle(activePill))}
        aria-pressed={activePill !== 'off'}
        class="rounded-lg border px-3 py-2 text-left transition-colors cursor-pointer {activePill !== 'off'
          ? 'border-brand-500/50 bg-brand-500/10'
          : 'border-border bg-surface-2/60 hover:bg-surface-2'}"
      >
        <span class="block text-[11px] text-text-muted">Active</span>
        <span class="mt-0.5 flex items-center gap-1.5">
          <span class="w-1.5 h-1.5 rounded-full bg-success"></span>
          <span class="text-sm font-semibold font-mono text-text-main">{activeCount}</span>
        </span>
      </button>
      <!-- Requests: a count that is also the sort control. Pressing it orders by
           traffic — most first, then least, then hands ordering back to the
           select. The number stays visible in every phase because it is a total
           for the whole list, not for the current filter. -->
      <button
        type="button"
        onclick={() => (requestsPill = cycle(requestsPill))}
        aria-pressed={requestsPill !== 'off'}
        class="rounded-lg border px-3 py-2 text-left transition-colors cursor-pointer {requestsPill !== 'off'
          ? 'border-brand-500/50 bg-brand-500/10'
          : 'border-border bg-surface-2/60 hover:bg-surface-2'}"
      >
        <span class="block text-[11px] text-text-muted">Requests</span>
        <span class="mt-0.5 flex items-center gap-1">
          <span class="text-sm font-semibold font-mono text-text-main">{totalReqs.toLocaleString()}</span>
          {#if requestsPill === 'forward'}
            <span class="material-symbols-outlined text-[12px] text-brand-500" aria-hidden="true">arrow_downward</span>
          {:else if requestsPill === 'reverse'}
            <span class="material-symbols-outlined text-[12px] text-brand-500" aria-hidden="true">arrow_upward</span>
          {/if}
        </span>
      </button>
      <!-- Tokens: the spend figure. A personal operator's first question about
           a key list is which one is consuming the quota, and the spread is
           wide enough to be worth seeing — a deployment with four keys can have
           one idle, one at a hundred thousand and one at two billion. Forward is
           the heaviest first, reverse the lightest, and either way the number is
           the total across every key, not the rows currently showing. -->
      <button
        type="button"
        onclick={() => {
          tokensPill = cycle(tokensPill)
          /* Requests and Tokens both order by a spend figure, so engaging one
             releases the other: two spend sorts at once would leave the
             comparator ambiguous. */
          if (tokensPill !== 'off') requestsPill = 'off'
        }}
        aria-pressed={tokensPill !== 'off'}
        class="rounded-lg border px-3 py-2 text-left transition-colors cursor-pointer {tokensPill !== 'off'
          ? 'border-brand-500/50 bg-brand-500/10'
          : 'border-border bg-surface-2/60 hover:bg-surface-2'}"
      >
        <span class="block text-[11px] text-text-muted">Tokens</span>
        <span class="mt-0.5 block text-sm font-semibold font-mono text-text-main">{compactTokens(totalTokens)}</span>
      </button>
      <!-- Unused: never spent. Half of a personal deployment can be keys that
           were minted for a tool that never got configured, and each one is a
           credential that grants access to nothing while still being something
           to leak. This pill is the cleanup list. -->
      <button
        type="button"
        onclick={() => (unusedPill = cycle(unusedPill))}
        aria-pressed={unusedPill !== 'off'}
        class="rounded-lg border px-3 py-2 text-left transition-colors cursor-pointer {unusedPill !== 'off'
          ? 'border-brand-500/50 bg-brand-500/10'
          : 'border-border bg-surface-2/60 hover:bg-surface-2'}"
      >
        <span class="block text-[11px] text-text-muted">Unused</span>
        <span class="mt-0.5 block text-sm font-semibold font-mono {unusedCount > 0 ? 'text-warning' : 'text-text-subtle'}">
          {unusedCount}
        </span>
      </button>
    </div>
    <!-- Search and sort, plus the create action.

         Status and Scope used to be selects here. On a phone they had no room
         beside a search field, and reaching them meant two taps into a menu to
         arrive at a state the pills above already express — so those pills are
         the filter now, and this bar keeps only what a pill cannot do: match on
         text, order the result, and start a new key.

         Create Key sits at the right end of the search row rather than in the
         card header. It is the one action an operator takes after reading the
         list, so it belongs where the reading ends rather than where it begins,
         and on a phone the header is the first thing to scroll away. -->
    <div class="flex items-center gap-2 rounded-xl border border-border bg-surface-2/50 p-2.5">
      <div class="relative flex-1 min-w-0">
        <span class="absolute left-3 top-1/2 -translate-y-1/2 text-text-subtle pointer-events-none" aria-hidden="true">
          <Search class="w-4 h-4" />
        </span>
        <input
          type="text"
          value={searchTerm}
          oninput={(e) => (searchTerm = e.currentTarget.value)}
          placeholder="Search keys or tokens..."
          aria-label="Search keys"
          class="w-full pl-9 pr-3 py-1.5 rounded-lg bg-bg border border-border font-mono text-xs text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50 transition-colors"
        />
      </div>
      <label class="flex items-center gap-1.5 rounded-lg border border-border bg-bg px-2.5 py-1.5 text-xs shrink-0">
        <span class="text-text-subtle">Sort</span>
        <select
          value={sortKey}
          onchange={(e) => (sortKey = e.currentTarget.value as 'newest' | 'name' | 'requests')}
          aria-label="Sort keys"
          class="bg-transparent border-0 p-0 text-xs font-medium text-text-main focus:outline-none cursor-pointer"
        >
          <option value="newest">Newest</option>
          <option value="name">Name</option>
          <option value="requests">Requests</option>
        </select>
      </label>
      {#if isFiltered}
        <button
          type="button"
          onclick={() => {
            searchTerm = ''
            activePill = 'off'
            requestsPill = 'off'
            tokensPill = 'off'
            unusedPill = 'off'
          }}
          class="shrink-0 rounded-lg px-2.5 py-1.5 text-xs text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
        >
          Clear
        </button>
      {/if}
      <button
        type="button"
        onclick={() => onCreate?.()}
        aria-label="Create Key"
        class="shrink-0 inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-brand-500 hover:bg-brand-600 text-white text-xs font-semibold transition cursor-pointer"
      >
        <Plus class="w-3.5 h-3.5" />
        <!-- One label, not a responsive pair. "Create Key" on desktop and
             "New" on a phone gave the same control two accessible names, so
             getByRole('button', {name: 'Create Key'}) — what the e2e suite and
             any screen-reader user addresses it by — matched nothing below sm.
             aria-label pins one name at every width; the visible text is
             shortened with CSS, which changes what is read off the screen
             without changing what the control is called. -->
        <span class="hidden sm:inline">Create Key</span>
        <span class="sm:hidden" aria-hidden="true">New</span>
      </button>
    </div>

    <!-- Batch bar. It sits directly under the search row, above the list it
         acts on: selecting rows is the last thing an operator does before
         reaching for Enable, Pause or Delete, so the actions belong within a
         thumb of the selection rather than a screen away above the filters. -->
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

        {#if (k.tokenQuota ?? 0) > 0}
          <!-- Metered keys get a reset (#365), and it is offered whether or not
               the budget is spent: an operator raising a ceiling for next month
               wants the same control as one clearing an exhausted key. The note
               says what will be cleared. -->
          <MenuItem
            label="Reset token quota"
            icon="restart_alt"
            note={compactTokens(k.usedTokens ?? 0)}
            disabled={busyId !== null}
            onSelect={() => handleResetQuota(k)}
          />
        {/if}

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

    <!--
      Keys render as a grid of collapsible cards, not a table.

      The table was five columns wide by construction, so at a phone width the
      scroll box held 500px of content with the policy and actions columns
      outside the viewport, and the secret cell broke one character per line.
      The card grid carries the same facts in the width a phone actually has,
      and it scales to desktop without a second layout to keep in sync.

      Each card collapses. An operator with a dozen keys reads the list as names
      and status; the secret, the policy badges and the usage meter are detail
      that only matters for the key being worked on. Collapsing keeps the list
      scannable without hiding anything behind a navigation step — the header
      is the toggle, so expanding is one click on the row already being read.

      The card uses this repo's own tokens throughout. The design it is ported
      from used an emerald accent and a purple secondary; both are dropped here
      because the dashboard has one accent (terracotta, --color-brand-500) and a
      second hue on one card type reads as a different kind of thing rather than
      a different environment.
    -->
    <!-- Cells shared by the desktop table and the mobile card. Both layouts
         show the same facts, so they render the same snippets — a card cannot
         drift from its table row. -->
    {#snippet secretCell(k: APIKey, chipWidth?: string)}
      {@const shown = shownKeyIds.has(k.id)}
      <!-- One chip holding the secret and its two verbs. The token, the reveal
           toggle and copy were three separate elements before, which read as
           three controls and cost the token most of the column width on a
           narrow table. Inside one bordered chip they are one thing: the value
           with the two actions that apply to it.

           The two buttons sit at the right edge and never move: `ml-auto` on the
           first of them pushes the pair to the end regardless of how much room
           the token takes, so the eye and copy stay where an operator expects
           them on every row instead of drifting with the length of each secret.
           The token gets whatever is left and truncates.

           Reveal and copy stay inline rather than in the overflow menu because
           they are the verbs used constantly — hiding them behind a menu made
           the common path slower. -->
      <div
        class="flex items-center gap-1.5 min-w-0 bg-bg border border-border rounded-lg pl-2.5 pr-1 py-1 {chipWidth ? '' : 'w-full'}" style={chipWidth ? `width:${chipWidth}` : ''}
      >
        <code class="font-mono text-[11px] text-text-main truncate select-all min-w-0 flex-1">
          {shown ? renderedKey(k) : maskSecret(renderedKey(k))}
        </code>
        {#if isRevealable(k)}
          <button
            type="button"
            onclick={() => toggleShow(k.id)}
            class="shrink-0 ml-auto p-1 rounded-md text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
            aria-label={shown ? `Hide key ${keyLabel(k)}` : `Show key ${keyLabel(k)}`}
            title={shown ? 'Hide key' : 'Show key'}
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
          onclick={() => handleCopy(k)}
          class="shrink-0 p-1 rounded-md text-text-muted hover:text-brand-500 hover:bg-surface-2 transition-colors cursor-pointer"
          aria-label={`Copy key ${keyLabel(k)}`}
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
    {#snippet usageCell(k: APIKey)}
      {@const quota = k.tokenQuota ?? 0}
      {@const used = k.usedTokens ?? 0}
      {#if quota > 0}
        <div class="space-y-1 min-w-[110px]">
          <div class="flex justify-between items-center text-[11px]">
            <span class="text-text-muted">Budget</span>
            <span class="text-text-main font-medium font-mono">
              {compactTokens(used)}
              <span class="text-text-subtle">/ {compactTokens(quota)}</span>
            </span>
          </div>
          <div class="w-full bg-surface-2 rounded-full h-1.5 overflow-hidden border border-border/50">
            <div
              class="h-1.5 rounded-full transition-all {used >= quota ? 'bg-danger' : 'bg-brand-500'}"
              style="width: {Math.min(100, Math.round((used / quota) * 100))}%"
            ></div>
          </div>
        </div>
      {:else}
        <span class="text-text-subtle text-[11px]">unmetered</span>
      {/if}
    {/snippet}
    {#snippet statusCell(k: APIKey)}
      {@const active = isActive(k)}
      <span
        class="inline-flex items-center gap-1.5 text-[11px] font-medium {active
          ? 'text-success'
          : 'text-text-muted'}"
      >
        <span class="w-1.5 h-1.5 rounded-full {active ? 'bg-success' : 'bg-danger opacity-60'}"></span>
        {active ? 'Active' : 'Paused'}
      </span>
    {/snippet}
    {#if visibleKeys.length === 0}
      <!-- No match. This is a different situation from having no keys at all,
           and the two need different actions: the first is "widen the search",
           the second is "create a key". Saying "no API keys yet" to someone who
           has twelve and typed the wrong name sends them to the wrong button. -->
      <div class="text-center py-10 space-y-3">
        <div class="inline-flex items-center justify-center w-12 h-12 rounded-full bg-surface-2 text-text-muted">
          <Search class="w-6 h-6" />
        </div>
        <div class="space-y-1">
          <p class="text-text-main font-medium text-sm">No keys match</p>
          <p class="text-xs text-text-muted">Nothing matches the current search and filters.</p>
        </div>
        <button
          type="button"
          onclick={() => {
            searchTerm = ''
            activePill = 'off'
            requestsPill = 'off'
            tokensPill = 'off'
            unusedPill = 'off'
          }}
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 text-xs font-semibold text-text-main transition cursor-pointer"
        >
          <RotateCcw class="w-3.5 h-3.5" />
          Clear filters
        </button>
      </div>
    {:else}
    <ul class="md:hidden grid grid-cols-1 gap-3">
      {#each visibleKeys as k (k.id)}
        {@const active = isActive(k)}
        {@const expanded = expandedIds.has(k.id)}
        <li class="rounded-xl border border-border bg-surface-2/40 overflow-hidden {active ? '' : 'opacity-70'}">
          <!-- Header: the whole row is the toggle. A button rather than a div
               with onclick, so it is reachable by keyboard and announced as a
               control. -->
          <button
            type="button"
            onclick={() => toggleExpanded(k.id)}
            aria-expanded={expanded}
            class="w-full flex items-center gap-2 p-3 text-left cursor-pointer hover:bg-surface-2/60 transition-colors"
          >
            <input
              type="checkbox"
              checked={selectedIds.includes(k.id)}
              aria-label={`Select ${keyLabel(k)}`}
              onclick={(e) => e.stopPropagation()}
              onchange={() => (selectedIds = toggleOne(selectedIds, k.id))}
              class="size-3.5 shrink-0 rounded border-border/60 accent-[var(--primary)] cursor-pointer"
            />
            <span class="min-w-0 flex-1 text-sm font-semibold text-text-main truncate">
              {k.name || 'Client Token'}
            </span>
            <!-- KeiRouter's StatusPill: a bare dot plus a label, not a filled
                 badge. A paused key reads at a glance without spending a
                 column on it. -->
            <span
              class="inline-flex shrink-0 items-center gap-1.5 text-[11px] font-medium {active
                ? 'text-success'
                : 'text-text-muted'}"
            >
              <span class="w-1.5 h-1.5 rounded-full {active ? 'bg-success' : 'bg-danger opacity-60'}"></span>
              {active ? 'Active' : 'Paused'}
            </span>
            <span
              class="shrink-0 text-text-muted transition-transform {expanded ? 'rotate-180' : ''}"
              aria-hidden="true"
            >
              <ChevronDown class="w-4 h-4" />
            </span>
          </button>

          {#if expanded}
            <div class="px-3 pb-3 space-y-3 border-t border-border-subtle pt-3">
              <!-- The same cells the desktop table renders, so a card cannot
                   drift from its row. -->
              {@render secretCell(k)}
              {@render policyCell(k)}
              {@render usageCell(k)}
              <!-- Footer actions. Regenerate sits on the left because it is the
                   one verb an operator reaches for without reading the menu;
                   the rest stay behind the overflow menu. -->
              <div class="flex items-center justify-between gap-2 pt-2 border-t border-border-subtle">
                <button
                  type="button"
                  onclick={() => handleRotate(k)}
                  class="text-[11px] text-text-muted hover:text-text-main px-2 py-1 rounded-lg hover:bg-surface-2 transition-colors inline-flex items-center gap-1.5 cursor-pointer"
                >
                  <RotateCw class="w-3.5 h-3.5" />
                  Regenerate
                </button>
                {@render keyActions(k)}
              </div>
            </div>
          {/if}
        </li>
      {/each}
    </ul>

    <!-- Desktop: the table. It is the layout an operator with a dozen keys
         works in — one row per key, five columns, no expanding — because
         scanning a list of names and statuses is what the table is for and the
         card's collapse step is friction there.

         Below `md` the card grid takes over. A row is five columns wide by
         construction, so at a phone width (390px, ~292px of card width) the
         table's scroll box held 500px of content: the policy and actions
         columns sat outside the viewport with no way to reach them, and the
         secret cell broke one character per line.

         Both layouts render the same snippets, so a card cannot drift from its
         table row. -->
    <div class="hidden md:block overflow-x-auto">
      <table class="w-full text-left text-xs">
        <thead>
          <tr class="border-b border-border text-text-subtle font-code uppercase text-[10px] tracking-wider">
            <th class="w-8 py-2.5 pl-3">
              <input
                type="checkbox"
                checked={selectedIds.length > 0 && selectedIds.length === visibleKeys.length}
                aria-label="Select all keys"
                onchange={() => {
                  selectedIds =
                    selectedIds.length === visibleKeys.length ? [] : visibleKeys.map((k) => k.id)
                }}
                class="size-3.5 rounded border-border/60 accent-[var(--primary)] cursor-pointer"
              />
            </th>
            <th class="py-2.5 px-3">Key</th>
            <th class="py-2.5 px-3">Token</th>
            <th class="py-2.5 px-3">Policy &amp; created</th>
            <th class="py-2.5 px-3">Usage</th>
            <th class="py-2.5 px-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/40">
          {#each visibleKeys as k (k.id)}
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
                  {@render statusCell(k)}
                </div>
              </td>
              <td class="py-3 px-3">
                {@render secretCell(k, tokenChipWidth)}
              </td>
              <td class="py-3 px-3">
                {@render policyCell(k)}
              </td>
              <td class="py-3 px-3">
                {@render usageCell(k)}
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
  {/if}
</Card>

{#if policyKey}
  <ApiKeyPolicyModal
    apiKey={policyKey}
    onClose={() => (policyKey = null)}
    onSaved={() => {
      /* The rename lives in the same dialog, so the list is stale until the
         parent re-reads it. */
      policyKey = null
      onRefresh?.()
    }}
  />
{/if}
