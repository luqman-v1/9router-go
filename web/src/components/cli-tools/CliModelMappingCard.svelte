<script lang="ts">
  // CLI Model Mapping panel — port of Cartethyia's cli-tools feature.
  // Lets an operator remap slot names (opus, sonnet, haiku) that a scoped
  // API key sends under a Claude Code User-Agent to any routed model.
  import { onMount } from 'svelte'
  import { Plus, Trash2, RefreshCw } from 'lucide-svelte'
  import { api, type APIKey, type CliToolMapping } from '../../api/client'

  interface Props {
    apiKey: APIKey
  }

  let { apiKey }: Props = $props()

  let mappings = $state<CliToolMapping[]>([])
  let hasScope = $state(false)
  let mappingsEnabled = $state(true)
  let isLoading = $state(true)
  let error = $state('')

  // New-mapping form state.
  let newSlot = $state('sonnet')
  let newTarget = $state('')

  const SLOTS = ['opus', 'sonnet', 'haiku']

  async function refresh() {
    isLoading = true
    error = ''
    try {
      const [m, s, k] = await Promise.all([
        api.getCliToolMappings(apiKey.id, 'claude'),
        api.getCliToolSettings(apiKey.id, 'claude'),
        fetch(`/api/keys/${encodeURIComponent(apiKey.id)}/cli-scope`, { credentials: 'include' }).then((r) => r.json()),
      ])
      mappings = m.mappings ?? []
      mappingsEnabled = s.settings ? !!s.settings.mappingsEnabled : true
      hasScope = !!k.hasCliMapping
    } catch (e) {
      error = String(e)
    } finally {
      isLoading = false
    }
  }

  async function toggleScope(grant: boolean) {
    error = ''
    try {
      await fetch(`/api/keys/${encodeURIComponent(apiKey.id)}/cli-scope`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ grantCliMapping: grant }),
      })
      hasScope = grant
    } catch (e) {
      error = String(e)
    }
  }

  async function toggleEnabled(on: boolean) {
    mappingsEnabled = on
    await api.setCliToolSettings({ apiKeyId: apiKey.id, toolId: 'claude', mappingsEnabled: on, mode: 'remote' })
  }

  async function addMapping() {
    if (!newSlot || !newTarget.trim()) return
    await api.saveCliToolMapping({
      toolId: 'claude',
      apiKeyId: apiKey.id,
      slotKey: newSlot,
      targetModel: newTarget.trim(),
      enabled: true,
    })
    newTarget = ''
    await refresh()
  }

  async function removeMapping(id: string) {
    await api.deleteCliToolMapping(id)
    await refresh()
  }

  onMount(refresh)
</script>

<div class="p-3 rounded-xl bg-surface-2 border border-border/80 space-y-3">
  <div class="flex items-center justify-between">
    <div>
      <span class="text-xs font-bold text-text-main">CLI Model Mapping</span>
      <p class="text-[11px] text-text-muted">
        Remap Claude Code slots (opus / sonnet / haiku) for this key when the request's User-Agent is claude-cli.
      </p>
    </div>
    <button
      type="button"
      class="p-1.5 rounded-md hover:bg-surface-3 text-text-muted"
      onclick={refresh}
      title="Refresh"
    >
      <RefreshCw class="w-3.5 h-3.5" />
    </button>
  </div>

  {#if isLoading}
    <p class="text-[11px] text-text-muted">Loading…</p>
  {:else}
    <div class="flex items-center justify-between pt-1 border-t border-border/40">
      <div>
        <span class="text-xs font-semibold text-text-main">Remote routing for this key</span>
        <p class="text-[11px] text-text-muted">Grants the routing:cli_mapping scope on this key</p>
      </div>
      <label class="relative inline-flex items-center cursor-pointer">
        <input
          type="checkbox"
          class="sr-only peer"
          checked={hasScope}
          onchange={(e) => toggleScope(e.currentTarget.checked)}
        />
        <div class="w-9 h-5 bg-surface-3 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-border after:border after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:bg-brand-500 border border-border"></div>
      </label>
    </div>

    {#if hasScope}
      <div class="flex items-center justify-between pt-1 border-t border-border/40">
        <div>
          <span class="text-xs font-bold text-text-main">Mappings enabled</span>
          <p class="text-[11px] text-text-muted">Master switch for this key's slot routes</p>
        </div>
        <label class="relative inline-flex items-center cursor-pointer">
          <input type="checkbox" class="sr-only peer" checked={mappingsEnabled} onchange={(e) => toggleEnabled(e.currentTarget.checked)} />
          <div class="w-9 h-5 bg-surface-3 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-border after:border after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:bg-brand-500 border border-border"></div>
        </label>
      </div>

      <div class="space-y-1.5">
        {#each mappings as m (m.id)}
          <div class="flex items-center justify-between gap-2 px-2 py-1.5 rounded-lg bg-surface-3 border border-border/60">
            <div class="min-w-0">
              <span class="text-[11px] font-semibold text-text-main">{m.slotKey}</span>
              <span class="text-text-muted"> → </span>
              <span class="text-[11px] font-mono text-brand-400">{m.targetModel}</span>
            </div>
            <button
              type="button"
              class="p-1 rounded-md hover:bg-surface-3 text-text-muted hover:text-error"
              onclick={() => removeMapping(m.id)}
              title="Delete mapping"
            >
              <Trash2 class="w-3.5 h-3.5" />
            </button>
          </div>
        {:else}
          <p class="text-[11px] text-text-muted">No mappings yet — slots resolve through ordinary aliases.</p>
        {/each}
      </div>

      <div class="flex items-center gap-2 pt-2 border-t border-border/40">
        <select bind:value={newSlot} class="text-xs px-2 py-1.5 rounded-lg bg-surface-3 border border-border/60 text-text-main">
          {#each SLOTS as s}
            <option value={s}>{s}</option>
          {/each}
        </select>
        <input
          type="text"
          bind:value={newTarget}
          placeholder="provider/model e.g. openai/gpt-5.2"
          class="flex-1 text-xs px-2 py-1.5 rounded-lg bg-surface-3 border border-border/60 text-text-main placeholder:text-text-muted"
        />
        <button
          type="button"
          class="flex items-center gap-1 px-2 py-1.5 text-xs font-semibold rounded-lg bg-brand-500 text-white hover:opacity-90 disabled:opacity-40"
          disabled={!newTarget.trim()}
          onclick={addMapping}
        >
          <Plus class="w-3.5 h-3.5" />
          Add
        </button>
      </div>
    {:else}
      <p class="text-[11px] text-text-muted">Enable remote routing above to configure slot mappings for this key.</p>
    {/if}
  {/if}

  {#if error}
    <p class="text-[11px] text-error">{error}</p>
  {/if}
</div>
