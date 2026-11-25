<script lang="ts">
  import { api, type APIKey, type JailbreakModelConfig } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'

  // One API key's jailbreak overrides. Everything here is scoped to the key
  // once: a row binds a model id to a preset for that key only, and the row
  // outranks the same model's global binding on the request path.
  interface Props {
    /** Every client key, so the picker does not need its own fetch. */
    keys: APIKey[]
    /** The live preset library: labels for the picks, and the ids to offer. */
    presets: { id?: string; name?: string }[]
    /** The model id the global list currently has open, prefilled when set. */
    initialModelId?: string
    classes?: string
    onchanged?: () => void
  }

  let { keys, presets, initialModelId = '', classes = '', onchanged }: Props = $props()

  let keyId = $state('')
  let modelId = $state('')
  let presetId = $state('')
  let saving = $state(false)
  let status = $state('')
  let error = $state('')
  let loading = $state(false)
  let bindings = $state<Record<string, JailbreakModelConfig>>({})
  let confirmDeleteId = $state<string | null>(null)

  $effect(() => {
    if (!keyId && keys.length > 0) keyId = keys[0].id
  })

  // The model id from the global list is a starting point for the field, not a
  // bound value: it is only adopted while the operator has typed nothing.
  $effect(() => {
    if (!modelId && initialModelId) modelId = initialModelId
  })

  // A key change re-reads, so the table never shows another key's rows.
  $effect(() => {
    if (keyId) void reload()
  })

  function message(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  function label(id: string): string {
    if (id === 'off' || id === 'none') return 'Off'
    const p = presets.find((x) => (x.id ?? x.name) === id)
    return p ? p.name || p.id || id : id
  }

  let keyName = $derived(keys.find((k) => k.id === keyId)?.name || keyId || '—')
  let rows = $derived(Object.entries(bindings).sort(([a], [b]) => a.localeCompare(b)))

  async function reload() {
    if (!keyId) {
      bindings = {}
      return
    }
    loading = true
    error = ''
    try {
      const res = await api.listJailbreakKeyBindings(keyId)
      bindings = res || {}
    } catch (e) {
      bindings = {}
      error = 'Load failed: ' + message(e)
    } finally {
      loading = false
    }
  }

  async function save() {
    const id = modelId.trim()
    if (!keyId) {
      error = 'Pick an API key first.'
      return
    }
    if (!id) {
      error = 'Enter a model id.'
      return
    }
    saving = true
    error = ''
    status = ''
    try {
      await api.setJailbreakKeyModel(keyId, id, presetId, true)
      modelId = ''
      presetId = ''
      status = `Armed "${id}" for ${keyName}.`
      await reload()
      onchanged?.()
    } catch (e) {
      error = message(e)
    } finally {
      saving = false
    }
  }

  async function toggle(id: string, cfg: JailbreakModelConfig) {
    status = ''
    error = ''
    try {
      await api.setJailbreakKeyModel(keyId, id, cfg.presetId, !cfg.enabled)
      await reload()
      onchanged?.()
    } catch (e) {
      error = message(e)
    }
  }

  async function remove(id: string) {
    if (confirmDeleteId !== id) {
      confirmDeleteId = id
      return
    }
    confirmDeleteId = null
    status = ''
    error = ''
    try {
      // The per-key delete drops the row outright. Turning it off instead would
      // leave a presetId-less row behind that the list's orphan cleanup deletes
      // on the next reload, and a later toggle-on would remap it to a preset
      // that may not exist — so the only correct "remove" is a real delete.
      await api.deleteJailbreakKeyModel(keyId, id)
      await reload()
      onchanged?.()
    } catch (e) {
      error = message(e)
    }
  }
</script>

<div class="space-y-3 {classes}">
  <div class="space-y-1">
    <h2 class="text-sm font-bold text-text-main">Per-key bindings</h2>
    <p class="text-[11px] text-text-subtle">Overrides the global binding for this key only.</p>
  </div>
  <p class="text-xs text-text-subtle">
    A row here overrides the <span class="text-text-main">global</span> binding for the same model,
    for this key only — the resolver reads the key's binding first, so a resold client gets its own
    persona without touching what the rest of the instance sees.
  </p>

  <div class="flex flex-wrap items-end gap-3">
    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">API key</span>
      <select
        bind:value={keyId}
        aria-label="API key"
        class="min-w-[220px] px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none focus:border-brand-500"
      >
        {#each keys as k (k.id)}
          <option value={k.id}>{k.name || k.keyDisplay || k.id}</option>
        {/each}
      </select>
    </label>
    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">Model id</span>
      <input
        type="text"
        bind:value={modelId}
        placeholder="providerx/deepseek-4-pro"
        aria-label="Model id for this key"
        class="min-w-[240px] px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs font-code placeholder:text-text-subtle outline-none focus:border-brand-500"
      />
    </label>
    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">Preset</span>
      <select
        bind:value={presetId}
        aria-label="Preset for this key"
        class="min-w-[200px] px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none focus:border-brand-500"
      >
        <option value="">Default document</option>
        {#each presets as preset (preset.id ?? preset.name)}
          <option value={preset.id ?? ''}>{preset.name || preset.id}</option>
        {/each}
      </select>
    </label>
    <Button variant="primary" size="sm" onclick={save} disabled={saving || !keyId} icon="add">
      {saving ? 'Saving…' : 'Arm for key'}
    </Button>
  </div>

  {#if error}<p class="text-xs text-error">{error}</p>
  {:else if status}<p class="text-xs text-text-muted">{status}</p>{/if}

  {#if loading}
    <p class="text-xs text-text-subtle">Loading key bindings…</p>
  {:else if rows.length === 0}
    <p class="text-xs text-text-subtle">
      No key-scoped binding yet — every model this key sends follows the global binding.
    </p>
  {:else}
    <ul class="divide-y divide-border-subtle rounded-[10px] border border-border-subtle bg-surface-2/40">
      {#each rows as [id, cfg] (id)}
        <li class="flex items-center gap-3 px-3 py-2.5">
          <div class="min-w-0 flex-1">
            <div class="text-sm text-text-main font-code truncate">{id}</div>
            <div class="text-[11px] text-text-subtle truncate">
              {label(cfg.presetId)}{cfg.enabled ? '' : ' · injection off'}
            </div>
          </div>
          <div class="flex items-center gap-2 shrink-0">
            <Toggle
              checked={cfg.enabled}
              label={`${cfg.enabled ? 'Disable' : 'Enable'} ${id} for this key`}
              title={
                cfg.enabled ? 'On for this key — click to disable' : 'Off for this key — click to enable'
              }
              onChange={() => toggle(id, cfg)}
            />
            {#if confirmDeleteId === id}
              <button
                type="button"
                onclick={() => remove(id)}
                class="text-xs text-error font-medium hover:underline"
              >
                Confirm
              </button>
              <button
                type="button"
                onclick={() => (confirmDeleteId = null)}
                class="text-xs text-text-subtle hover:underline"
              >
                Cancel
              </button>
            {:else}
              <button
                type="button"
                onclick={() => remove(id)}
                aria-label={`Unbind ${id} for this key`}
                title="Unbind"
                class="text-text-subtle hover:text-error leading-none"
              >
                <span class="material-symbols-outlined text-[16px]">delete</span>
              </button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>