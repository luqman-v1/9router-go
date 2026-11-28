<script lang="ts">
  import { onMount } from 'svelte'
  import { Plus, Trash2 } from 'lucide-svelte'
  import {
    PI_CONTEXT_WINDOWS,
    PI_MAX_TOKENS,
    PI_THINKING_LEVELS,
    PI_DEFAULT_THINKING_LEVELS,
    capOptions,
    piModelCaps,
    capsFromModelInfo,
    buildPiModelEntry,
    readPiModels,
    piInputHasImage,
  } from '../../lib/piModels'

  interface Props {
    initialEntries?: any[]
    onChange?: (models: any[]) => void
  }

  let { initialEntries = [], onChange }: Props = $props()

  let selectedModels = $state<string[]>([])
  let modelCaps = $state<Record<string, any>>({})
  let modelInfo = $state<Record<string, any>>({})
  let availableModels = $state<string[]>([])
  let toAdd = $state('')

  onMount(async () => {
    const parsed = readPiModels({ providers: { '9router': { models: initialEntries } } })
    if (parsed) {
      selectedModels = parsed.ids
      modelCaps = parsed.caps
    }

    try {
      const res = await fetch('/api/models')
      if (res.ok) {
        const data = await res.json()
        const list = Array.isArray(data?.models) ? data.models : []
        const info: Record<string, any> = {}
        const vals: string[] = []
        for (const m of list) {
          const key = m?.routedModel || m?.fullModel || m?.id
          if (!key) continue
          info[key] = { ...(m?.caps || {}), thinking_levels: m?.thinking_levels }
          vals.push(key)
        }
        modelInfo = info
        availableModels = vals
      }
    } catch {
      // ignore
    }
  })

  $effect(() => {
    const out = selectedModels.map((id) => buildPiModelEntry(id, modelCaps[id]))
    onChange?.(out)
  })

  function addModel(value: string) {
    if (!value || selectedModels.includes(value)) return
    selectedModels = [...selectedModels, value]
    const info = { ...modelInfo[value] }
    const caps = capsFromModelInfo(info)
    modelCaps = { ...modelCaps, [value]: modelCaps[value] || caps }
    toAdd = ''
  }

  function removeModel(id: string) {
    selectedModels = selectedModels.filter((m) => m !== id)
    const next = { ...modelCaps }
    delete next[id]
    modelCaps = next
  }

  function setCap(id: string, key: string, value: any) {
    const prev = piModelCaps(modelCaps[id])
    modelCaps = { ...modelCaps, [id]: { ...prev, [key]: value } }
  }

  function toggleInput(id: string) {
    const prev = piModelCaps(modelCaps[id])
    const isImage = piInputHasImage(prev)
    modelCaps = { ...modelCaps, [id]: { ...prev, input: isImage ? ['text'] : ['text', 'image'] } }
  }

  function toggleReasoning(id: string) {
    const prev = piModelCaps(modelCaps[id])
    modelCaps = { ...modelCaps, [id]: { ...prev, reasoning: !prev.reasoning } }
  }

  function toggleLevel(id: string, level: string) {
    const prev = piModelCaps(modelCaps[id])
    const supported = (prev.thinkingLevels ?? PI_DEFAULT_THINKING_LEVELS) as string[]
    const enabled = supported.includes(level)
    const next = enabled ? supported.filter((l) => l !== level) : [...supported, level]
    if (next.length === 0) return
    const ordered = PI_THINKING_LEVELS.filter((l) => next.includes(l))
    modelCaps = { ...modelCaps, [id]: { ...prev, thinkingLevels: ordered } }
  }
</script>

<div class="space-y-3">
  <div class="flex items-center gap-2">
    <select bind:value={toAdd} class="flex-1 px-2 py-1.5 rounded-lg bg-surface-2 border border-border text-xs">
      <option value="">Add model…</option>
      {#each availableModels as m}
        {#if !selectedModels.includes(m)}
          <option value={m}>{m}</option>
        {/if}
      {/each}
    </select>
    <button type="button" onclick={() => addModel(toAdd)} class="px-3 py-1.5 rounded-lg bg-brand-500 text-white text-xs font-semibold">
      <Plus class="w-3.5 h-3.5 inline" /> Add
    </button>
  </div>

  {#each selectedModels as id (id)}
    {@const caps = piModelCaps(modelCaps[id])}
    <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-2">
      <div class="flex items-center justify-between">
        <span class="text-xs font-mono text-text-main">{id}</span>
        <button type="button" onclick={() => removeModel(id)} class="text-error text-xs">
          <Trash2 class="w-3.5 h-3.5" />
        </button>
      </div>

      <div class="grid grid-cols-2 gap-2">
        <div class="space-y-1">
          <span class="text-[10px] font-bold text-text-muted">Context window</span>
          <select value={caps.contextWindow} onchange={(e) => setCap(id, 'contextWindow', Number((e.target as HTMLSelectElement).value))} class="w-full px-2 py-1 rounded bg-surface border border-border text-xs">
            {#each capOptions(PI_CONTEXT_WINDOWS, caps.contextWindow) as v}
              <option value={v}>{v}</option>
            {/each}
          </select>
        </div>
        <div class="space-y-1">
          <span class="text-[10px] font-bold text-text-muted">Max tokens</span>
          <select value={caps.maxTokens} onchange={(e) => setCap(id, 'maxTokens', Number((e.target as HTMLSelectElement).value))} class="w-full px-2 py-1 rounded bg-surface border border-border text-xs">
            {#each capOptions(PI_MAX_TOKENS, caps.maxTokens) as v}
              <option value={v}>{v}</option>
            {/each}
          </select>
        </div>
      </div>

      <div class="flex items-center gap-3">
        <label class="flex items-center gap-1.5 text-xs">
          <input type="checkbox" checked={piInputHasImage(caps)} onchange={() => toggleInput(id)} />
          image input
        </label>
        <label class="flex items-center gap-1.5 text-xs">
          <input type="checkbox" checked={caps.reasoning === true} onchange={() => toggleReasoning(id)} />
          reasoning
        </label>
      </div>

      <div class="space-y-1">
        <span class="text-[10px] font-bold text-text-muted">Thinking levels</span>
        <div class="flex flex-wrap gap-2">
          {#each PI_THINKING_LEVELS as level}
            <label class="flex items-center gap-1 text-[11px]">
              <input type="checkbox" checked={(caps.thinkingLevels ?? PI_DEFAULT_THINKING_LEVELS).includes(level)} onchange={() => toggleLevel(id, level)} />
              {level}
            </label>
          {/each}
        </div>
      </div>
    </div>
  {/each}
</div>
