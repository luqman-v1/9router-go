<script lang="ts">
  import { onMount } from 'svelte'
  import { Plus, Trash2, Save, X, ChevronDown, FileText, Bot } from 'lucide-svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import { api, type PromptInjectionDoc, type PromptInjectionTemplate } from '../api/client'

  let injections = $state<PromptInjectionDoc[]>([])
  let templates = $state<PromptInjectionTemplate[]>([])
  let models = $state<string[]>([])
  let apiKeys = $state<{ id: string; name?: string }[]>([])
  let loading = $state(true)
  let saving = $state(false)
  let savingTemplate = $state(false)
  let deletingId = $state<string | null>(null)

  // Editor modal
  let editorOpen = $state(false)
  let editing = $state<PromptInjectionDoc | null>(null)
  let formTargetType = $state<'global' | 'key' | 'model' | 'key-model'>('global')
  let formTargetId = $state('')
  let formMode = $state<'system' | 'script'>('system')
  let formContent = $state('')
  let formEnabled = $state(true)

  // Template modal
  let templateModalOpen = $state(false)
  let templateName = $state('')
  let templateContent = $state('')
  let templateKind = $state('')
  let savingTemplateName = $state('')
  let savingTemplateContent = $state('')
  let savingTemplateKind = $state('')

  onMount(() => {
    loadAll()
  })

  async function loadAll() {
    loading = true
    try {
      const [inj, tpl, modelsRes, keys] = await Promise.all([
        api.listPromptInjections(),
        api.listPromptInjectionTemplates(),
        api.getJailbreakModelCatalog().catch(() => ({ models: [] })),
        api.getApiKeys().catch(() => []),
      ])
      injections = inj
      templates = tpl
      models = modelsRes.models
      apiKeys = keys.map((k) => ({ id: k.id, name: k.name }))
    } finally {
      loading = false
    }
  }

  function openEditor(item?: PromptInjectionDoc) {
    if (item) {
      editing = item
      formTargetType = item.targetType
      formTargetId = item.targetId
      formMode = item.mode
      formContent = item.content
      formEnabled = item.enabled
    } else {
      editing = null
      formTargetType = 'global'
      formTargetId = ''
      formMode = 'system'
      formContent = ''
      formEnabled = true
    }
    editorOpen = true
  }

  async function saveEditor() {
    if (!editing && formTargetType !== 'global' && formTargetId.trim() === '') return
    saving = true
    try {
      const payload: PromptInjectionDoc = {
        id: editing?.id,
        targetType: formTargetType,
        targetId: formTargetId.trim(),
        mode: formMode,
        content: formContent,
        enabled: formEnabled,
        createdAt: editing?.createdAt,
        updatedAt: editing?.updatedAt,
      }
      const res = await api.savePromptInjection(payload)
      await loadAll()
      editorOpen = false
    } finally {
      saving = false
    }
  }

  async function deleteInjection(id: string) {
    deletingId = id
    try {
      await api.deletePromptInjection(id)
      await loadAll()
    } finally {
      deletingId = null
    }
  }

  function openTemplateEditor(tpl?: PromptInjectionTemplate) {
    if (tpl) {
      savingTemplateName = tpl.name
      savingTemplateContent = tpl.content
      savingTemplateKind = tpl.kind || ''
    } else {
      savingTemplateName = ''
      savingTemplateContent = ''
      savingTemplateKind = ''
    }
    templateModalOpen = true
  }

  async function saveTemplate() {
    if (savingTemplateName.trim() === '') return
    savingTemplate = true
    try {
      const payload: PromptInjectionTemplate = {
        id: editing?.id,
        name: savingTemplateName.trim(),
        content: savingTemplateContent,
        kind: savingTemplateKind.trim(),
      }
      await api.savePromptInjectionTemplate(payload)
      await loadAll()
      templateModalOpen = false
    } finally {
      savingTemplate = false
    }
  }

  async function deleteTemplate(id: string) {
    deletingId = id
    try {
      await api.deletePromptInjectionTemplate(id)
      await loadAll()
    } finally {
      deletingId = null
    }
  }

  function applyTemplate(tpl: PromptInjectionTemplate) {
    formContent = tpl.content
    formMode = 'system'
  }

  $: filteredInjections = injections.filter((inj) => {
    if (!formTargetType) return true
    return inj.targetType === formTargetType
  })

  $: targetOptions = $derived(
    formTargetType === 'key'
      ? apiKeys.map((k) => ({ value: k.id, label: k.name || k.id }))
      : formTargetType === 'model'
        ? models.map((m) => ({ value: m, label: m }))
        : formTargetType === 'key-model'
          ? apiKeys.flatMap((k) =>
              models.map((m) => ({
                value: `${k.id}:${m}`,
                label: `${k.name || k.id} → ${m}`,
              })),
            )
          : []
  )
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between gap-2">
    <div>
      <h2 class="text-base font-semibold text-text-main">Prompt Injection Pro</h2>
      <p class="text-[11px] text-text-subtle">
        System prompt or chat script per global, API key, model, or model+key. Stacked: global → key → model → key+model.
      </p>
    </div>
    <div class="flex gap-2">
      <Button variant="secondary" size="sm" onclick={() => openTemplateEditor()}>
        <FileText class="w-3.5 h-3.5" /> Template
      </Button>
      <Button size="sm" onclick={() => openEditor()}>
        <Plus class="w-3.5 h-3.5" /> New Injection
      </Button>
    </div>
  </div>

  {#if loading}
    <Card>
      <p class="text-xs text-text-subtle">Loading injections…</p>
    </Card>
  {:else if injections.length === 0}
    <Card>
      <p class="text-xs text-text-subtle">No prompt injections configured. Add one to get started.</p>
    </Card>
  {:else}
    <div class="space-y-2">
      {#each injections as inj (inj.id)}
        <Card>
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="text-xs font-semibold text-text-main">
                  {inj.targetType === 'global' ? 'Global' : inj.targetType === 'key' ? `Key: ${inj.targetId}` : inj.targetType === 'model' ? `Model: ${inj.targetId}` : `Key+Model: ${inj.targetId}`}
                </span>
                <span class="inline-flex items-center rounded-full border border-border px-1.5 py-0.5 text-[10px] font-medium text-text-muted">
                  {inj.mode}
                </span>
                {#if inj.enabled}
                  <span class="inline-flex items-center rounded-full bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-medium text-emerald-400">ON</span>
                {:else}
                  <span class="inline-flex items-center rounded-full bg-text-muted/10 px-1.5 py-0.5 text-[10px] font-medium text-text-muted">OFF</span>
                {/if}
              </div>
              <p class="mt-1 text-[11px] text-text-subtle line-clamp-2">
                {inj.content.slice(0, 120)}{inj.content.length > 120 ? '…' : ''}
              </p>
            </div>
            <div class="flex gap-1">
              <Button variant="secondary" size="sm" onclick={() => openEditor(inj)}>
                <Save class="w-3 h-3" />
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onclick={() => deleteInjection(inj.id)}
                disabled={deletingId === inj.id}
              >
                <Trash2 class="w-3 h-3" />
              </Button>
            </div>
          </div>
        </Card>
      {/each}
    </div>
  {/if}

  {#if templates.length > 0}
    <Card>
      <div class="flex items-center justify-between mb-2">
        <p class="text-xs font-semibold text-text-main">Templates</p>
        <Button variant="secondary" size="sm" onclick={() => openTemplateEditor()}>
          <Plus class="w-3.5 h-3.5" /> Add
        </Button>
      </div>
      <div class="space-y-1">
        {#each templates as tpl (tpl.id)}
          <div class="flex items-center justify-between gap-2 rounded-lg border border-border-subtle px-2 py-1.5">
            <div class="min-w-0 flex-1">
              <p class="text-xs font-medium text-text-main">{tpl.name}</p>
              {#if tpl.kind}<p class="text-[10px] text-text-muted">{tpl.kind}</p>{/if}
            </div>
            <div class="flex gap-1">
              <Button variant="secondary" size="sm" onclick={() => applyTemplate(tpl)}>
                <Bot class="w-3 h-3" />
              </Button>
              <Button variant="secondary" size="sm" onclick={() => deleteTemplate(tpl.id)} disabled={deletingId === tpl.id}>
                <Trash2 class="w-3 h-3" />
              </Button>
            </div>
          </div>
        {/each}
      </div>
    </Card>
  {/if}
</div>

<!-- Editor Modal -->
<Modal bind:open={editorOpen} title={editing ? 'Edit Injection' : 'New Injection'} size="lg">
  <div class="space-y-3">
    <div class="grid grid-cols-2 gap-2">
      <div>
        <label class="text-[11px] font-medium text-text-muted">Target type</label>
        <select bind:value={formTargetType} class="mt-1 w-full rounded-lg border border-border bg-bg px-2 py-1.5 text-xs text-text-main">
          <option value="global">Global</option>
          <option value="key">API Key</option>
          <option value="model">Model</option>
          <option value="key-model">Key + Model</option>
        </select>
      </div>
      <div>
        <label class="text-[11px] font-medium text-text-muted">Mode</label>
        <select bind:value={formMode} class="mt-1 w-full rounded-lg border border-border bg-bg px-2 py-1.5 text-xs text-text-main">
          <option value="system">System Prompt</option>
          <option value="script">Chat Script</option>
        </select>
      </div>
    </div>

    {#if formTargetType !== 'global'}
      <div>
        <label class="text-[11px] font-medium text-text-muted">Target ID</label>
        <select bind:value={formTargetId} class="mt-1 w-full rounded-lg border border-border bg-bg px-2 py-1.5 text-xs text-text-main">
          <option value="">Select target…</option>
          {#each targetOptions as opt}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      </div>
    {/if}

    <div>
      <label class="text-[11px] font-medium text-text-muted">Content</label>
      <textarea
        bind:value={formContent}
        rows={formMode === 'script' ? 12 : 6}
        class="mt-1 w-full rounded-lg border border-border bg-bg px-2 py-1.5 text-xs text-text-main font-mono"
        placeholder={formMode === 'script' ? '{"role":"user","content":"…"} or plain text' : 'Enter system prompt…'}
      ></textarea>
    </div>

    <div class="flex items-center justify-between gap-3">
      <Toggle checked={formEnabled} size="sm" label="Enabled" onChange={(val) => (formEnabled = val)} />
      <div class="flex gap-2">
        <Button variant="secondary" size="sm" onclick={() => (editorOpen = false)}>Cancel</Button>
        <Button size="sm" onclick={saveEditor} disabled={saving || (formTargetType !== 'global' && formTargetId === '')}>
          <Save class="w-3.5 h-3.5" /> {saving ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </div>
  </div>
</Modal>

<!-- Template Modal -->
<Modal bind:open={templateModalOpen} title={savingTemplateName ? 'Edit Template' : 'New Template'} size="md">
  <div class="space-y-3">
    <div>
      <label class="text-[11px] font-medium text-text-muted">Name</label>
      <input
        bind:value={savingTemplateName}
        class="mt-1 w-full rounded-lg border border-border bg-bg px-2 py-1.5 text-xs text-text-main"
        placeholder="Template name"
      />
    </div>
    <div>
      <label class="text-[11px] font-medium text-text-muted">Kind (optional)</label>
      <input
        bind:value={savingTemplateKind}
        class="mt-1 w-full rounded-lg border border-border bg-bg px-2 py-1.5 text-xs text-text-main"
        placeholder="AGENTS, SOUL, etc."
      />
    </div>
    <div>
      <label class="text-[11px] font-medium text-text-muted">Content</label>
      <textarea
        bind:value={savingTemplateContent}
        rows={8}
        class="mt-1 w-full rounded-lg border border-border bg-bg px-2 py-1.5 text-xs text-text-main font-mono"
        placeholder="Template content"
      ></textarea>
    </div>
    <div class="flex justify-end gap-2">
      <Button variant="secondary" size="sm" onclick={() => (templateModalOpen = false)}>Cancel</Button>
      <Button size="sm" onclick={saveTemplate} disabled={savingTemplate || savingTemplateName.trim() === ''}>
        <Save class="w-3.5 h-3.5" /> {savingTemplate ? 'Saving…' : 'Save'}
      </Button>
    </div>
  </div>
</Modal>
