<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type JailbreakDoc, type JailbreakImportResult, type JailbreakKeyword, type JailbreakModelConfig } from '../api/client'
  import { matchesAnyJailbreakKeyword } from '../lib/jailbreakKeywords'
  import KeywordRules from './KeywordRules.svelte'

  // Default document: injected for any armed model that has no specific preset.
  let docPrompt = $state('')
  let docSafety = $state(false)
  let docSource = $state('')
  let docSaving = $state(false)
  let docStatus = $state('')

  // Preset library.
  let presets = $state<JailbreakDoc[]>([])
  let loading = $state(false)

  // Preset editor.
  let presetModalOpen = $state(false)
  let editingId = $state<string | null>(null)
  let presetForm = $state({
    id: '',
    name: '',
    prompt: '',
    kind: '',
    geminiSafety: false,
    source: '',
    keywords: [] as JailbreakKeyword[],
    excludes: [] as JailbreakKeyword[],
  })
  let presetSaving = $state(false)
  let presetError = $state('')

  // Per-model / provider / combo bindings.
  let models = $state<Record<string, JailbreakModelConfig>>({})
  let bindModelId = $state('')
  let bindPresetId = $state('')
  let bindError = $state('')
  let confirmDeleteId = $state<string | null>(null)

  // Keyword / exclude rules: on the default document, per preset, and the one
  // global deny list that overrides everything.
  let docKeywords = $state<JailbreakKeyword[]>([])
  let docExcludes = $state<JailbreakKeyword[]>([])
  let globalExcludes = $state<JailbreakKeyword[]>([])
  let globalSaving = $state(false)
  let globalStatus = $state('')

  // GitHub import: a whole collection of prompts in one request.
  let importTarget = $state('')
  let importing = $state(false)
  let importStatus = $state('')
  let importResult = $state<JailbreakImportResult | null>(null)
  let uploading = $state(false)
  let dragging = $state(false)

  onMount(load)

  async function load() {
    loading = true
    try {
      const [doc, presetList, modelMap, excludeList] = await Promise.all([
        api.getJailbreak(),
        api.listJailbreakPresets(),
        api.listJailbreakModels(),
        api.getJailbreakExcludes(),
      ])
      docPrompt = doc?.prompt || ''
      docSafety = !!doc?.geminiSafety
      docSource = doc?.source || ''
      docKeywords = doc?.keywords || []
      docExcludes = doc?.excludes || []
      presets = Array.isArray(presetList) ? presetList : []
      models = modelMap || {}
      globalExcludes = excludeList?.excludes || []
    } catch (e) {
      docStatus = 'Load failed: ' + errorMessage(e)
    } finally {
      loading = false
    }
  }

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  async function saveDoc() {
    docSaving = true
    docStatus = ''
    try {
      await api.saveJailbreak({
        prompt: docPrompt,
        geminiSafety: docSafety,
        source: docSource || undefined,
        keywords: docKeywords,
        excludes: docExcludes,
      })
      docStatus = 'Default document saved.'
    } catch (e) {
      docStatus = 'Save failed: ' + errorMessage(e)
    } finally {
      docSaving = false
    }
  }

  async function importFromGithub() {
    const target = importTarget.trim()
    if (!target) {
      importStatus = 'Enter a repository first, e.g. togg53192-cmd/jailbreaks'
      return
    }
    importing = true
    importStatus = ''
    importResult = null
    try {
      const res = await api.importJailbreak(target)
      importResult = res
      importStatus = `Imported ${res.imported.length} preset(s) from ${res.source}.`
      await load()
    } catch (e) {
      importStatus = 'Import failed: ' + errorMessage(e)
    } finally {
      importing = false
    }
  }

  async function uploadPresetFile(event: Event) {
    const input = event.currentTarget as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    uploading = true
    importStatus = ''
    importResult = null
    try {
      const res = await api.uploadJailbreakPreset(file)
      importResult = {
        ok: true,
        source: res.preset.path,
        imported: [res.preset],
        refused: [],
      }
      importStatus = `Uploaded ${res.preset.name} as a preset.`
      await load()
    } catch (e) {
      importStatus = 'Upload failed: ' + errorMessage(e)
    } finally {
      uploading = false
      // Reset so picking the same file again fires change.
      input.value = ''
    }
  }

  // The import field accepts a dropped file, a dropped URL, or a typed one.
  // A file goes to the upload endpoint; anything else is a target for the
  // import endpoint, which knows a GitHub repository from a single URL.
  function onDrop(event: DragEvent) {
    event.preventDefault()
    dragging = false
    const file = event.dataTransfer?.files?.[0]
    if (file) {
      void uploadPresetFile({ currentTarget: { files: [file], value: '' } } as unknown as Event)
      return
    }
    const text = event.dataTransfer?.getData('text/uri-list') || event.dataTransfer?.getData('text/plain') || ''
    const url = text.split('\n')[0].trim()
    if (url) {
      importTarget = url
      void importFromGithub()
    }
  }

  function openCreate() {
    editingId = null
    presetForm = {
      id: '',
      name: '',
      prompt: '',
      kind: '',
      geminiSafety: false,
      source: '',
      keywords: [],
      excludes: [],
    }
    presetError = ''
    presetModalOpen = true
  }

  function openEdit(preset: JailbreakDoc) {
    editingId = preset.id || null
    presetForm = {
      id: preset.id || '',
      name: preset.name || '',
      prompt: preset.prompt || '',
      kind: preset.kind || '',
      geminiSafety: !!preset.geminiSafety,
      source: preset.source || '',
      keywords: preset.keywords || [],
      excludes: preset.excludes || [],

    }
    presetError = ''
    presetModalOpen = true
  }

  async function savePreset() {
    if (!presetForm.name.trim() || !presetForm.prompt.trim()) {
      presetError = 'Name and prompt are required.'
      return
    }
    presetSaving = true
    presetError = ''
    try {
      await api.saveJailbreakPreset({
        id: presetForm.id.trim() || undefined,
        name: presetForm.name.trim(),
        prompt: presetForm.prompt,
        geminiSafety: presetForm.geminiSafety,
        kind: presetForm.kind.trim() || undefined,
        source: presetForm.source.trim() || undefined,
        keywords: presetForm.keywords,
        excludes: presetForm.excludes,
      })

      presetModalOpen = false
      await load()
    } catch (e) {
      presetError = errorMessage(e)
    } finally {
      presetSaving = false
    }
  }

  async function saveGlobalExcludes() {
    globalSaving = true
    globalStatus = ''
    try {
      const res = await api.saveJailbreakExcludes(globalExcludes)
      globalExcludes = res?.excludes || globalExcludes
      globalStatus = 'Global exclude list saved.'
    } catch (e) {
      globalStatus = 'Save failed: ' + errorMessage(e)
    } finally {
      globalSaving = false
    }
  }

  async function removePreset(id: string) {
    if (confirmDeleteId !== id) {
      confirmDeleteId = id
      return
    }
    confirmDeleteId = null
    try {
      await api.deleteJailbreakPreset(id)
      await load()
    } catch (e) {
      docStatus = 'Delete failed: ' + errorMessage(e)
    }
  }

  async function bindModel() {
    const id = bindModelId.trim()
    if (!id) {
      bindError = 'Enter a model, provider, or combo id.'
      return
    }
    bindError = ''
    try {
      await api.setJailbreakModel(id, bindPresetId, true)
      bindModelId = ''
      bindPresetId = ''
      await load()
    } catch (e) {
      bindError = errorMessage(e)
    }
  }

  async function toggleModel(id: string, cfg: JailbreakModelConfig) {
    try {
      await api.setJailbreakModel(id, cfg.presetId, !cfg.enabled)
      await load()
    } catch (e) {
      docStatus = 'Toggle failed: ' + errorMessage(e)
    }
  }

  async function unbindModel(id: string) {
    try {
      await api.setJailbreakModel(id, 'off', false)
      await load()
    } catch (e) {
      docStatus = 'Unbind failed: ' + errorMessage(e)
    }
  }

  function presetLabel(id: string): string {
    if (id === 'off') return 'Off'
    const p = presets.find((x) => x.id === id)
    return p ? p.name || p.id || id : id
  }

  let boundEntries = $derived(Object.entries(models))

  // A typed binding id that a preset keyword already covers needs no binding.
  let bindHint = $derived.by(() => {
    const id = bindModelId.trim()
    if (!id) return ''
    for (const preset of presets) {
      if (preset.keywords && matchesAnyJailbreakKeyword(id, preset.keywords)) {
        return `Keyword in "${preset.name || preset.id}" already covers this id — binding is optional.`
      }
    }
    return ''
  })
</script>

<div class="space-y-6">
  <div class="flex items-center gap-2">
    <span class="material-symbols-outlined text-brand-500">lock_open</span>
    <h1 class="text-xl font-semibold sm:text-2xl text-text-main">Jailbreak</h1>
    {#if loading}<span class="text-xs text-text-subtle">Loading…</span>{/if}
  </div>
  <p class="text-sm text-text-muted">
    Inject a system prompt ahead of the original content for chosen models, providers, or combos.
    Gemini models also get their safety filters relaxed when a binding has safety enabled.
  </p>

  <!-- Default document -->
  <section class="p-5 rounded-[14px] bg-surface border border-border space-y-4">
    <div class="flex items-center justify-between gap-2">
      <h2 class="text-sm font-bold text-text-main">Default document</h2>
      <span class="text-[11px] text-text-subtle">Used when a model is armed with no preset</span>
    </div>
    <label class="block space-y-1">
      <span class="text-xs text-text-muted">Source file name</span>
      <input
        type="text"
        bind:value={docSource}
        placeholder="agents.md"
        class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm outline-none focus:border-brand-500"
      />
    </label>
    <label class="block space-y-1">
      <span class="text-xs text-text-muted">Prompt</span>
      <textarea
        bind:value={docPrompt}
        rows="10"
        placeholder="System prompt injected before the original content…"
        class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm font-code outline-none focus:border-brand-500 resize-y"
      ></textarea>
    </label>
    <label class="flex items-center gap-2 cursor-pointer select-none">
      <input type="checkbox" bind:checked={docSafety} class="w-4 h-4 accent-brand-500" />
      <span class="text-sm text-text-main">Relax Gemini safety filters (BLOCK_NONE)</span>
    </label>
    <KeywordRules
      label="Keywords (include)"
      hint="Models matching one of these rules get this document — across every provider and combo leg. word = whole token, partial = substring, regex = RE2 pattern."
      rules={docKeywords}
      onchange={(next) => (docKeywords = next)}
    />
    <KeywordRules
      label="Excludes (deny)"
      hint="Models matching one of these rules never get this document, even when a keyword above matches."
      rules={docExcludes}
      onchange={(next) => (docExcludes = next)}
    />
    <div class="flex items-center gap-3">
      <button
        type="button"
        onclick={saveDoc}
        disabled={docSaving}
        class="px-4 py-2 rounded-[10px] bg-brand-500 text-white text-sm font-medium disabled:opacity-50 hover:bg-primary-hover"
      >
        {docSaving ? 'Saving…' : 'Save default'}
      </button>
      {#if docStatus}<span class="text-xs text-text-muted">{docStatus}</span>{/if}
    </div>
  </section>

  <!-- Global exclude list -->
  <section class="p-5 rounded-[14px] bg-surface border border-border space-y-4">
    <div class="flex items-center justify-between gap-2">
      <h2 class="text-sm font-bold text-text-main">Global exclude list</h2>
      <span class="text-[11px] text-text-subtle">Overrides every preset and binding</span>
    </div>
    <p class="text-xs text-text-subtle">
      A model id matching one of these rules is never injected, whatever any preset or exact binding
      says. Use it for the handful of models that must stay untouched.
    </p>
    <KeywordRules
      label="Excluded models"
      rules={globalExcludes}
      onchange={(next) => (globalExcludes = next)}
    />
    <div class="flex items-center gap-3">
      <button
        type="button"
        onclick={saveGlobalExcludes}
        disabled={globalSaving}
        class="px-4 py-2 rounded-[10px] bg-brand-500 text-white text-sm font-medium disabled:opacity-50 hover:bg-primary-hover"
      >
        {globalSaving ? 'Saving…' : 'Save exclude list'}
      </button>
      {#if globalStatus}<span class="text-xs text-text-muted">{globalStatus}</span>{/if}
    </div>
  </section>

  <!-- Import prompts: a repository, a URL, or a file you drop here -->
  <section
    class="p-5 rounded-[14px] bg-surface border border-border space-y-4 transition-colors {dragging
      ? 'border-brand-500 bg-surface-2'
      : ''}"
    ondragover={(e) => {
      e.preventDefault()
      dragging = true
    }}
    ondragleave={(e) => {
      if (e.currentTarget === e.target) dragging = false
    }}
    ondrop={onDrop}
    role="group"
    aria-label="Import prompts"
  >
    <div class="flex items-center justify-between gap-2">
      <h2 class="text-sm font-bold text-text-main">Import prompts</h2>
      <span class="text-[11px] text-text-subtle">
        {dragging ? 'Drop to import' : 'Repository, URL, or file'}
      </span>
    </div>
    <p class="text-xs text-text-subtle">
      Type or drop a source. A <span class="text-text-muted">repository</span> keeps one prompt per file
      — the shape collections such as <code class="text-text-muted">togg53192-cmd/jailbreaks</code> use —
      and every file becomes a preset. A <span class="text-text-muted">URL</span> is read as one prompt
      file. A <span class="text-text-muted">dropped file</span> is uploaded from your machine. In every
      case the name, the kind and the keywords come from the file name, so a preset covers that model on
      every provider and combo leg without arming each id. README files and dot-directories are skipped.
    </p>
    <div class="flex flex-wrap items-center gap-3">
      <input
        type="text"
        bind:value={importTarget}
        placeholder="owner/repo, owner/repo@branch, a github.com URL, or any http(s) URL"
        aria-label="Repository or URL to import"
        class="flex-1 min-w-[280px] px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-sm text-text-main placeholder:text-text-subtle"
      />
      <button
        type="button"
        onclick={importFromGithub}
        disabled={importing}
        class="px-4 py-2 rounded-[10px] bg-brand-500 text-white text-sm font-medium disabled:opacity-50 hover:bg-primary-hover"
      >
        {importing ? 'Importing…' : 'Import'}
      </button>
      {#if importStatus}<span class="text-xs text-text-muted">{importStatus}</span>{/if}
    </div>
    {#if importResult}
      {#if importResult.imported.length > 0}
        <div class="space-y-1">
          <h3 class="text-xs font-semibold text-text-main">Imported ({importResult.imported.length})</h3>
          <ul class="space-y-1">
            {#each importResult.imported as entry (entry.id)}
              <li class="text-xs text-text-muted">
                <span class="text-text-main">{entry.name}</span>
                {#if entry.kind}
                  <span
                    class="px-1.5 py-0.5 rounded-[6px] bg-surface-3 border border-border text-[10px] font-semibold uppercase text-text-main"
                    >{entry.kind}</span
                  >
                {/if}
                <span class="text-text-subtle"> · {entry.path}</span>
                {#if entry.keywords.length > 0}
                  <span class="text-text-subtle"> · matches {entry.keywords.join(', ')}</span>
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {/if}
      {#if importResult.refused.length > 0}
        <div class="space-y-1">
          <h3 class="text-xs font-semibold text-text-main">Skipped ({importResult.refused.length})</h3>
          <ul class="space-y-1">
            {#each importResult.refused as refusal (refusal.path)}
              <li class="text-xs text-text-muted">
                <span class="text-text-main">{refusal.path}</span>
                <span class="text-text-subtle"> · {refusal.reason}</span>
              </li>
            {/each}
          </ul>
        </div>
      {/if}
    {/if}
    <div class="pt-3 border-t border-border-subtle space-y-2">
      <div class="flex flex-wrap items-center gap-3">
        <span class="text-xs text-text-subtle">…or upload a prompt file</span>
        <input
          type="file"
          accept=".md,.markdown,.txt"
          onchange={uploadPresetFile}
          disabled={uploading}
          aria-label="Upload a prompt file"
          class="text-xs text-text-muted file:mr-3 file:px-3 file:py-1.5 file:rounded-[10px] file:border file:border-border file:bg-surface-2 file:text-text-main file:text-xs file:font-medium hover:file:bg-surface-3 disabled:opacity-50"
        />
        {#if uploading}<span class="text-xs text-text-muted">Uploading…</span>{/if}
      </div>
      <p class="text-[11px] text-text-subtle">
        The file name becomes the preset name and its keyword chain, the same as a GitHub import.
        To write a prompt by hand instead, use <span class="text-text-muted">+ New preset</span> below.
      </p>
    </div>
  </section>

  <!-- Presets -->
  <section class="p-5 rounded-[14px] bg-surface border border-border space-y-4">
    <div class="flex items-center justify-between">
      <h2 class="text-sm font-bold text-text-main">Presets</h2>
      <button
        type="button"
        onclick={openCreate}
        class="px-3 py-1.5 rounded-[10px] border border-border text-text-main text-xs font-medium hover:bg-surface-2"
      >
        + New preset
      </button>
    </div>
    {#if presets.length === 0}
      <p class="text-xs text-text-subtle">No presets yet.</p>
    {:else}
      <ul class="divide-y divide-border">
        {#each presets as preset (preset.id ?? preset.name)}
          <li class="flex items-center justify-between gap-3 py-2.5">
            <div class="min-w-0">
              <div class="flex items-center gap-2 min-w-0">
                <span class="text-sm text-text-main truncate">{preset.name || preset.id || 'Untitled'}</span>
                {#if preset.kind}
                  <span
                    class="px-1.5 py-0.5 rounded-[6px] bg-surface-3 border border-border text-[10px] font-semibold uppercase text-text-main shrink-0"
                    >{preset.kind}</span
                  >
                {/if}
              </div>
              <div class="text-[11px] text-text-subtle truncate">
                {preset.id}{preset.geminiSafety ? ' · safety' : ''}{preset.source ? ' · ' + preset.source : ''}{preset.keywords?.length
                  ? ' · ' + preset.keywords.length + ' keyword' + (preset.keywords.length > 1 ? 's' : '')
                  : ''}{preset.excludes?.length ? ' · ' + preset.excludes.length + ' excluded' : ''}
              </div>
            </div>
            <div class="flex items-center gap-2 shrink-0">
              <button type="button" onclick={() => openEdit(preset)} class="text-xs text-brand-500 hover:underline">Edit</button>
              {#if confirmDeleteId === (preset.id ?? '')}
                <button type="button" onclick={() => removePreset(preset.id ?? '')} class="text-xs text-error font-medium hover:underline">Confirm</button>
                <button type="button" onclick={() => (confirmDeleteId = null)} class="text-xs text-text-subtle hover:underline">Cancel</button>
              {:else}
                <button type="button" onclick={() => removePreset(preset.id ?? '')} class="text-xs text-text-muted hover:text-error hover:underline">Delete</button>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <!-- Model bindings -->
  <section class="p-5 rounded-[14px] bg-surface border border-border space-y-4">
    <h2 class="text-sm font-bold text-text-main">Model bindings</h2>
    <p class="text-xs text-text-subtle">
      Bind a model, provider, or combo id to a preset. The id is matched exactly as the client sends it.
      A keyword on a preset already covers every id it matches, so a binding is only needed to pick a
      different preset for one specific id.
    </p>
    <div class="flex flex-col sm:flex-row gap-2">
      <input
        type="text"
        bind:value={bindModelId}
        placeholder="model / provider / combo id"
        class="flex-1 px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm outline-none focus:border-brand-500"
      />
      <select
        bind:value={bindPresetId}
        class="px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm outline-none focus:border-brand-500"
      >
        <option value="">Default document</option>
        {#each presets as preset (preset.id ?? preset.name)}
          <option value={preset.id ?? ''}>{preset.name || preset.id}</option>
        {/each}
      </select>
      <button
        type="button"
        onclick={bindModel}
        class="px-4 py-2 rounded-[10px] bg-brand-500 text-white text-sm font-medium hover:bg-primary-hover"
      >
        Bind
      </button>
    </div>
    {#if bindError}<p class="text-xs text-error">{bindError}</p>{/if}
    {#if bindHint}<p class="text-xs text-text-muted">{bindHint}</p>{/if}
    {#if boundEntries.length === 0}
      <p class="text-xs text-text-subtle">No models armed.</p>
    {:else}
      <ul class="divide-y divide-border">
        {#each boundEntries as [id, cfg] (id)}
          <li class="flex items-center justify-between gap-3 py-2.5">
            <div class="min-w-0">
              <div class="text-sm text-text-main font-code truncate">{id}</div>
              <div class="text-[11px] text-text-subtle">{presetLabel(cfg.presetId)}</div>
            </div>
            <div class="flex items-center gap-3 shrink-0">
              <label class="flex items-center gap-1.5 cursor-pointer">
                <input
                  type="checkbox"
                  checked={cfg.enabled}
                  onchange={() => toggleModel(id, cfg)}
                  class="w-4 h-4 accent-brand-500"
                />
                <span class="text-xs text-text-muted">{cfg.enabled ? 'On' : 'Off'}</span>
              </label>
              <button type="button" onclick={() => unbindModel(id)} class="text-xs text-text-muted hover:text-error hover:underline">Unbind</button>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

{#if presetModalOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50" role="dialog" aria-modal="true">
    <div class="w-full max-w-lg p-5 rounded-[14px] bg-surface border border-border space-y-4 shadow-xl">
      <h3 class="text-sm font-bold text-text-main">{editingId ? 'Edit preset' : 'New preset'}</h3>
      <label class="block space-y-1">
        <span class="text-xs text-text-muted">ID</span>
        <input
          type="text"
          bind:value={presetForm.id}
          disabled={!!editingId}
          placeholder="expert-coder"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm outline-none focus:border-brand-500 disabled:opacity-50"
        />
      </label>
      <label class="block space-y-1">
        <span class="text-xs text-text-muted">Name</span>
        <input
          type="text"
          bind:value={presetForm.name}
          placeholder="Expert Coder"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm outline-none focus:border-brand-500"
        />
      </label>
      <label class="block space-y-1">
        <span class="text-xs text-text-muted">Prompt</span>
        <textarea
          bind:value={presetForm.prompt}
          rows="6"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm font-code outline-none focus:border-brand-500 resize-y"
        ></textarea>
      </label>
      <label class="block space-y-1">
        <span class="text-xs text-text-muted">Source file name</span>
        <input
          type="text"
          bind:value={presetForm.source}
          placeholder="expert.md"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm outline-none focus:border-brand-500"
        />
      </label>
      <label class="block space-y-1">
        <span class="text-xs text-text-muted">Kind</span>
        <input
          type="text"
          bind:value={presetForm.kind}
          list="jailbreak-kinds"
          placeholder="SOUL, PERSONA, AGENTS, LOAD…"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border text-text-main text-sm outline-none focus:border-brand-500"
        />
        <span class="text-[11px] text-text-subtle">
          The instruction-file convention this preset came from. Filled in automatically on import from
          a name such as SOUL.md or AGENTS.md; leave empty for a prompt about a model.
        </span>
      </label>
      <datalist id="jailbreak-kinds">
        {#each ['AGENTS', 'CLAUDE', 'GEMINI', 'CURSORRULES', 'WINDSURF', 'COPILOT', 'SOUL', 'PERSONA', 'IDENTITY', 'CHARACTER', 'LOAD', 'SYSTEM', 'RULES', 'POLICY', 'MODELFILE', 'CONFIG'] as kind (kind)}
          <option value={kind}></option>
        {/each}
      </datalist>
      <label class="flex items-center gap-2 cursor-pointer select-none">
        <input type="checkbox" bind:checked={presetForm.geminiSafety} class="w-4 h-4 accent-brand-500" />
        <span class="text-sm text-text-main">Relax Gemini safety filters</span>
      </label>
      <KeywordRules
        label="Keywords (include)"
        hint="Every model id matching one of these rules resolves to this preset — across providers and combo legs."
        rules={presetForm.keywords}
        onchange={(next) => (presetForm.keywords = next)}
      />
      <KeywordRules
        label="Excludes (deny)"
        hint="Models matching one of these rules never get this preset, even when a keyword above matches."
        rules={presetForm.excludes}
        onchange={(next) => (presetForm.excludes = next)}
      />
      {#if presetError}<p class="text-xs text-error">{presetError}</p>{/if}
      <div class="flex justify-end gap-2">
        <button type="button" onclick={() => (presetModalOpen = false)} class="px-4 py-2 rounded-[10px] border border-border text-text-main text-sm hover:bg-surface-2">Cancel</button>
        <button type="button" onclick={savePreset} disabled={presetSaving} class="px-4 py-2 rounded-[10px] bg-brand-500 text-white text-sm font-medium disabled:opacity-50 hover:bg-primary-hover">
          {presetSaving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  </div>
{/if}
