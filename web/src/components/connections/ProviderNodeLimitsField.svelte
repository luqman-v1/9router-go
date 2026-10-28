<script lang="ts">
  // Capacity limits, as a section inside the OpenAI/Anthropic-compatible node
  // dialogs — not a card of its own: the fields use the same Input as Name, Prefix
  // and Base URL, separated from them by a rule, and collapsed to one header line
  // until someone needs them.
  //
  // The section opens by itself when the node already has a limit and stays closed
  // for an unlimited node, so a fresh node's dialog shows only the fields that
  // matter for creating it. Typing does not reopen or close it: only the node's
  // stored policy can, because that is what the operator is being shown.
  import Input from '../../lib/ui/Input.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import {
    hasAnyNodeLimit,
    nodeLimitsDraftOf,
    nodeLimitsSummary,
    nodeLimitsUsageSummary,
    type NodeLimitsDraft,
  } from '../../lib/node-limits'
  import { api, type ProviderNodeLimits } from '../../api/client'

  interface Props {
    draft: NodeLimitsDraft
    /** The node's stored policy, for the read-only usage rows and the reset. */
    usage?: ProviderNodeLimits | null
    /** Present only when the node exists; the reset writes against this id. */
    nodeId?: string
    /** Called with the fresh policy after a token-count reset. */
    onReset?: (limits: ProviderNodeLimits) => void
  }

  let { draft = $bindable(), usage = null, nodeId = '', onReset }: Props = $props()

  let open = $state(false)
  let resetting = $state(false)
  let resetError = $state('')
  let confirmingReset = $state(false)

  // Which node the section was last seeded for. A plain variable, not $state: the
  // effect below writes it, and reading it back as reactive state would retrigger
  // the effect on its own write.
  let seededFor: string | null = null

  // Seeded from the stored policy, deliberately not from `draft`: a policy that
  // exists opens the section, an unlimited node keeps it closed.
  //
  // Seeded once per node, because the provider page re-reads the node list while
  // this dialog is open and hands down a fresh `limits` object each time. Tracking
  // that object directly made the section collapse under the operator's hands every
  // time a poll landed, which reads as the control closing itself.
  $effect(() => {
    const node = nodeId || 'new-node'
    if (seededFor === node) return
    seededFor = node
    open = hasAnyNodeLimit(nodeLimitsDraftOf(usage))
    confirmingReset = false
  })

  async function handleReset() {
    if (!nodeId || resetting) return
    resetting = true
    resetError = ''
    try {
      const out = await api.resetProviderNodeQuota(nodeId)
      onReset?.(out.limits)
    } catch (err) {
      resetError = err instanceof Error ? err.message : String(err)
    } finally {
      resetting = false
    }
  }
</script>

<div class="border-t border-border pt-4">
  <button
    type="button"
    class="flex w-full items-start justify-between gap-3 text-left cursor-pointer focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/40 rounded-md"
    aria-expanded={open}
    aria-controls="node-limits-fields"
    onclick={() => (open = !open)}
  >
    <span class="flex flex-col gap-0.5">
      <span class="text-sm font-medium text-text-main">Capacity limits</span>
      <span class="text-xs text-text-muted">
        {#if open}Optional. Applied to every request routed through this node.{:else}{nodeLimitsSummary(draft)}{/if}
      </span>
    </span>
    <span class="material-symbols-outlined text-[20px] text-text-muted shrink-0" aria-hidden="true">
      {open ? 'expand_less' : 'expand_more'}
    </span>
  </button>

  {#if open}
    <div id="node-limits-fields" class="mt-4 flex flex-col gap-4">
      <div class="grid gap-4 sm:grid-cols-2">
        <Input id="node-limits-rpm" label="Requests / minute" type="number" min="0" bind:value={draft.rpm} placeholder="0" />
        <Input id="node-limits-tpm" label="Prompt tokens / minute" type="number" min="0" bind:value={draft.tpm} placeholder="0" />
        <Input
          id="node-limits-concurrency"
          label="Concurrent requests"
          type="number"
          min="0"
          bind:value={draft.concurrency}
          placeholder="0"
        />
        <Input
          id="node-limits-queue"
          label="Queue wait (ms)"
          type="number"
          min="0"
          bind:value={draft.queueTimeoutMs}
          placeholder="0"
          hint="How long a request waits for a slot before the node counts as full."
        />
        <Input
          id="node-limits-quota"
          label="Lifetime token quota"
          type="number"
          min="0"
          bind:value={draft.tokenQuota}
          placeholder="0"
          hint="Prompt + completion tokens for this node. Once spent, requests to it answer 403."
        />
        <Input
          id="node-limits-expiry"
          label="Contract ends (optional)"
          type="date"
          bind:value={draft.contractExpiresAt}
          hint="Last day the subscription is valid. Empty means no expiry."
        />
      </div>

      <p class="text-xs text-text-muted">
        0 means unlimited. A request that cannot get a slot is served by another candidate; 429 only appears once no
        candidate is left.
      </p>

      {#if usage}
        <div class="flex flex-col gap-2 border-t border-border pt-3">
          <div class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
            <span class="text-xs font-medium text-text-main">Token count</span>
            <span class="text-xs text-text-muted font-mono">{nodeLimitsUsageSummary(usage)}</span>
          </div>
          {#if confirmingReset}
            <p class="text-xs text-text-main">
              Reset the counter to zero from this moment? The node's usage history is not deleted — the Usage page keeps
              every request, and only the number this quota is measured against restarts.
            </p>
            <div class="flex gap-2">
              <Button variant="danger" size="sm" loading={resetting} onclick={handleReset}>Yes, reset the count</Button>
              <Button variant="ghost" size="sm" onclick={() => (confirmingReset = false)}>Cancel</Button>
            </div>
          {:else}
            <div class="flex flex-wrap items-center justify-between gap-2">
              <p class="text-xs text-text-muted">
                {#if usage.tokenQuota > 0}
                  Quota counts from the last reset. Restarting it starts a fresh allowance without touching history.
                {:else}
                  Set a lifetime quota above, then this counter is what it is measured against.
                {/if}
              </p>
              <Button variant="secondary" size="sm" onclick={() => (confirmingReset = true)} disabled={!nodeId}>
                Reset token count
              </Button>
            </div>
          {/if}
        </div>
        {#if usage.expired}
          <p class="text-xs text-red-500">The contract has ended, so this node is skipped until the date is moved forward.</p>
        {:else if usage.tokenQuota > 0 && usage.usedTokens >= usage.tokenQuota}
          <p class="text-xs text-red-500">The lifetime quota is spent; requests to this node answer 403.</p>
        {/if}
      {/if}

      {#if resetError}
        <p class="text-xs text-red-500" role="alert">{resetError}</p>
      {/if}
    </div>
  {/if}
</div>
