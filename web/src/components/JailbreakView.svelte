
<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type JailbreakDoc, type JailbreakImportEntry, type JailbreakImportResult, type JailbreakMatch, type JailbreakKeyword, type JailbreakModelConfig } from '../api/client'
  import { matchesAnyJailbreakKeyword } from '../lib/jailbreakKeywords'
  import { marked } from 'marked'
  import KeywordRules from './KeywordRules.svelte'
  import Pagination from './Pagination.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  // Preset library.
  let presets = $state<JailbreakDoc[]>([])
  let loading = $state(false)

  // Preset editor.
  let presetModalOpen = $state(false)
  let editingId = $state<string | null>(null)
  // The form is a subset of JailbreakDoc: id and name are editable here, the
  // timestamps are the store's. Typing it against the doc rather than letting
  // it infer keeps `order` — which the API accepts and the list sorts by —
  // from being a type error the moment it is read.
  let presetForm = $state<{
    id: string
    name: string
    prompt: string
    kind: string
    geminiSafety: boolean
    source: string
    keywords: JailbreakKeyword[]
    excludes: JailbreakKeyword[]
    order: number
  }>({
    id: '',
    name: '',
    prompt: '',
    kind: '',
    geminiSafety: false,
    source: '',
    keywords: [],
    excludes: [],
    order: 0,
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
  let globalExcludes = $state<JailbreakKeyword[]>([])
  let globalSaving = $state(false)
  let globalStatus = $state('')

  // Repository / URL import. Several URLs may be pasted at once, one per
  // line; each becomes its own preset. The first line is also what the
  // single-line field shows, so pasting many lines is not a separate mode.
  let importTarget = $state('')
  let importing = $state(false)
  let importStatus = $state('')
  let importResult = $state<JailbreakImportResult | null>(null)
  let importFileName = $state('')
  // Picked .md files waiting to be uploaded. Each entry keeps the File so the
  // upload can read it, and the list is rendered with a remove button per row.
  let pickedFiles = $state<File[]>([])
  let uploading = $state(false)
  let dragging = $state(false)
  // #3 Test-match: type a model id, see which preset wins and why.
  let testMatchId = $state('')
  // The test-match panel is a modal, not a card of its own: it is used
  // occasionally, and a full card cost as much vertical space as the 53-row
  // preset table above it.
  let testMatchOpen = $state(false)
  let testMatchLoading = $state(false)
  let testMatchResult = $state<JailbreakMatch | null>(null)
  let testMatchError = $state('')
  // #2 Payload preview: the exact outbound body one request would carry.
  let previewOpen = $state(false)
  let previewLoading = $state(false)
  let previewError = $state('')
  let previewData = $state<{
    injected: boolean
    reason: string
    presetId?: string
    presetName?: string
    prompt?: string
    messages: { role: string; content: string }[]
  } | null>(null)
  // Add binding: one modal with two tabs — a single id typed by hand, or
  // many ids picked from the live catalog. The card no longer carries either
  // form inline, so the list below keeps its full height.
  let bindModalOpen = $state(false)
  let bindModalTab = $state<'single' | 'many'>('single')
  // #6 Bulk arm: pick several ids from the live catalog, bind them at once.
  let bulkArmSearch = $state('')
  let bulkArmModels = $state<string[]>([])
  let bulkArmModelsLoading = $state(false)
  let bulkArmPicked = $state<Set<string>>(new Set())
  let bulkArmPresetId = $state('')
  let bulkArmPresetSearch = $state('')
  let bulkArmPresetDropdownOpen = $state(false)
  let bulkArmSaving = $state(false)
  let bulkArmStatus = $state('')
  // Import log rendered as one line per file, so a 34-file import reads like a
  // terminal transcript rather than a nested list.
  // The URLs the operator pasted, one per line. Blank lines are dropped, so a
  // trailing newline from a copy-paste does not become an empty target.
  let importUrls = $derived(
    importTarget
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
  )
  // A suggested file name for the first URL, derived from its host and path:
  // github.com/owner/repo -> repo.md, paste.example.com/abc123 -> abc123.md.
  // It fills the placeholder only while the field is empty, so an operator who
  // typed a name keeps it.
  let importNameSuggestion = $derived.by(() => {
    const first = importUrls[0]
    if (!first) return ''
    try {
      const u = new URL(first)
      const seg = u.pathname.split('/').filter(Boolean).pop()
      const base = (seg || u.hostname).replace(/\.[a-z]{2,4}$/i, '')
      return base ? base + '.md' : u.hostname + '.md'
    } catch {
      return ''
    }
  })
  let importLogText = $derived.by(() => {
    if (!importResult) return ''
    const lines: string[] = []
    for (const e of importResult.imported) {
      const kw = e.keywords?.length ? `  matches: ${e.keywords.join(', ')}` : ''
      const kind = e.kind ? `  [${e.kind}]` : ''
      lines.push(`+ ${e.name}${kind}  ${e.path}${kw}`)
    }
    for (const f of importResult.refused) {
      lines.push(`- ${f.path}  ${f.reason}`)
    }
    return lines.join('\n')
  })
  // The preset modal carries three tabs: writing one preset by hand, or
  // filling many at once from files or URLs. The card that used to hold the
  // import paths is gone, so this is the only way in.
  type PresetModalTab = 'write' | 'files' | 'source'
  let presetModalTab = $state<PresetModalTab>('write')
  // #8 Prompt preview inside the preset editor.
  let promptPreviewOpen = $state(false)
  // #5 Per-preset usage counters, filled by the usage endpoint.
  let presetUsage = $state<Record<string, number>>({})
  // #1 Drag-reorder: the id being dragged, and the id it hovers over.
  // Pagination + batch selection for the preset list.
  let presetPage = $state(1)
  let presetPageSize = $state(10)
  let presetSearch = $state('')
  let selectedPresetIds = $state<Set<string>>(new Set())
  let batchBusy = $state(false)
  let batchStatus = $state('')
  // Batch selection for the model-binding list.
  let selectedBindingIds = $state<Set<string>>(new Set())
  let bindingBatchBusy = $state(false)
  let bindingBatchStatus = $state('')
  let bindingBatchPresetId = $state('')
  let bindingPage = $state(1)
  let bindingPageSize = $state(10)
  let bindingSearch = $state('')

  onMount(load)
  async function load() {
    loading = true
    try {
      const [presetList, modelMap, excludeList] = await Promise.all([
        api.listJailbreakPresets(),
        api.listJailbreakModels(),
        api.getJailbreakExcludes(),
      ])
      presets = Array.isArray(presetList) ? presetList : []
      models = modelMap || {}
      globalExcludes = excludeList?.excludes || []
      // #5 Per-preset hit counters. A failure here is cosmetic, so it is
      // swallowed: the preset list still renders without the counts.
      try {
        const usageRes = await api.getJailbreakUsage()
        presetUsage = usageRes?.usage || {}
      } catch {}
    } catch (e) {
      importStatus = 'Load failed: ' + errorMessage(e)
    } finally {
      loading = false
    }
  }
  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  async function importFromGithub() {
    const urls = importUrls
    if (urls.length === 0) {
      importStatus = 'Enter a repository or URL first, e.g. togg53192-cmd/jailbreaks'
      return
    }
    importing = true
    importStatus = ''
    importResult = null
    const imported: JailbreakImportEntry[] = []
    const refused: { path: string; reason: string }[] = []
    for (const url of urls) {
      try {
        // The custom name applies to the first URL only: with several URLs
        // each preset is named after its own source, which is the only way to
        // tell them apart.
        const name = urls.length === 1 ? importFileName : ''
        const res = await api.importJailbreak(url, name)
        imported.push(...res.imported)
        refused.push(...res.refused)
      } catch (e) {
        refused.push({ path: url, reason: errorMessage(e) })
      }
    }
    importResult = { ok: refused.length === 0, source: urls.join(', '), imported, refused }
    importStatus =
      refused.length === 0
        ? `Imported ${imported.length} preset(s).`
        : `Imported ${imported.length}, skipped ${refused.length}.`
    importing = false
    await load()
  }
  // Picking files only queues them; the Upload button sends the whole queue.
  // A file already queued under the same name+size is skipped, so picking the
  // same file twice does not upload it twice.
  function queueFiles(fileList: FileList | null) {
    if (!fileList) return
    for (const file of Array.from(fileList)) {
      const dupe = pickedFiles.some((f) => f.name === file.name && f.size === file.size)
      if (!dupe) pickedFiles = [...pickedFiles, file]
    }
  }
  function removePickedFile(index: number) {
    pickedFiles = pickedFiles.filter((_, i) => i !== index)
  }
  function clearPickedFiles() {
    pickedFiles = []
  }
  async function uploadPickedFiles() {
    if (pickedFiles.length === 0) return
    uploading = true
    importStatus = ''
    importResult = null
    const imported: JailbreakImportEntry[] = []
    const refused: { path: string; reason: string }[] = []
    for (const file of pickedFiles) {
      try {
        const res = await api.uploadJailbreakPreset(file)
        imported.push(res.preset)
      } catch (e) {
        refused.push({ path: file.name, reason: errorMessage(e) })
      }
    }
    importResult = { ok: refused.length === 0, source: 'upload', imported, refused }
    importStatus =
      refused.length === 0
        ? `Uploaded ${imported.length} preset(s).`
        : `Uploaded ${imported.length}, skipped ${refused.length}.`
    pickedFiles = []
    uploading = false
    await load()
  }
  // The drop zone accepts files (queued for upload) or a URL (imported directly).
  function onDrop(event: DragEvent) {
    event.preventDefault()
    dragging = false
    if (event.dataTransfer?.files?.length) {
      queueFiles(event.dataTransfer.files)
      return
    }
    const text = event.dataTransfer?.getData('text/uri-list') || event.dataTransfer?.getData('text/plain') || ''
    const url = text.split('\n')[0].trim()
    if (url) {
      importTarget = url
      void importFromGithub()
    }
  }

  // #3 Test-match: ask the gateway which preset wins for a typed id.
  async function runTestMatch() {
    const id = testMatchId.trim()
    if (!id) {
      testMatchError = 'Enter a model, provider, or combo id.'
      return
    }
    testMatchLoading = true
    testMatchError = ''
    testMatchResult = null
    try {
      testMatchResult = await api.explainJailbreak(id)
    } catch (e) {
      testMatchError = errorMessage(e)
    } finally {
      testMatchLoading = false
    }
  }
  // #2 Payload preview: build the exact outbound body for the tested id.
  async function togglePreview() {
    if (previewOpen) {
      previewOpen = false
      return
    }
    previewOpen = true
    if (!testMatchId.trim()) return
    previewLoading = true
    previewError = ''
    previewData = null
    try {
      previewData = await api.previewJailbreak(testMatchId.trim(), [
        { role: 'user', content: 'Example user message.' },
      ])
    } catch (e) {
      previewError = errorMessage(e)
    } finally {
      previewLoading = false
    }
  }
  // Opens the add-binding modal on the requested tab. The guard matters: this
  // was once wired straight to onclick, which hands the MouseEvent in as the
  // tab, leaving both tabs unselected and the panel defaulting to Bulk.
  function openBindModal(tab: 'single' | 'many' = 'single') {
    bindModalTab = tab === 'many' ? 'many' : 'single'
    bindModalOpen = true
    bindError = ''
    bindingEditId = ''
    // Reopening shows the current selection, not a stale search string.
    bindPresetSearch = ''
    bindPresetDropdownOpen = false
  }
  // #6 Bulk arm: load the live model catalog once, then bind the picked ids.
  // It no longer opens a modal of its own — the 'many' tab of the shared
  // add-binding modal renders it, and switching to that tab is what triggers
  // the load: the operator can reach Bulk from Single without reopening the
  // modal, so opening it is not a reliable trigger.
  async function openBulkArm() {
    bulkArmStatus = ''
    bulkArmPicked = new Set()
    if (bulkArmModels.length > 0 || bulkArmModelsLoading) return
    bulkArmModelsLoading = true
    try {
      const res = await api.getJailbreakModelCatalog()
      bulkArmModels = Array.isArray(res.models) ? res.models : []
    } catch (e) {
      bulkArmStatus = 'Failed to load models: ' + errorMessage(e)
    } finally {
      bulkArmModelsLoading = false
    }
  }
  // The Bulk tab is reachable by clicking it from Single, so the load is tied
  // to the tab being visible rather than to the modal opening.
  $effect(() => {
    if (bindModalOpen && bindModalTab === 'many') void openBulkArm()
  })
  // Close the single-bind preset combobox on a click anywhere outside it —
  // modal padding, the title bar, the tab strip, the page behind the overlay.
  // A document listener rather than a wrapper onclick, because the wrapper
  // only sees clicks that bubble through the panel, and a click on the modal's
  // own padding or title never does. A blur handler is not an option: blur
  // fires before the option's click and would cancel the pick.
  $effect(() => {
    if (!bindPresetDropdownOpen) return
    const onDocClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null
      if (target?.closest('[data-preset-combobox]')) return
      bindPresetDropdownOpen = false
    }
    // capture: the overlay's own click-to-close handler runs on the same
    // event, and in the bubble phase it may already have unmounted this
    // component before the listener is removed.
    document.addEventListener('click', onDocClick, true)
    return () => document.removeEventListener('click', onDocClick, true)
  })
  // Single-bind preset combobox: a keyword search over name and id, so a
  // 50-preset library stays navigable without scrolling a <select>.
  let bindPresetSearch = $state('')
  let bindPresetDropdownOpen = $state(false)
  // Editing one existing binding reuses the Add modal's Single tab: the id
  // field is disabled and the on/off checkbox appears, which is the whole
  // difference. A second modal for the same two controls was the alternative,
  // and it duplicated the combobox, the outside-click handler, and the footer.
  let bindingEditId = $state('')
  let bindingEditEnabled = $state(true)
  function openBindingEdit(id: string, cfg: JailbreakModelConfig) {
    bindingEditId = id
    bindingEditEnabled = cfg.enabled
    bindModelId = id
    bindPresetId = cfg.presetId || ''
    bindPresetSearch = ''
    bindPresetDropdownOpen = false
    bindError = ''
    bindModalTab = 'single'
    bindModalOpen = true
  }
  let bindPresetOptions = $derived.by(() => {
    const q = bindPresetSearch.trim().toLowerCase()
    if (!q) return presets.slice(0, 50)
    return presets
      .filter((p) => (p.name || '').toLowerCase().includes(q) || (p.id || '').toLowerCase().includes(q))
      .slice(0, 50)
  })
  // The label of whatever is currently bound, so reopening the modal shows the
  // selection instead of an empty field.
  let bindPresetLabel = $derived.by(() => {
    if (!bindPresetId) return 'Default document'
    const p = presets.find((x) => x.id === bindPresetId)
    return p ? p.name || p.id || bindPresetId : bindPresetId
  })
  function toggleBulkArmPick(id: string) {
    const next = new Set(bulkArmPicked)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    bulkArmPicked = next
  }
  // Bulk-arm filter: plain substring by default, or an RE2 pattern when the
  // query starts with "/" — the same convention the keyword rules use, so an
  // operator who already writes `^deepseek` there does not relearn it here.
  // A pattern that fails to compile falls back to substring with a note, so a
  // half-typed regex never leaves the list empty and silent.
  let bulkArmPresetOptions = $derived.by(() => {
    const q = bulkArmPresetSearch.trim().toLowerCase()
    if (!q) return presets.slice(0, 50)
    return presets
      .filter((p) => (p.name || '').toLowerCase().includes(q) || (p.id || '').toLowerCase().includes(q))
      .slice(0, 50)
  })
  // The message shown under the search box. Derived rather than assigned, so
  // nothing writes to state while a value is being computed.
  let bulkArmRegexError = $derived.by(() => {
    const q = bulkArmSearch.trim()
    if (!q.startsWith('/') || bulkArmRegexValid) return ''
    try {
      new RegExp(q.slice(1), 'i')
      return ''
    } catch (e) {
      return `Regex invalid: ${e instanceof Error ? e.message : String(e)}`
    }
  })
  // The regex validity check runs as its own derived, and the filter reads it.
  // Writing bulkArmRegexError from inside bulkArmFiltered used to be the shape
  // here, and it silently broke the filter: a $derived.by that writes to
  // another $state is not re-tracked, so the list never recomputed — the input
  // accepted text, the count stayed at 200, and nothing filtered.
  let bulkArmRegexValid = $derived.by(() => {
    const q = bulkArmSearch.trim()
    if (!q.startsWith('/')) return true
    try {
      new RegExp(q.slice(1), 'i')
      return true
    } catch {
      return false
    }
  })
  let bulkArmFiltered = $derived.by(() => {
    const q = bulkArmSearch.trim()
    if (!q) return bulkArmModels.slice(0, 200)
    if (q.startsWith('/')) {
      if (!bulkArmRegexValid) return []
      const re = new RegExp(q.slice(1), 'i')
      return bulkArmModels.filter((m) => re.test(m)).slice(0, 200)
    }
    const lower = q.toLowerCase()
    return bulkArmModels.filter((m) => m.toLowerCase().includes(lower)).slice(0, 200)
  })
  // Select every id the current filter matches — the regex/substring query is
  // the selector, so "arm everything matching /^deepseek/" is one click.
  function selectAllBulkArmFiltered() {
    const next = new Set(bulkArmPicked)
    for (const id of bulkArmFiltered) next.add(id)
    bulkArmPicked = next
  }
  async function saveBulkArm() {
    const ids = Array.from(bulkArmPicked)
    if (ids.length === 0) {
      bulkArmStatus = 'Pick at least one model id.'
      return
    }
    bulkArmSaving = true
    bulkArmStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.setJailbreakModel(id, bulkArmPresetId || 'default', true)
        ok++
      } catch (e) {
        failures.push(id)
      }
    }
    bulkArmStatus =
      failures.length === 0
        ? `Armed ${ok} model(s).`
        : `Armed ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    bulkArmSaving = false
    if (ok > 0) {
      bulkArmPicked = new Set()
      await load()
      bindModalOpen = false
    }
  }
  // Pagination + batch selection over the model-binding list.
  let filteredBindings = $derived.by(() => {
    const q = bindingSearch.trim().toLowerCase()
    if (!q) return boundEntries
    return boundEntries.filter(
      ([id, cfg]) =>
        id.toLowerCase().includes(q) ||
        presetLabel(cfg.presetId).toLowerCase().includes(q),
    )
  })
  let bindingPageCount = $derived(Math.max(1, Math.ceil(filteredBindings.length / bindingPageSize)))
  let pagedBindings = $derived.by(() => {
    const start = (bindingPage - 1) * bindingPageSize
    return filteredBindings.slice(start, start + bindingPageSize)
  })
  $effect(() => {
    if (bindingPage > bindingPageCount) bindingPage = bindingPageCount
  })
  function toggleBindingSelect(id: string) {
    const next = new Set(selectedBindingIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selectedBindingIds = next
  }
  function toggleSelectAllBindingsOnPage() {
    const ids = pagedBindings.map(([id]) => id)
    const allSelected = ids.length > 0 && ids.every((id) => selectedBindingIds.has(id))
    const next = new Set(selectedBindingIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
    selectedBindingIds = next
  }
  // Select all reaches every binding the filter matches, not just the page.
  function toggleSelectAllBindingsFiltered() {
    const ids = filteredBindings.map(([id]) => id)
    const allSelected = ids.length > 0 && ids.every((id) => selectedBindingIds.has(id))
    const next = new Set(selectedBindingIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
    selectedBindingIds = next
  }
  function clearBindingSelection() {
    selectedBindingIds = new Set()
  }
  // Batch menu for bindings: turn the picked ids on or off, rebind them to one
  // preset, or unbind them. Each step reports per-item failures so a partial
  // batch still tells the operator what happened.
  async function batchSetBindingsEnabled(enabled: boolean) {
    const ids = Array.from(selectedBindingIds)
    if (ids.length === 0) return
    bindingBatchBusy = true
    bindingBatchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      const cfg = models[id]
      if (!cfg) continue
      try {
        await api.setJailbreakModel(id, cfg.presetId, enabled)
        ok++
      } catch {
        failures.push(id)
      }
    }
    bindingBatchStatus =
      failures.length === 0
        ? `${enabled ? 'Enabled' : 'Disabled'} ${ok} binding(s).`
        : `Updated ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedBindingIds = new Set()
    bindingBatchBusy = false
    await load()
  }
  async function batchRebindPreset() {
    const ids = Array.from(selectedBindingIds)
    const preset = bindingBatchPresetId
    if (ids.length === 0 || !preset) {
      bindingBatchStatus = 'Pick ids and a preset first.'
      return
    }
    bindingBatchBusy = true
    bindingBatchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.setJailbreakModel(id, preset, true)
        ok++
      } catch {
        failures.push(id)
      }
    }
    bindingBatchStatus =
      failures.length === 0
        ? `Rebound ${ok} binding(s).`
        : `Rebound ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedBindingIds = new Set()
    bindingBatchBusy = false
    await load()
  }
  async function batchUnbindModels() {
    const ids = Array.from(selectedBindingIds)
    if (ids.length === 0) return
    bindingBatchBusy = true
    bindingBatchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.deleteJailbreakModel(id)
        ok++
      } catch {
        failures.push(id)
      }
    }
    bindingBatchStatus =
      failures.length === 0
        ? `Unbound ${ok} model(s).`
        : `Unbound ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedBindingIds = new Set()
    bindingBatchBusy = false
    await load()
  }
  // Pagination + batch selection over the preset list.
  let filteredPresets = $derived.by(() => {
    const q = presetSearch.trim().toLowerCase()
    if (!q) return sortedPresets
    return sortedPresets.filter(
      (p) =>
        (p.name || '').toLowerCase().includes(q) ||
        (p.id || '').toLowerCase().includes(q) ||
        (p.kind || '').toLowerCase().includes(q),
    )
  })
  let presetPageCount = $derived(Math.max(1, Math.ceil(filteredPresets.length / presetPageSize)))
  let pagedPresets = $derived.by(() => {
    const start = (presetPage - 1) * presetPageSize
    return filteredPresets.slice(start, start + presetPageSize)
  })
  // Keep the page in range when the list shrinks (a batch delete, or a filter
  // that matches fewer rows than the current page holds).
  $effect(() => {
    if (presetPage > presetPageCount) presetPage = presetPageCount
  })
  function togglePresetSelect(id: string) {
    const next = new Set(selectedPresetIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selectedPresetIds = next
  }
  function toggleSelectAllOnPage() {
    const ids = pagedPresets.map((p) => p.id ?? p.name ?? '').filter(Boolean)
    const allSelected = ids.every((id) => selectedPresetIds.has(id))
    const next = new Set(selectedPresetIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
    selectedPresetIds = next
  }
  // Select all reaches every preset the current filter matches, not just the
  // visible page — the two controls sit side by side so the difference is
  // obvious: "in page" is scoped, "all" is the whole filtered set.
  function toggleSelectAllFiltered() {
    const ids = filteredPresets.map((p) => p.id ?? p.name ?? '').filter(Boolean)
    const allSelected = ids.length > 0 && ids.every((id) => selectedPresetIds.has(id))
    const next = new Set(selectedPresetIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
  }
  function clearPresetSelection() {
    selectedPresetIds = new Set()
  }
  // Batch menu: delete, or arm every model each selected preset's keywords
  // already cover. Both report per-item failures rather than stopping at the
  // first one, so a partial batch still tells the operator what happened.
  async function batchDeletePresets() {
    const ids = Array.from(selectedPresetIds)
    if (ids.length === 0) return
    batchBusy = true
    batchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.deleteJailbreakPreset(id)
        ok++
      } catch (e) {
        failures.push(id)
      }
    }
    batchStatus =
      failures.length === 0
        ? `Deleted ${ok} preset(s).`
        : `Deleted ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedPresetIds = new Set()
    batchBusy = false
    await load()
  }
  async function batchExportPresets() {
    const ids = Array.from(selectedPresetIds)
    if (ids.length === 0) return
    const chosen = presets.filter((p) => ids.includes(p.id ?? p.name ?? ''))
    const blob = new Blob([JSON.stringify(chosen, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `jailbreak-presets-${chosen.length}.json`
    a.click()
    URL.revokeObjectURL(url)
    batchStatus = `Exported ${chosen.length} preset(s).`
  }
  // Sorting for the preset list. The default is name A→Z; the other modes are
  // what an operator reaches for in a long library — newest first to find what
  // was just added, recently updated to find what was just edited, most-used to
  // see what actually earns its keep, and kind to group the instruction files.
  let presetSort = $state<'name-asc' | 'name-desc' | 'newest' | 'updated' | 'usage' | 'kind'>('name-asc')
  let sortedPresets = $derived.by(() => {
    const list = [...presets]
    const byName = (a: JailbreakDoc, b: JailbreakDoc) =>
      (a.name || a.id || '').localeCompare(b.name || b.id || '')
    if (presetSort === 'name-desc') return list.sort((a, b) => byName(b, a))
    if (presetSort === 'newest') {
      return list.sort((a, b) => Date.parse(b.createdAt || '0') - Date.parse(a.createdAt || '0') || byName(a, b))
    }
    if (presetSort === 'updated') {
      return list.sort((a, b) => Date.parse(b.updatedAt || '0') - Date.parse(a.updatedAt || '0') || byName(a, b))
    }
    if (presetSort === 'usage') {
      return list.sort((a, b) => (presetUsage[b.id ?? ''] ?? 0) - (presetUsage[a.id ?? ''] ?? 0) || byName(a, b))
    }
    if (presetSort === 'kind') {
      return list.sort((a, b) => (a.kind || 'zzz').localeCompare(b.kind || 'zzz') || byName(a, b))
    }
    return list.sort(byName)
  })
  // #4 Duplicate a preset with its rules.
  async function duplicatePreset(id: string) {
    try {
      await api.duplicateJailbreakPreset(id)
      await load()
    } catch (e) {
      importStatus = 'Duplicate failed: ' + errorMessage(e)
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
      order: 0,
    }
    presetError = ''
    presetModalTab = 'write'
    promptPreviewOpen = false
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
      order: preset.order || 0,
    }
    presetError = ''
    promptPreviewOpen = false
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
        order: presetForm.order || undefined,
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
      importStatus = 'Delete failed: ' + errorMessage(e)
    }
  }

  async function bindModel() {
    const id = bindModelId.trim()
    if (!id) {
      bindError = 'Enter a model, provider, or combo id.'
      return
    }
    bindError = ''
    // Editing keeps the id and honours the on/off checkbox; creating a new
    // binding always switches it on.
    const enabled = bindingEditId ? bindingEditEnabled : true
    try {
      await api.setJailbreakModel(id, bindPresetId, enabled)
      bindModelId = ''
      bindPresetId = ''
      bindingEditId = ''
      await load()
      // The list below is the proof the binding landed, so the modal closes on
      // success and stays open on failure with the reason in view.
      bindModalOpen = false
    } catch (e) {
      bindError = errorMessage(e)
    }
  }

  async function toggleModel(id: string, cfg: JailbreakModelConfig) {
    try {
      await api.setJailbreakModel(id, cfg.presetId, !cfg.enabled)
      await load()
    } catch (e) {
      importStatus = 'Toggle failed: ' + errorMessage(e)
    }
  }

  async function unbindModel(id: string) {
    try {
      await api.deleteJailbreakModel(id)
      await load()
    } catch (e) {
      bindingBatchStatus = 'Unbind failed: ' + errorMessage(e)
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
  <!-- No page header: the TopBar already carries the page title and description. -->
  {#if loading}<span class="text-xs text-text-subtle">Loading…</span>{/if}

  <!-- Summary strip: label above, number below, description last — the same
       reading order SecurityPanel uses, so the two pages scan identically. -->
  <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
    <div class="rounded-[14px] border border-border-subtle bg-surface p-4 shadow-[var(--shadow-soft)]">
      <div class="font-code text-[10px] uppercase tracking-wider text-text-subtle">Presets</div>
      <div class="mt-1 text-2xl font-semibold text-text-main">{presets.length}</div>
      <p class="mt-1 text-xs text-text-muted">Prompt presets in the library</p>
    </div>
    <div class="rounded-[14px] border border-border-subtle bg-surface p-4 shadow-[var(--shadow-soft)]">
      <div class="font-code text-[10px] uppercase tracking-wider text-text-subtle">Bindings armed</div>
      <div class="mt-1 text-2xl font-semibold text-text-main">
        {Object.values(models).filter((c) => c.enabled).length}
        <span class="text-sm font-normal text-text-subtle">/ {Object.keys(models).length}</span>
      </div>
      <p class="mt-1 text-xs text-text-muted">Exact bindings currently injecting</p>
    </div>
    <div class="rounded-[14px] border border-border-subtle bg-surface p-4 shadow-[var(--shadow-soft)]">
      <div class="font-code text-[10px] uppercase tracking-wider text-text-subtle">Injections</div>
      <div class="mt-1 text-2xl font-semibold text-text-main">
        {Object.values(presetUsage).reduce((a, b) => a + b, 0)}
      </div>
      <p class="mt-1 text-xs text-text-muted">Total prompts prepended, all presets</p>
    </div>
  </div>
  <!-- How it works: read-once reference, so it carries the lightest
       visual weight of any card on the page. -->
  <Card padding="sm" class="bg-surface-2/60">
    <details class="group">
      <summary class="cursor-pointer flex items-center gap-2 text-sm font-bold text-text-main">
        <span class="material-symbols-outlined text-text-subtle text-[18px]">info</span>
        How it works
        <span class="material-symbols-outlined text-text-subtle text-[16px] ml-auto transition-transform group-open:rotate-180">expand_more</span>
      </summary>
      <div class="pt-3 space-y-3 text-xs text-text-muted leading-relaxed">
        <p>
          This feature prepends a <span class="text-text-main">system prompt</span> system prompt ahead of the original content for the models, providers, or combos you choose.
          Injection is <span class="text-text-main">opt-in</span> and off by default: a model is never injected just because it exists, only because you armed it.
        </p>
        <p>
          A persona is simply a system prompt of your own — nothing about the mechanism limits what it
          may say. Because it is prepended ahead of the model's original instructions, it is also the
          usual way to apply <span class="text-text-main">jailbreak prompting</span>: a persona that
          reframes the assistant's role, drops its refusals, or replaces its rules entirely is written
          here and armed exactly like any other. The gateway does not judge the content, it only
          delivers it to the models you chose.
        </p>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold uppercase tracking-wide text-text-subtle">Three ways to arm a preset</div>
          <ul class="space-y-1.5 pl-4">
            <li class="flex gap-2">
              <span class="text-brand-500 shrink-0">1.</span>
              <span><span class="text-text-main">Exact binding</span> — bind one id to one preset. The precise tool: use it when one id needs a different preset from the rest of its family, or when a whole combo should be armed regardless of its legs.</span>
            </li>
            <li class="flex gap-2">
              <span class="text-brand-500 shrink-0">2.</span>
              <span><span class="text-text-main">Keyword (include)</span> — one rule covers every model id that matches, across providers and combo legs. Three modes: <span class="font-code">word</span> (whole token, default), <span class="font-code">partial</span> (substring), <span class="font-code">regex</span> (RE2).</span>
            </li>
            <li class="flex gap-2">
              <span class="text-brand-500 shrink-0">3.</span>
              <span><span class="text-text-main">Excludes (deny)</span> — two deny lists: per preset (a model matching a keyword still never gets this preset) and global (never injected at all, outranking everything).</span>
            </li>
          </ul>
        </div>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold uppercase tracking-wide text-text-subtle">Precedence</div>
          <ol class="space-y-1 pl-4 list-decimal">
            <li>Global exclude list — a match means no injection, whatever anything else says.</li>
            <li>Exact binding on the id the client sent.</li>
            <li>Exclude of the winning preset.</li>
            <li>Keyword on the primary id, before a keyword on a forwarded leg.</li>
            <li>Among keyword hits on the same id, the longest wins; a tie goes to the lowest preset id.</li>
          </ol>
        </div>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold uppercase tracking-wide text-text-subtle">Filling a preset</div>
          <ul class="space-y-1 pl-4">
            <li class="flex gap-2"><span class="text-brand-500 shrink-0">•</span><span><span class="text-text-main">New preset</span> — one modal, three tabs: <span class="text-text-muted">Write</span> builds one preset field by field, <span class="text-text-muted">From files</span> picks or drops several at once, <span class="text-text-muted">From URL</span> pulls a repository or any http(s) address. The file name becomes the preset name and its keyword chain, so <span class="font-code">SOUL.md</span>, <span class="font-code">AGENTS.md</span>, <span class="font-code">CLAUDE.md</span> are labelled with their kind right away.</span></li>
          </ul>
        </div>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold uppercase tracking-wide text-text-subtle">Checking without guessing</div>
          <ul class="space-y-1 pl-4">
            <li class="flex gap-2"><span class="text-brand-500 shrink-0">•</span><span><span class="text-text-main">Test match</span> — type an id and the page answers which preset wins and why (exact-binding, keyword, global-exclude, preset-exclude, or no-match), plus a preview of the final payload.</span></li>
            <li class="flex gap-2"><span class="text-brand-500 shrink-0">•</span><span><span class="text-text-main">Add</span> — one modal, two tabs: <span class="text-text-muted">Single</span> types one id by hand, <span class="text-text-muted">Bulk</span> picks many ids straight from the live catalog (1500+ models) and binds them all to one preset.</span></li>
            <li class="flex gap-2"><span class="text-brand-500 shrink-0">•</span><span><span class="text-text-main">Statistik pemakaian</span> — every preset shows how many times it actually injected.</span></li>
          </ul>
        </div>
        <p class="text-[11px] text-text-subtle">
          Limits: prompt 512 KB, at most 20 rules per list, rule length 200 characters. A rule the gateway would reject is refused at save time with the reason, so a typo never becomes a preset that silently stops matching.
        </p>
      </div>
    </details>
  </Card>
  <!-- Global exclude list. The save action sits in the header beside the
       title, the same place New preset sits in the Presets card, so the two
       cards read with one convention. The rules below own the full width. -->
  <Card padding="sm" class="bg-surface-2/60">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="space-y-1">
        <h2 class="text-sm font-bold text-text-main">Global exclude list</h2>
        <p class="text-[11px] text-text-subtle">Overrides every preset and binding.</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        {#if globalStatus}<span class="text-xs text-text-muted">{globalStatus}</span>{/if}
        <Button variant="primary" size="sm" onclick={saveGlobalExcludes} disabled={globalSaving} icon="save">
          {globalSaving ? 'Saving…' : 'Save exclude list'}
        </Button>
      </div>
    </div>
    <p class="text-xs text-text-subtle pt-2">
      A model id matching one of these rules is never injected, whatever any preset or exact binding
      says. Use it for the handful of models that must stay untouched.
    </p>
    <div class="pt-3">
      <KeywordRules
        label="Excluded models"
        rules={globalExcludes}
        onchange={(next) => (globalExcludes = next)}
      />
    </div>
  </Card>


  <!-- Presets: the primary workspace. It gets the heaviest card on the page —
       full padding, no muted background, and the list itself is flush to the
       card edges so a 53-row table reads as one surface rather than rows
       floating inside a box. -->
  <Card padding="md" class="overflow-hidden">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="text-sm font-bold text-text-main">
        Presets <span class="text-text-subtle font-normal">({filteredPresets.length})</span>
      </h2>
      <div class="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" onclick={() => (testMatchOpen = true)} icon="troubleshoot">
          Test match
        </Button>
        <Button variant="primary" size="sm" onclick={openCreate} icon="note_add">
          New preset
        </Button>
      </div>
    </div>
    {#if batchStatus}<p class="text-xs text-text-muted pt-2">{batchStatus}</p>{/if}
    {#if presets.length === 0}
      <p class="text-xs text-text-subtle">No presets yet.</p>
    {:else}
      <!-- The filter row lives outside the match/no-match branch: when a query
           matches nothing, the field has to stay on screen or the operator
           cannot clear what they typed. -->
      <div class="flex flex-wrap items-center gap-2 pt-4">
        <input
          type="text"
          bind:value={presetSearch}
          placeholder="Filter presets…"
          aria-label="Filter presets"
          class="min-w-[240px] flex-1 px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs placeholder:text-text-subtle"
        />
        <select
          bind:value={presetSort}
          aria-label="Sort presets"
          class="px-2 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none"
        >
          <option value="name-asc">Name A→Z</option>
          <option value="name-desc">Name Z→A</option>
          <option value="newest">Newest first</option>
          <option value="updated">Recently updated</option>
          <option value="usage">Most used</option>
          <option value="kind">By kind</option>
        </select>
        <select
          bind:value={presetPageSize}
          aria-label="Rows per page"
          class="px-2 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none"
        >
          <option value={10}>10 / page</option>
          <option value={20}>20 / page</option>
          <option value={50}>50 / page</option>
          <option value={100}>100 / page</option>
        </select>
      </div>
      {#if filteredPresets.length === 0}
        <p class="text-xs text-text-subtle pt-3 border-t border-border-subtle">
          No preset matches "{presetSearch}".
        </p>
      {:else}
      <!-- Two rows: the select-all checkboxes and the selected count stay on the
           first line, and every action that applies to the selection moves to
           its own line below. One line held both, so the actions pushed the
           count sideways and wrapped unpredictably as the selection grew. -->
      <div class="flex flex-wrap items-center gap-2 pt-3 border-t border-border-subtle">
        <label class="flex items-center gap-2 text-xs text-text-subtle cursor-pointer select-none" title="Select the presets on this page only">
          <input
            type="checkbox"
            checked={pagedPresets.length > 0 && pagedPresets.every((p) => selectedPresetIds.has(p.id ?? p.name ?? ''))}
            onchange={toggleSelectAllOnPage}
            class="w-4 h-4 accent-brand-500"
          />
          Select page
        </label>
        <label class="flex items-center gap-2 text-xs text-text-subtle cursor-pointer select-none" title="Select every preset the current filter matches, across all pages">
          <input
            type="checkbox"
            checked={filteredPresets.length > 0 && filteredPresets.every((p) => selectedPresetIds.has(p.id ?? p.name ?? ''))}
            onchange={toggleSelectAllFiltered}
            class="w-4 h-4 accent-brand-500"
          />
          Select all ({filteredPresets.length})
        </label>
        {#if selectedPresetIds.size > 0}
          <span class="inline-flex items-center rounded-[6px] bg-brand-500/10 border border-brand-500/30 px-1.5 py-0.5 text-[11px] font-semibold text-brand-500">{selectedPresetIds.size} selected</span>
        {/if}
      </div>
      {#if selectedPresetIds.size > 0}
        <div class="flex flex-wrap items-center gap-2 pt-2">
          <Button variant="outline" size="sm" onclick={batchExportPresets}>
            Export JSON
          </Button>
          <Button variant="danger" size="sm" onclick={batchDeletePresets} disabled={batchBusy}>
            {batchBusy ? 'Deleting…' : 'Delete'}
          </Button>
          <button type="button" onclick={clearPresetSelection} class="text-xs text-text-subtle hover:underline">
            Clear
          </button>
        </div>
      {/if}
      <!-- The list is flush to the card edges: the toolbar keeps the card's
           padding, the rows themselves span full width, and the pagination
           footer returns to the padded inset. That is what makes a 53-row
           table read as one surface. -->
      <div class="-mx-5 mt-4 border-t border-border-subtle">
      <ul class="divide-y divide-border-subtle">
        {#each pagedPresets as preset (preset.id ?? preset.name)}
          {@const presetKey = preset.id ?? preset.name ?? ''}
          <li class="flex items-center gap-3 px-5 py-2.5">
            <input
              type="checkbox"
              checked={selectedPresetIds.has(presetKey)}
              onchange={() => togglePresetSelect(presetKey)}
              aria-label={`Select ${preset.name || presetKey}`}
              class="w-4 h-4 accent-brand-500 shrink-0"
            />
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 min-w-0">
                <span class="text-sm text-text-main truncate">{preset.name || preset.id || 'Untitled'}</span>
                {#if preset.kind}
                  <span
                    class="px-1.5 py-0.5 rounded-[6px] bg-surface-3 border border-border-subtle text-[10px] font-semibold uppercase text-text-main shrink-0"
                    >{preset.kind}</span
                  >
                {/if}
              </div>
              <div class="text-[11px] text-text-subtle truncate">
                {preset.id}{preset.geminiSafety ? ' · safety' : ''}{preset.source ? ' · ' + preset.source : ''}{preset.keywords?.length
                  ? ' · ' + preset.keywords.length + ' keyword' + (preset.keywords.length > 1 ? 's' : '')
                  : ''}{preset.excludes?.length ? ' · ' + preset.excludes.length + ' excluded' : ''}
                {#if presetUsage[preset.id ?? ''] !== undefined}
                  · <span class="text-text-muted">{presetUsage[preset.id ?? '']} hit{presetUsage[preset.id ?? ''] === 1 ? '' : 's'}</span>
                {/if}
              </div>
            </div>
            <div class="flex items-center gap-2 shrink-0">
              <button
                type="button"
                onclick={() => openEdit(preset)}
                aria-label={`Edit ${preset.name || presetKey}`}
                title="Edit"
                class="text-text-subtle hover:text-brand-500 leading-none"
              >
                <span class="material-symbols-outlined text-[16px]">edit</span>
              </button>
              <button
                type="button"
                onclick={() => duplicatePreset(preset.id ?? '')}
                aria-label={`Duplicate ${preset.name || presetKey}`}
                title="Duplicate"
                class="text-text-subtle hover:text-brand-500 leading-none"
              >
                <span class="material-symbols-outlined text-[16px]">content_copy</span>
              </button>
              {#if confirmDeleteId === presetKey}
                <button type="button" onclick={() => removePreset(presetKey)} class="text-xs text-error font-medium hover:underline">Confirm</button>
                <button type="button" onclick={() => (confirmDeleteId = null)} class="text-xs text-text-subtle hover:underline">Cancel</button>
              {:else}
                <button
                  type="button"
                  onclick={() => removePreset(presetKey)}
                  aria-label={`Delete ${preset.name || presetKey}`}
                  title="Delete"
                  class="text-text-subtle hover:text-error leading-none"
                >
                  <span class="material-symbols-outlined text-[16px]">delete</span>
                </button>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
      </div>

      <Pagination
        page={presetPage}
        pageCount={presetPageCount}
        total={filteredPresets.length}
        noun="preset"
        onPage={(p) => (presetPage = p)}
      />
    {/if}
    {/if}
  </Card>

  <!-- Model bindings: the second workspace, same table treatment as presets. -->
  <Card padding="md" class="overflow-hidden">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="text-sm font-bold text-text-main">
        Model bindings <span class="text-text-subtle font-normal">({filteredBindings.length})</span>
      </h2>
      <Button variant="primary" size="sm" onclick={() => openBindModal('single')} icon="add">
        Add
      </Button>
    </div>
    <p class="text-xs text-text-subtle pt-2">
      Bind a model, provider, or combo id to a preset. The id is matched exactly as the client sends it.
      A keyword on a preset already covers every id it matches, so a binding is only needed to pick a
      different preset for one specific id.
    </p>
    {#if bindingBatchStatus}<p class="text-xs text-text-muted pt-2">{bindingBatchStatus}</p>{/if}
    {#if boundEntries.length === 0}
      <p class="text-xs text-text-subtle">No models armed.</p>
    {:else}
      <!-- The filter row lives outside the match/no-match branch: when a query
           matches nothing, the field has to stay on screen or the operator
           cannot clear what they typed. -->
      <div class="flex flex-wrap items-center gap-2 pt-4">
        <input
          type="text"
          bind:value={bindingSearch}
          placeholder="Filter bindings…"
          aria-label="Filter bindings"
          class="min-w-[240px] flex-1 px-3 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs placeholder:text-text-subtle"
        />
        <select
          bind:value={bindingPageSize}
          aria-label="Rows per page"
          class="px-2 py-1.5 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none"
        >
          <option value={10}>10 / page</option>
          <option value={20}>20 / page</option>
          <option value={50}>50 / page</option>
          <option value={100}>100 / page</option>
        </select>
      </div>
      {#if filteredBindings.length === 0}
        <p class="text-xs text-text-subtle pt-3 border-t border-border-subtle">
          No binding matches "{bindingSearch}".
        </p>
      {:else}
      <!-- Two rows: the select-all checkboxes and the selected count stay on the
           first line, and every action that applies to the selection moves to
           its own line below. One line held both, so the actions pushed the
           count sideways and wrapped unpredictably as the selection grew. -->
      <div class="flex flex-wrap items-center gap-2 pt-3 border-t border-border-subtle">
        <label class="flex items-center gap-2 text-xs text-text-subtle cursor-pointer select-none" title="Select the bindings on this page only">
          <input
            type="checkbox"
            checked={pagedBindings.length > 0 && pagedBindings.every(([id]) => selectedBindingIds.has(id))}
            onchange={toggleSelectAllBindingsOnPage}
            class="w-4 h-4 accent-brand-500"
          />
          Select page
        </label>
        <label class="flex items-center gap-2 text-xs text-text-subtle cursor-pointer select-none" title="Select every binding the current filter matches, across all pages">
          <input
            type="checkbox"
            checked={filteredBindings.length > 0 && filteredBindings.every(([id]) => selectedBindingIds.has(id))}
            onchange={toggleSelectAllBindingsFiltered}
            class="w-4 h-4 accent-brand-500"
          />
          Select all ({filteredBindings.length})
        </label>
        {#if selectedBindingIds.size > 0}
          <span class="inline-flex items-center rounded-[6px] bg-brand-500/10 border border-brand-500/30 px-1.5 py-0.5 text-[11px] font-semibold text-brand-500">{selectedBindingIds.size} selected</span>
        {/if}
      </div>
      {#if selectedBindingIds.size > 0}
        <div class="flex flex-wrap items-center gap-2 pt-2">
          <select
            bind:value={bindingBatchPresetId}
            aria-label="Preset for the selected bindings"
            class="px-2 py-1 rounded-[8px] bg-surface-2 border border-border-subtle text-text-main text-xs outline-none"
          >
            <option value="">Choose preset…</option>
            {#each presets as preset (preset.id ?? preset.name)}
              <option value={preset.id ?? ''}>{preset.name || preset.id}</option>
            {/each}
          </select>
          <button
            type="button"
            onclick={batchRebindPreset}
            disabled={bindingBatchBusy || !bindingBatchPresetId}
            class="px-2.5 py-1 rounded-[8px] bg-brand-500 text-white text-xs font-medium disabled:opacity-50 hover:bg-primary-hover"
          >
            Rebind
          </button>
          <button
            type="button"
            onclick={() => batchSetBindingsEnabled(true)}
            disabled={bindingBatchBusy}
            class="px-2.5 py-1 rounded-[8px] border border-border-subtle text-text-main text-xs hover:bg-surface-2"
          >
            Turn on
          </button>
          <button
            type="button"
            onclick={() => batchSetBindingsEnabled(false)}
            disabled={bindingBatchBusy}
            class="px-2.5 py-1 rounded-[8px] border border-border-subtle text-text-main text-xs hover:bg-surface-2"
          >
            Turn off
          </button>
          <Button variant="danger" size="sm" onclick={batchUnbindModels} disabled={bindingBatchBusy}>
            {bindingBatchBusy ? 'Working…' : 'Unbind'}
          </Button>
          <button type="button" onclick={clearBindingSelection} class="text-xs text-text-subtle hover:underline">
            Clear
          </button>
          </div>
      {/if}

      <div class="-mx-5 mt-4 border-t border-border-subtle">
        <ul class="divide-y divide-border-subtle">
        {#each pagedBindings as [id, cfg] (id)}
          {@const slash = id.indexOf('/')}
          {@const provider = slash > 0 ? id.slice(0, slash) : ''}
          {@const model = slash > 0 ? id.slice(slash + 1) : id}
          <li class="flex items-center gap-3 px-5 py-2.5">
            <input
              type="checkbox"
              checked={selectedBindingIds.has(id)}
              onchange={() => toggleBindingSelect(id)}
              aria-label={`Select ${id}`}
              class="w-4 h-4 accent-brand-500 shrink-0"
            />
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 min-w-0">
                {#if provider}
                  <span
                    class="shrink-0 rounded-[6px] border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-text-muted"
                    title={`Provider prefix: ${provider}`}>{provider}</span
                  >
                {/if}
                <span class="text-sm text-text-main font-code truncate">{model}</span>
              </div>
              <div class="text-[11px] text-text-subtle truncate">
                {presetLabel(cfg.presetId)}{cfg.enabled ? '' : ' · injection off'}
              </div>
            </div>
            <div class="flex items-center gap-2 shrink-0">
              <button
                type="button"
                onclick={() => openBindingEdit(id, cfg)}
                aria-label={`Edit ${id}`}
                title="Edit"
                class="text-text-subtle hover:text-brand-500 leading-none"
              >
                <span class="material-symbols-outlined text-[16px]">edit</span>
              </button>
              <!-- Toggle: the binding stays listed either way, only injection
                   is switched on or off. -->
              <Toggle
                checked={cfg.enabled}
                label={`${cfg.enabled ? 'Disable' : 'Enable'} ${id}`}
                title={cfg.enabled ? 'On — click to disable' : 'Off — click to enable'}
                onChange={() => toggleModel(id, cfg)}
              />
              <button
                type="button"
                onclick={() => unbindModel(id)}
                aria-label={`Unbind ${id}`}
                title="Unbind"
                class="text-text-subtle hover:text-error leading-none"
              >
                <span class="material-symbols-outlined text-[16px]">delete</span>
              </button>
            </div>
          </li>
        {/each}
      </ul>
      </div>

      <Pagination
        page={bindingPage}
        pageCount={bindingPageCount}
        total={filteredBindings.length}
        noun="binding"
        onPage={(p) => (bindingPage = p)}
      />
    {/if}
      {/if}
  </Card>
</div>

{#if testMatchOpen}
  <Modal
    isOpen={testMatchOpen}
    onClose={() => (testMatchOpen = false)}
    title="Test match"
    size="md"
    icon="troubleshoot"
  >

    <!-- One rhythm for the whole panel: the input row, the verdict card, and
         the payload preview are siblings, so they are spaced as siblings
         rather than left touching. -->
    <div class="space-y-4">
      <div class="flex flex-wrap items-center gap-3">
        <input
          type="text"
          bind:value={testMatchId}
          placeholder="providerx/deepseek-4-pro, or a combo name"
          aria-label="Model id to test"
          onkeydown={(e) => e.key === 'Enter' && runTestMatch()}
          class="flex-1 min-w-[280px] px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-sm text-text-main font-code placeholder:text-text-subtle"
        />
        <Button variant="primary" size="md" onclick={runTestMatch} disabled={testMatchLoading}>
          {testMatchLoading ? 'Checking…' : 'Check'}
        </Button>
      </div>
      {#if testMatchError}<p class="text-xs text-error">{testMatchError}</p>{/if}
      {#if testMatchResult}
        <div
          class="p-3 rounded-[10px] border space-y-1.5 {testMatchResult.injected
            ? 'border-success/40 bg-success/5'
            : 'border-border bg-surface-2'}"
        >
        <div class="flex items-center gap-2">
          <span
            class="material-symbols-outlined text-[16px] {testMatchResult.injected ? 'text-success' : 'text-text-subtle'}"
          >
            {testMatchResult.injected ? 'check_circle' : 'block'}
          </span>
          <span class="text-sm font-medium text-text-main">
            {testMatchResult.injected ? 'Injected' : 'Not injected'}
          </span>
          <span class="text-[11px] text-text-subtle font-code">{testMatchResult.reason}</span>
        </div>
        {#if testMatchResult.presetName || testMatchResult.presetId}
          <p class="text-xs text-text-muted">
            Preset: <span class="text-text-main">{testMatchResult.presetName || testMatchResult.presetId}</span>
          </p>
        {/if}
        {#if testMatchResult.keyword}
          <p class="text-xs text-text-muted">
            Keyword: <span class="text-text-main font-code">{testMatchResult.keyword}</span>
          </p>
        {/if}
        {#if testMatchResult.matchedId}
          <p class="text-xs text-text-muted">
            Matched id: <span class="text-text-main font-code">{testMatchResult.matchedId}</span>
          </p>
        {/if}
        {#if testMatchResult.detail}
          <p class="text-[11px] text-text-subtle">{testMatchResult.detail}</p>
        {/if}
      </div>
      {#if testMatchResult?.injected}
        <div class="space-y-2">
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-text-muted">Outbound payload preview</span>
            <button
              type="button"
              onclick={togglePreview}
              class="text-[11px] text-text-muted hover:text-brand-500 hover:underline"
            >
              {previewOpen ? 'Hide' : 'Show payload'}
            </button>
          </div>
          {#if previewOpen}
            {#if previewLoading}
              <p class="text-xs text-text-muted">Building preview…</p>
            {:else if previewError}
              <p class="text-xs text-error">{previewError}</p>
            {:else if previewData}
              <div class="space-y-2">
                {#if previewData.prompt}
                  <div class="p-3 rounded-[10px] bg-surface-2 border border-border-subtle">
                    <div class="text-[10px] font-semibold uppercase text-text-subtle mb-1">
                      Injected system prompt {previewData.presetName ? `· ${previewData.presetName}` : ''}
                    </div>
                    <pre class="text-xs text-text-main font-code whitespace-pre-wrap break-all max-h-40 overflow-y-auto">{previewData.prompt}</pre>
                  </div>
                {/if}
                <div class="p-3 rounded-[10px] bg-surface-2 border border-border-subtle">
                  <div class="text-[10px] font-semibold uppercase text-text-subtle mb-1">
                    Final messages array
                  </div>
                  <pre class="text-xs text-text-main font-code whitespace-pre-wrap break-all max-h-60 overflow-y-auto">{JSON.stringify(previewData.messages, null, 2)}</pre>
                </div>
              </div>
            {/if}
          {/if}
        </div>
      {/if}
    {/if}
    </div>
  </Modal>
{/if}

{#if presetModalOpen}
  <Modal
    isOpen={presetModalOpen}
    onClose={() => (presetModalOpen = false)}
    title={editingId
      ? 'Edit preset'
      : presetModalTab === 'files'
        ? 'New preset from files'
        : presetModalTab === 'source'
          ? 'New preset from URLs'
          : 'New preset'}
    size="lg"
    icon={editingId ? 'edit' : presetModalTab === 'files' ? 'upload_file' : presetModalTab === 'source' ? 'cloud_download' : 'note_add'}
  >
    {#if !editingId}
      <!-- Three tabs. Write builds one preset from eight fields; the other two
           fill many presets at once and have no fields to fill, only a source
           and a result log. Editing an existing preset is always Write — the
           import paths create, they never modify. -->
      <div class="grid grid-cols-3 gap-2 mb-5" role="tablist" aria-label="Preset source">
        <button
          type="button"
          role="tab"
          aria-selected={presetModalTab === 'write'}
          onclick={() => (presetModalTab = 'write')}
          class="flex items-center justify-center gap-1.5 rounded-[8px] border px-3 py-2 text-xs font-medium transition-colors {presetModalTab === 'write'
            ? 'border-brand-500 bg-brand-500/10 text-text-main'
            : 'border-transparent text-text-muted hover:border-border hover:text-text-main'}"
        >
          <span class="material-symbols-outlined text-[16px]">edit</span>
          Write
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={presetModalTab === 'files'}
          onclick={() => (presetModalTab = 'files')}
          class="flex items-center justify-center gap-1.5 rounded-[8px] border px-3 py-2 text-xs font-medium transition-colors {presetModalTab === 'files'
            ? 'border-brand-500 bg-brand-500/10 text-text-main'
            : 'border-transparent text-text-muted hover:border-border hover:text-text-main'}"
        >
          <span class="material-symbols-outlined text-[16px]">upload_file</span>
          From files
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={presetModalTab === 'source'}
          onclick={() => (presetModalTab = 'source')}
          class="flex items-center justify-center gap-1.5 rounded-[8px] border px-3 py-2 text-xs font-medium transition-colors {presetModalTab === 'source'
            ? 'border-brand-500 bg-brand-500/10 text-text-main'
            : 'border-transparent text-text-muted hover:border-border hover:text-text-main'}"
        >
          <span class="material-symbols-outlined text-[16px]">cloud_download</span>
          From URL
        </button>
      </div>
    {/if}
    {#if editingId || presetModalTab === 'write'}
    <div class="space-y-4">
      <label class="block space-y-1.5">
        <span class="text-xs text-text-muted">ID</span>
        <input
          type="text"
          bind:value={presetForm.id}
          disabled={!!editingId}
          placeholder="expert-coder"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm outline-none focus:border-brand-500 disabled:opacity-50"
        />
        {#if editingId}
          <span class="text-[11px] text-text-subtle">
            The id is what bindings and keyword rules already point at, so it cannot change here.
          </span>
        {/if}
      </label>
      <label class="block space-y-1.5">
        <span class="text-xs text-text-muted">Name</span>
        <input
          type="text"
          bind:value={presetForm.name}
          placeholder="Expert Coder"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm outline-none focus:border-brand-500"
        />
      </label>
      <label class="block space-y-1.5">
        <span class="text-xs text-text-muted">Prompt</span>
        <textarea
          bind:value={presetForm.prompt}
          rows="6"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm font-code outline-none focus:border-brand-500 resize-y"
        ></textarea>
        <button
          type="button"
          onclick={() => (promptPreviewOpen = !promptPreviewOpen)}
          class="text-[11px] text-text-muted hover:text-brand-500 hover:underline"
        >
          {promptPreviewOpen ? 'Hide preview' : 'Preview markdown'}
        </button>
        {#if promptPreviewOpen && presetForm.prompt.trim()}
          <div class="mt-1 p-3 rounded-[10px] bg-surface-2 border border-border-subtle max-h-60 overflow-y-auto">
            <div class="prose-jailbreak text-xs text-text-main leading-relaxed">
              {@html marked.parse(presetForm.prompt, { async: false }) as string}
            </div>
          </div>
        {/if}
      </label>
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="block space-y-1.5">
          <span class="text-xs text-text-muted">Source file name</span>
          <input
            type="text"
            bind:value={presetForm.source}
            placeholder="expert.md"
            class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm outline-none focus:border-brand-500"
          />
        </label>
        <label class="block space-y-1.5">
          <span class="text-xs text-text-muted">Kind</span>
          <input
            type="text"
            bind:value={presetForm.kind}
            list="jailbreak-kinds"
            placeholder="SOUL, PERSONA, AGENTS, LOAD…"
            class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm outline-none focus:border-brand-500"
          />
        </label>
      </div>
      <p class="text-[11px] text-text-subtle">
        Kind is the instruction-file convention this preset came from — filled in automatically on import
        from a name such as SOUL.md or AGENTS.md; leave empty for a prompt about a model.
      </p>
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
      <div class="flex justify-end gap-2 pt-4 border-t border-border-subtle">
        <Button variant="secondary" size="md" onclick={() => (presetModalOpen = false)}>Cancel</Button>
        <Button variant="primary" size="md" onclick={savePreset} disabled={presetSaving}>{presetSaving ? 'Saving…' : 'Save'}</Button>
      </div>
    </div>
    {:else if presetModalTab === 'files'}
      <div
        class="space-y-3"
        role="group"
        aria-label="Import from files"
        ondragover={(e) => {
          e.preventDefault()
          dragging = true
        }}
        ondragleave={(e) => {
          if (e.currentTarget === e.target) dragging = false
        }}
        ondrop={onDrop}
      >
        <p class="text-xs text-text-subtle">
          Pick or drop several files at once. The file name becomes the preset name and its keyword
          chain, so a preset covers that model on every provider and combo leg.
        </p>
        <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-text-subtle">
          <span class="font-medium">Accepted:</span>
          <span class="font-code text-text-muted">.md</span>
          <span class="font-code text-text-muted">.markdown</span>
          <span class="font-code text-text-muted">.txt</span>
          <span class="text-border">·</span>
          <span>instruction files such as</span>
          <span class="font-code text-text-muted">SOUL.md</span>
          <span class="font-code text-text-muted">AGENTS.md</span>
          <span class="font-code text-text-muted">CLAUDE.md</span>
          <span class="font-code text-text-muted">PERSONA.md</span>
          <span class="font-code text-text-muted">LOAD.md</span>
        </div>
        <label
          class="relative flex h-[calc(6*1.5rem+1rem+2px)] w-full cursor-pointer flex-col items-center justify-center gap-1 rounded-[10px] border border-dashed border-border bg-surface-2 px-3 py-2 text-center transition-colors {dragging
            ? 'border-brand-500 bg-brand-500/10'
            : 'hover:border-border'} {uploading ? 'opacity-50' : ''}"
        >
          <input
            type="file"
            accept=".md,.markdown,.txt"
            multiple
            onchange={(e) => {
              queueFiles(e.currentTarget.files)
              e.currentTarget.value = ''
            }}
            disabled={uploading}
            aria-label="Pick prompt files"
            class="absolute inset-0 h-full w-full cursor-pointer opacity-0"
          />
          <span class="material-symbols-outlined text-[22px] text-text-subtle">upload_file</span>
          <span class="text-xs font-medium text-text-main">
            {dragging ? 'Drop to queue' : 'Pick or drop several files'}
          </span>
          <span class="text-[11px] text-text-subtle">
            .md .markdown .txt — one Upload sends the whole queue
          </span>
        </label>
        {#if pickedFiles.length > 0}
          <ul class="divide-y divide-border rounded-[10px] border border-border-subtle">
            {#each pickedFiles as file, index (file.name + '|' + file.size)}
              <li class="flex items-center gap-3 px-3 py-2">
                <span class="material-symbols-outlined text-[16px] text-text-subtle shrink-0">description</span>
                <span class="text-xs text-text-main truncate flex-1">{file.name}</span>
                <span class="text-[11px] text-text-subtle shrink-0">{(file.size / 1024).toFixed(1)} KB</span>
                <button
                  type="button"
                  onclick={() => removePickedFile(index)}
                  aria-label={`Remove ${file.name}`}
                  class="text-text-subtle hover:text-error leading-none shrink-0"
                >
                  <span class="material-symbols-outlined text-[16px]">close</span>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
        <div class="flex flex-wrap items-center justify-center gap-2 pt-4 border-t border-border-subtle">
          <Button variant="primary" size="sm" class="min-w-[140px]" onclick={uploadPickedFiles} disabled={uploading || pickedFiles.length === 0}>
            {uploading ? 'Uploading…' : pickedFiles.length > 1 ? `Upload ${pickedFiles.length} files` : 'Upload'}
          </Button>
          {#if pickedFiles.length > 0}
            <button type="button" onclick={clearPickedFiles} class="text-xs text-text-muted hover:text-error hover:underline">
              Clear all
            </button>
          {/if}
        </div>
        {#if importStatus}<p class="text-xs text-text-muted text-center">{importStatus}</p>{/if}
      </div>
    {:else}
      <div class="space-y-4">
        <p class="text-xs text-text-subtle">
          One URL per line. A GitHub repository pulls every prompt file in it; any other http(s)
          address — GitLab, pastebin, hastebin, a raw file — is read as one prompt.
        </p>
        <textarea
          bind:value={importTarget}
          rows="6"
          placeholder={'owner/repo, owner/repo@branch\nhttps://github.com/owner/repo\nhttps://gitlab.com/owner/repo/-/raw/main/SOUL.md\nhttps://paste.example.com/abc123'}
          aria-label="Repositories or URLs to import, one per line"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm font-code placeholder:text-text-subtle outline-none focus:border-brand-500 resize-y"
        ></textarea>
        <div class="flex flex-wrap items-center justify-center gap-2">
          <input
            type="text"
            bind:value={importFileName}
            placeholder={importNameSuggestion || 'my-prompt.md (optional)'}
            aria-label="Custom file name for imported preset"
            class="w-[240px] px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-sm text-text-main placeholder:text-text-subtle"
          />
          <Button variant="primary" size="sm" class="min-w-[140px]" onclick={importFromGithub} disabled={importing}>
            {importing ? 'Importing…' : importUrls.length > 1 ? `Import ${importUrls.length} URLs` : 'Import'}
          </Button>
        </div>
        {#if importUrls.length > 1}
          <p class="text-[11px] text-text-subtle text-center">
            Each URL becomes its own preset, named after its source.
          </p>
        {/if}
        {#if importStatus}<p class="text-xs text-text-muted text-center">{importStatus}</p>{/if}
      </div>
    {/if}
    {#if importResult}
      <div class="space-y-1 pt-4 mt-4 border-t border-border-subtle">
        <h3 class="text-xs font-semibold text-text-main">
          Import log
          <span class="text-text-subtle font-normal">
            ({importResult.imported.length} imported, {importResult.refused.length} skipped)
          </span>
        </h3>
        <textarea
          readonly
          value="{importLogText}"
          rows="{Math.min(12, Math.max(4, importResult.imported.length + importResult.refused.length))}"
          aria-label="Import log"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-[11px] font-code leading-relaxed outline-none resize-y"
        ></textarea>
      </div>
    {/if}
  </Modal>
{/if}

{#if bindModalOpen}
  <Modal
    isOpen={bindModalOpen}
    onClose={() => (bindModalOpen = false)}
    title={bindingEditId ? 'Edit model binding' : 'Add model bindings'}
    size="xl"
    icon={bindingEditId ? 'edit' : 'link'}
  >
    {#if !bindingEditId}
    <!-- Two tabs: type one id by hand, or pick many from the live catalog.
         Single opens first — it is the common case. Editing an existing
         binding always lands on Single, so the strip is hidden then.
         The segmented treatment is the one the Proxy Pools Bulk Add dialog
         uses, so the two "one by hand vs many at once" dialogs match. -->
    <div class="flex gap-1 rounded-lg border border-border/50 bg-surface-2 p-1 mb-5" role="tablist" aria-label="Binding mode">
      <button
        type="button"
        role="tab"
        aria-selected={bindModalTab === 'single'}
        onclick={() => (bindModalTab = 'single')}
        class="flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors {bindModalTab === 'single'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Single
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={bindModalTab === 'many'}
        onclick={() => (bindModalTab = 'many')}
        class="flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors {bindModalTab === 'many'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Bulk
      </button>
    </div>
    {/if}

    {#if bindModalTab === 'single'}
      <div class="space-y-4">
        <p class="text-xs text-text-subtle">
          {bindingEditId
            ? 'The id is what the client sends, so it cannot change here. Unbind and re-add to bind a different id.'
            : 'Type one model, provider, or combo id exactly as the client sends it, then pick the preset it resolves to.'}
        </p>
        <label class="block space-y-1.5">
          <span class="text-xs text-text-muted">Model / provider / combo id</span>
          <input
            type="text"
            bind:value={bindModelId}
            disabled={!!bindingEditId}
            placeholder="deepseek-v4-flash"
            class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm font-code outline-none focus:border-brand-500 disabled:opacity-50"
          />
        </label>
        <label class="block space-y-1.5">
          <span class="text-xs text-text-muted">Preset</span>
          <div class="relative" data-preset-combobox>
            <input
              type="text"
              value={bindPresetDropdownOpen ? bindPresetSearch : bindPresetLabel}
              onfocus={() => {
                bindPresetSearch = ''
                bindPresetDropdownOpen = true
              }}
              oninput={(e) => {
                bindPresetSearch = e.currentTarget.value
                bindPresetDropdownOpen = true
              }}
              placeholder="Search presets…"
              aria-label="Search presets"
              class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-text-main text-sm placeholder:text-text-subtle outline-none focus:border-brand-500"
            />
            {#if bindPresetDropdownOpen}
              <ul
                class="absolute z-10 mt-1 w-full max-h-48 overflow-y-auto rounded-[10px] border border-border-subtle bg-surface shadow-lg divide-y divide-border"
              >
                <li>
                  <button
                    type="button"
                    onclick={() => {
                      bindPresetId = ''
                      bindPresetSearch = ''
                      bindPresetDropdownOpen = false
                    }}
                    class="w-full text-left px-3 py-2 text-sm text-text-main hover:bg-surface-2 {bindPresetId === ''
                      ? 'text-brand-500 font-medium'
                      : ''}"
                  >
                    Default document
                  </button>
                </li>
                {#each bindPresetOptions as preset (preset.id ?? preset.name)}
                  <li>
                    <button
                      type="button"
                      onclick={() => {
                        bindPresetId = preset.id ?? ''
                        bindPresetSearch = ''
                        bindPresetDropdownOpen = false
                      }}
                      class="w-full text-left px-3 py-2 text-sm text-text-main hover:bg-surface-2 truncate {bindPresetId ===
                      (preset.id ?? '')
                        ? 'text-brand-500 font-medium'
                        : ''}"
                    >
                      {preset.name || preset.id}
                      {#if preset.kind}<span class="ml-1.5 text-[10px] text-text-subtle uppercase">{preset.kind}</span>{/if}
                    </button>
                  </li>
                {/each}
                {#if bindPresetOptions.length === 0}
                  <li class="px-3 py-2 text-xs text-text-subtle">No preset matches.</li>
                {/if}
              </ul>
            {/if}
          </div>
        </label>
        {#if bindError}<p class="text-xs text-error">{bindError}</p>{/if}
        {#if bindHint}<p class="text-xs text-text-muted">{bindHint}</p>{/if}
        {#if bindingEditId}
          <label class="flex items-center gap-2 cursor-pointer select-none">
            <input type="checkbox" bind:checked={bindingEditEnabled} class="w-4 h-4 accent-brand-500" />
            <span class="text-sm text-text-main">Inject this preset for the id above</span>
          </label>
        {/if}
        <div class="flex justify-end gap-2 pt-4 border-t border-border-subtle">
          <Button variant="secondary" size="md" onclick={() => (bindModalOpen = false)}>Cancel</Button>
          <Button variant="primary" size="md" onclick={bindModel} disabled={bindingBatchBusy}>
            {bindingEditId ? 'Save' : 'Bind'}
          </Button>
        </div>
      </div>
    {:else}
      <div class="space-y-4">
      <p class="text-xs text-text-subtle">
        Pick model ids from the live catalog, then choose the preset below. Every picked id is bound to it.
      </p>

      <!-- Search: plain substring, or an RE2 pattern when the query starts with "/" -->
      <div class="space-y-1.5">
        <input
          type="text"
          bind:value={bulkArmSearch}
          placeholder="Filter models…  (/^deepseek for regex)"
          aria-label="Filter models"
          class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-sm text-text-main font-code placeholder:text-text-subtle"
        />
        <p class="text-[11px] text-text-subtle">
          Substring by default. Start with <span class="font-code text-text-muted">/</span> for an RE2 regex,
          matched case-insensitively — e.g. <span class="font-code text-text-muted">/^deepseek</span> or
          <span class="font-code text-text-muted">/(kimi|glm)-\d</span>.
        </p>
        {#if bulkArmRegexError}
          <p class="text-[11px] text-error">{bulkArmRegexError}</p>
        {/if}
      </div>

      {#if bulkArmModelsLoading}
        <p class="text-xs text-text-muted">Loading catalog…</p>
      {:else if bulkArmModels.length === 0}
        <div class="space-y-2">
          <p class="text-xs text-text-subtle">
            {bulkArmStatus
              ? 'The live catalog could not be loaded, so there is nothing to pick from yet.'
              : 'No models available.'}
          </p>
          <Button variant="secondary" size="sm" onclick={openBulkArm}>
            {bulkArmStatus ? 'Retry' : 'Reload catalog'}
          </Button>
        </div>
      {:else}
        <!-- Separator closes the search block, so the result count and Select
             all read as the header of the list below rather than part of the
             search hint. -->
        <div class="flex items-center justify-between gap-2 pt-3 border-t border-border-subtle">
          <span class="text-[11px] text-text-subtle">
            {bulkArmFiltered.length} match{bulkArmFiltered.length === 1 ? '' : 'es'}
            {#if bulkArmModels.length > 200} (first 200 shown){/if}
          </span>
          <button
            type="button"
            onclick={selectAllBulkArmFiltered}
            disabled={bulkArmFiltered.length === 0}
            class="text-xs text-brand-500 hover:underline disabled:opacity-40"
          >
            Select all matches
          </button>
        </div>
        <ul class="divide-y divide-border rounded-[10px] border border-border-subtle overflow-y-auto max-h-[40vh]">
          {#each bulkArmFiltered as id (id)}
            <li>
              <label class="flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-surface-2">
                <input
                  type="checkbox"
                  checked={bulkArmPicked.has(id)}
                  onchange={() => toggleBulkArmPick(id)}
                  class="w-4 h-4 accent-brand-500"
                />
                <span class="text-sm text-text-main font-code truncate">{id}</span>
              </label>
            </li>
          {/each}
        </ul>
      {/if}

      <!-- Preset picker sits below the results: the operator picks the models
           first, then decides what they become. It is a searchable combobox so
           a 50-preset library stays navigable. -->
      <div class="pt-4 border-t border-border-subtle space-y-2">
        <span class="text-xs text-text-muted">Bind the picked ids to…</span>
        <div class="relative">
          <input
            type="text"
            bind:value={bulkArmPresetSearch}
            placeholder="Search presets…"
            aria-label="Search presets"
            onfocus={() => (bulkArmPresetDropdownOpen = true)}
            class="w-full px-3 py-2 rounded-[10px] bg-surface-2 border border-border-subtle text-sm text-text-main placeholder:text-text-subtle outline-none focus:border-brand-500"
          />
          {#if bulkArmPresetDropdownOpen}
            <ul class="absolute z-10 mt-1 w-full max-h-48 overflow-y-auto rounded-[10px] border border-border-subtle bg-surface shadow-lg divide-y divide-border">
              <li>
                <button
                  type="button"
                  onclick={() => {
                    bulkArmPresetId = ''
                    bulkArmPresetSearch = 'Default document'
                    bulkArmPresetDropdownOpen = false
                  }}
                  class="w-full text-left px-3 py-2 text-sm text-text-main hover:bg-surface-2"
                >
                  Default document
                </button>
              </li>
              {#each bulkArmPresetOptions as preset (preset.id ?? preset.name)}
                <li>
                  <button
                    type="button"
                    onclick={() => {
                      bulkArmPresetId = preset.id ?? ''
                      bulkArmPresetSearch = preset.name || preset.id || ''
                      bulkArmPresetDropdownOpen = false
                    }}
                    class="w-full text-left px-3 py-2 text-sm text-text-main hover:bg-surface-2 truncate"
                  >
                    {preset.name || preset.id}
                    {#if preset.kind}<span class="ml-1.5 text-[10px] text-text-subtle uppercase">{preset.kind}</span>{/if}
                  </button>
                </li>
              {/each}
              {#if bulkArmPresetOptions.length === 0}
                <li class="px-3 py-2 text-xs text-text-subtle">No preset matches.</li>
              {/if}
            </ul>
          {/if}
        </div>
      </div>

      {#if bulkArmStatus}<p class="text-xs text-text-muted">{bulkArmStatus}</p>{/if}
      <div class="flex items-center justify-between gap-2 pt-4 border-t border-border-subtle">
        <span class="text-xs text-text-subtle">{bulkArmPicked.size} picked</span>
        <div class="flex gap-2">
          <Button variant="secondary" size="md" onclick={() => (bindModalOpen = false)}>Cancel</Button>
          <Button variant="primary" size="md" onclick={saveBulkArm} disabled={bulkArmSaving || bulkArmPicked.size === 0}>
            {bulkArmSaving ? 'Binding…' : 'Bulk Bind'}
          </Button>
        </div>
      </div>
      </div>
    {/if}
  </Modal>
{/if}
