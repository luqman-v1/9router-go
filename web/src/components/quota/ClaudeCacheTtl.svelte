<script lang="ts">
  // Operator knob for the prompt-cache window 9router pins onto the system and
  // tools head anchors of every Claude request. It lives on both the Claude
  // provider page and the Claude quota tracker because those are the two places
  // where its cost is watched. The tradeoff is spelled out inline: the choice is
  // a billing decision, not a preference.
  import { onMount } from 'svelte'
  import { api } from '../../api/client'

  let ttl = $state<'5m' | '1h'>('5m')
  let saving = $state(false)
  let error = $state('')

  onMount(async () => {
    try {
      const settings = await api.getSettings()
      if (settings.claudeCacheTtl === '1h') ttl = '1h'
    } catch {
      // An unreadable settings row means the default 5m, which is already shown.
    }
  })

  async function onChange(event: Event) {
    const next = (event.currentTarget as HTMLSelectElement).value as '5m' | '1h'
    const previous = ttl
    ttl = next
    saving = true
    error = ''
    try {
      await api.updateSettings({ claudeCacheTtl: next })
    } catch (e) {
      ttl = previous
      error = e instanceof Error ? e.message : 'Failed to save'
    } finally {
      saving = false
    }
  }
</script>

<div class="rounded-xl border border-border-subtle bg-surface p-4">
  <div class="flex flex-wrap items-center justify-between gap-3">
    <div class="min-w-0">
      <p class="text-sm font-semibold text-text-main">Prompt cache TTL (Claude)</p>
      <p class="text-[11px] text-text-muted">
        Window 9router pins on the system + tools head anchors of every Claude request.
      </p>
    </div>
    <select
      value={ttl}
      onchange={onChange}
      disabled={saving}
      class="rounded-md border border-border bg-background px-2 py-1 text-xs focus:border-primary focus:outline-none cursor-pointer disabled:cursor-not-allowed disabled:opacity-60"
    >
      <option value="5m">5 minutes (default)</option>
      <option value="1h">1 hour</option>
    </select>
  </div>
  {#if error}
    <p class="mt-2 text-[11px] text-red-500">{error}</p>
  {/if}
  <div class="mt-3 grid gap-2 text-[11px] leading-relaxed sm:grid-cols-2">
    <div
      class="rounded-lg border px-3 py-2 {ttl === '5m'
        ? 'border-primary/40 bg-primary/5'
        : 'border-border-subtle bg-surface-2'}"
    >
      <p class="font-semibold text-text-main">5 minutes — cache write 1.25x</p>
      <p class="mt-1 text-text-muted">
        Cheapest per written token. Choose this when you work in bursts: nearly every turn lands
        inside 5 minutes, so the prefix is read back instead of written again. A pause longer than 5
        minutes drops the cache and the next turn pays one fresh write.
      </p>
    </div>
    <div
      class="rounded-lg border px-3 py-2 {ttl === '1h'
        ? 'border-primary/40 bg-primary/5'
        : 'border-border-subtle bg-surface-2'}"
    >
      <p class="font-semibold text-text-main">1 hour — cache write 2x</p>
      <p class="mt-1 text-text-muted">
        Costs 60% more per written token but survives an idle break of up to an hour. Choose this
        only if you routinely stop for 5–60 minutes between turns: the resumed turn then reads the
        prefix instead of rewriting it. Under a 5-minute pause it buys nothing, and past an hour it
        has expired anyway.
      </p>
    </div>
  </div>
</div>
