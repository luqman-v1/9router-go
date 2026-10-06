<script lang="ts">
  import { onMount } from 'svelte'
  import { api, getStoredAPIKey, type APIKey, type ProviderConnection, type Settings } from '../../api/client'
  import { copyToClipboard } from '../../lib/clipboard'
  import { getModelKind, getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
  import { parseCustomModelsResponse, subscribeCustomModelsChanged } from '../../lib/customModels'
  import type { ProviderCatalogItem } from '../../lib/providers'
  import Badge from '../../lib/ui/Badge.svelte'
  import { getIconPath } from '../connections/types'
  import { type MediaKind, MEDIA_KIND_INFO } from './mediaTypes'
  import NoAuthProxyCard from './NoAuthProxyCard.svelte'
  import TtsExampleCard from './TtsExampleCard.svelte'
  import SttExampleCard from './SttExampleCard.svelte'

  interface Props {
    provider: ProviderCatalogItem
    kind: MediaKind
    connections?: ProviderConnection[]
    apiKeys?: APIKey[]
    settings?: Settings
    onBack: () => void
    onRefresh: () => void
  }

  let {
    provider,
    kind,
    connections = [],
    apiKeys = [],
    settings = {},
    onBack,
    onRefresh
  }: Props = $props()

  const KIND_LABELS: Record<string, string> = {
    video: 'Video',
    embedding: 'Embedding',
    tts: 'Text To Speech',
    stt: 'Speech To Text',
    image: 'Text to Image',
    systemone: 'System One',
    webSearch: 'Web Search',
    webFetch: 'Web Fetch'
  }

  const KIND_PATHS: Record<string, string> = {
    video: '/dashboard/media-providers/video',
    embedding: '/dashboard/media-providers/embedding',
    tts: '/dashboard/media-providers/tts',
    stt: '/dashboard/media-providers/stt',
    image: '/dashboard/media-providers/image',
    systemone: '/dashboard/media-providers/systemone',
    webSearch: '/dashboard/media-providers/web',
    webFetch: '/dashboard/media-providers/web'
  }

  let kindTitle = $derived(KIND_LABELS[kind] || MEDIA_KIND_INFO[kind]?.title || kind)
  let parentPath = $derived(KIND_PATHS[kind] || `/dashboard/media-providers/${kind}`)

  // Strategy & Round Robin
  let isRoundRobin = $state(false)
  let stickyLimit = $state('1')

  $effect(() => {
    const strategy = settings?.providerStrategies?.[provider.id]
    isRoundRobin = strategy?.fallbackStrategy === 'round-robin'
    stickyLimit = String(strategy?.stickyRoundRobinLimit ?? 1)
  })

  async function toggleRoundRobin() {
    isRoundRobin = !isRoundRobin
    const current = settings?.providerStrategies || {}
    const updated = {
      ...current,
      [provider.id]: {
        ...(current[provider.id] || {}),
        fallbackStrategy: isRoundRobin ? 'round-robin' : 'priority',
        stickyRoundRobinLimit: Number(stickyLimit) || 1
      }
    }
    try {
      await api.updateSettings({ providerStrategies: updated })
      onRefresh()
    } catch (e) {
      console.error('Failed to toggle round robin', e)
    }
  }

  async function handleStickyLimitChange() {
    if (!isRoundRobin) return
    const current = settings?.providerStrategies || {}
    const updated = {
      ...current,
      [provider.id]: {
        ...(current[provider.id] || {}),
        fallbackStrategy: 'round-robin',
        stickyRoundRobinLimit: Number(stickyLimit) || 1
      }
    }
    try {
      await api.updateSettings({ providerStrategies: updated })
      onRefresh()
    } catch (e) {
      console.error('Failed to update sticky limit', e)
    }
  }

  // Connections state
  let providerConns = $derived(connections.filter((c) => c.provider === provider.id))
  let showAddConnModal = $state(false)
  let newConnKey = $state('')
  let newConnName = $state('')
  let newConnError = $state('')
  let isSavingConn = $state(false)

  let editingConn = $state<ProviderConnection | null>(null)
  let editConnName = $state('')
  let isEditingActive = $state(true)
  // Non-null while a reorder request is in flight.
  let reorderingConnId = $state<string | null>(null)

  async function handleToggleConnActive(conn: ProviderConnection) {
    try {
      await api.updateConnection(conn.id, { isActive: conn.isActive === 1 ? 0 : 1 })
      onRefresh()
    } catch (e) {
      console.error('Failed to toggle connection active', e)
    }
  }

  async function handleDeleteConn(id: string) {
    if (!confirm('Are you sure you want to delete this connection?')) return
    try {
      await api.deleteConnection(id)
      onRefresh()
    } catch (e) {
      console.error('Failed to delete connection', e)
    }
  }

  // Single server-side transactional call instead of two independent PUTs:
  // a partial failure between those two writes left two rows sharing a
  // priority, and a stable sort over tied priorities made every later click a
  // literal no-op. The in-flight flag also stops a second click from firing a
  // swap computed from the same stale list.
  async function handleMovePriority(idx: number, delta: -1 | 1) {
    const conn = providerConns[idx]
    if (!conn || reorderingConnId) return
    const target = providerConns[idx + delta]
    if (!target) return
    reorderingConnId = conn.id
    try {
      await api.reorderConnection(conn.id, delta < 0 ? 'up' : 'down')
      onRefresh()
    } catch (e) {
      alert(`Reorder failed: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      reorderingConnId = null
    }
  }

  async function handleSaveNewConnection() {
    if (!newConnKey.trim()) {
      newConnError = 'API Key is required.'
      return
    }
    isSavingConn = true
    newConnError = ''
    try {
      await api.createConnection({
        provider: provider.id,
        authType: 'apikey',
        key: newConnKey.trim(),
        name: newConnName.trim() || undefined,
        isActive: 1
      })
      showAddConnModal = false
      newConnKey = ''
      newConnName = ''
      onRefresh()
    } catch (err) {
      newConnError = err instanceof Error ? err.message : String(err)
    } finally {
      isSavingConn = false
    }
  }

  function handleOpenEditConn(conn: ProviderConnection) {
    editingConn = conn
    editConnName = conn.displayName || conn.name || ''
    isEditingActive = conn.isActive === 1
  }

  async function handleSaveEditedConn() {
    if (!editingConn) return
    try {
      await api.updateConnection(editingConn.id, {
        name: editConnName.trim(),
        isActive: isEditingActive ? 1 : 0
      })
      editingConn = null
      onRefresh()
    } catch (err) {
      alert(`Save failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Models state — upstream parity (ModelsCard kindFilter): builtin filtered by
  // kind + custom models for this provider+kind (providerAlias match, builtin dedupe).
  let customKindModels = $state<Array<{ id: string; name?: string }>>([])
  let rawModels = $derived(getModelsByProviderId(provider.id))
  let mediaModels = $derived.by(() => {
    const builtin = rawModels.filter((m) => getModelKind(m) === kind)
    const seen = new Set(builtin.map((m) => m.id))
    return [...builtin, ...customKindModels.filter((m) => m.id && !seen.has(m.id))]
  })
  let selectedModelId = $state('')

  // Custom media models — upstream SttExampleCard parity: (d.models || [])
  // filtered by kind + providerAlias (alias or id), reload on focus/changed.
  // $effect tracks provider.id/kind so navigation between providers reloads.
  $effect(() => {
    const pid = provider.id
    const k = kind
    const storageAlias = provider.alias || PROVIDER_ID_TO_ALIAS[pid] || pid
    const loadCustom = () => {
      api.getCustomModels?.().then((d: any) => {
        customKindModels = parseCustomModelsResponse(d).filter(
          (m: any) => getModelKind(m) === k && (m.providerAlias === storageAlias || m.providerAlias === pid)
        )
      }).catch(() => {})
    }
    loadCustom()
    const unsub = subscribeCustomModelsChanged(loadCustom)
    return unsub
  })
  let selectedVoice = $state('alloy')
  let ttsResponseFormat = $state('json')
  let sttResponseFormat = $state('json')
  let copiedModelId = $state<string | null>(null)
  let testingModelId = $state<string | null>(null)
  let modelTestResults = $state<Record<string, { ok: boolean; error?: string; note?: string; latency?: number }>>({})

  $effect(() => {
    if (mediaModels.length > 0 && !selectedModelId) {
      selectedModelId = mediaModels[0].id
    }
  })

  function resolveQualifiedModel(rawModelId: string): string {
    const p = provider.alias || provider.id
    if (!rawModelId) return p
    if (rawModelId.startsWith(`${provider.id}/`) || (provider.alias && rawModelId.startsWith(`${provider.alias}/`))) {
      return rawModelId
    }
    return `${p}/${rawModelId}`
  }

  function handleCopyModel(id: string) {
    const full = resolveQualifiedModel(id)
    copyToClipboard(full)
    copiedModelId = id
    setTimeout(() => { copiedModelId = null }, 2000)
  }

  async function handleTestModel(modelId: string) {
    const full = resolveQualifiedModel(modelId)
    testingModelId = modelId
    const start = performance.now()
    try {
      // The kind picks the endpoint the probe travels on, exactly like the Run
      // button below it — probing a System One model through chat completions
      // is what made this button fail while the button beside it worked.
      const res = await api.testModel(full, kind)
      modelTestResults[modelId] = {
        ok: res.ok,
        error: res.error,
        note: res.note,
        latency: res.latencyMs ?? Math.round(performance.now() - start),
      }
    } catch (err) {
      modelTestResults[modelId] = { ok: false, error: err instanceof Error ? err.message : 'Error', latency: Math.round(performance.now() - start) }
    } finally {
      testingModelId = null
    }
  }

  // Config attributes
  const FIELD_SCHEMA: Record<string, { label: string; format: (v: any) => string; isLink?: boolean; mono?: boolean }> = {
    mode: { label: 'Mode', format: (v) => v },
    defaultModel: { label: 'Model', format: (v) => v, mono: true },
    baseUrl: { label: 'Endpoint', format: (v) => v, isLink: true, mono: true },
    costPerQuery: { label: 'Cost / call', format: (v) => v === 0 ? 'Free' : `$${Number(v).toFixed(4)}` },
    pricingUrl: { label: 'Pricing', format: () => 'View pricing', isLink: true },
    freeTier: { label: 'Free tier', format: (v) => v },
    freeMonthlyQuota: { label: 'Free quota', format: (v) => v === 0 ? '—' : v >= 999999 ? 'Unlimited' : `${Number(v).toLocaleString()} / mo` },
    searchTypes: { label: 'Types', format: (v) => Array.isArray(v) ? v.join(', ') : String(v) },
    formats: { label: 'Formats', format: (v) => Array.isArray(v) ? v.join(', ') : String(v) },
    maxMaxResults: { label: 'Max results', format: (v) => String(v) },
    maxCharacters: { label: 'Max chars', format: (v) => Number(v).toLocaleString() },
  }

  let effectiveConfig = $derived.by(() => {
    if (kind === 'webFetch') {
      return (provider as any).fetchConfig || null
    }
    if (kind === 'tts') {
      return (provider as any).ttsConfig || null
    }
    if (kind === 'stt') {
      return (provider as any).sttConfig || null
    }
    if (kind === 'embedding') {
      return (provider as any).embeddingConfig || null
    }
    if (kind === 'systemone') {
      return (provider as any).systemoneConfig || null
    }
    if (kind === 'webSearch') {
      if ((provider as any).searchConfig) {
        return (provider as any).searchConfig
      }
      if ((provider as any).searchViaChat) {
        const svc = (provider as any).searchViaChat
        return {
          mode: 'chat-completions',
          defaultModel: svc.defaultModel,
          pricingUrl: svc.pricingUrl,
          freeTier: svc.freeTier,
        }
      }
      return { mode: 'chat-completions', defaultModel: mediaModels[0]?.id || provider.alias || provider.id }
    }
    if (kind === 'video' || kind === 'image') {
      return { mode: 'chat-completions', defaultModel: mediaModels[0]?.id || provider.alias || provider.id }
    }
    return null
  })

  let configRows = $derived.by(() => {
    if (!effectiveConfig) return []
    return Object.entries(FIELD_SCHEMA)
      .filter(([key]) => effectiveConfig[key] !== undefined && effectiveConfig[key] !== null && effectiveConfig[key] !== '')
      .map(([key, schema]) => ({
        key,
        label: schema.label,
        value: schema.format(effectiveConfig[key]),
        isLink: schema.isLink,
        mono: schema.mono,
        raw: effectiveConfig[key],
      }))
  })

  // Example inputs & runner
  let activeApiKey = $state('')
  let selectedConnId = $state('')
  let exampleInput = $state('')
  let exampleQuestion = $state('Does this request require urgent attention?')
  let searchType = $state('web')
  let maxResults = $state(5)
  let country = $state('')
  let language = $state('')
  let fetchFormat = $state('markdown')
  let fetchMaxChars = $state(0)
  let copiedCurl = $state(false)
  let isTestingExample = $state(false)
  let exampleResponse = $state<string | null>(null)
  let exampleLatency = $state<number | null>(null)

  const ttsVoices = ['alloy', 'ash', 'ballad', 'coral', 'echo', 'fable', 'nova', 'onyx', 'sage', 'shimmer']

  $effect(() => {
    if (kind === 'video') {
      exampleInput = 'A serene lake at sunset'
    } else if (kind === 'image') {
      exampleInput = 'A cute cat wearing a hat'
    } else if (kind === 'tts') {
      exampleInput = 'Hello, this is a text to speech test.'
    } else if (kind === 'stt') {
      exampleInput = 'audio.mp3'
    } else if (kind === 'embedding') {
      exampleInput = 'The quick brown fox jumps over the lazy dog'
    } else if (kind === 'systemone') {
      exampleInput = 'My payments have failed for three days and I am losing sales. Please help now.'
    } else if (kind === 'webSearch') {
      exampleInput = 'What is the latest news about AI?'
    } else {
      exampleInput = 'https://example.com'
    }
  })

  $effect(() => {
    if (apiKeys && apiKeys.length > 0) {
      const active = apiKeys.find((k) => k.isActive === 1 && k.key)
      if (active?.key) activeApiKey = active.key
    }
  })

  onMount(() => {
    const stored = getStoredAPIKey()
    if (stored) activeApiKey = stored
  })

  let exampleEndpoint = $derived.by(() => {
    const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:20130'
    if (kind === 'video') return `${origin}/v1/videos/generations`
    if (kind === 'image') return `${origin}/v1/images/generations`
    if (kind === 'tts') return `${origin}/v1/audio/speech`
    if (kind === 'stt') return `${origin}/v1/audio/transcriptions`
    if (kind === 'embedding') return `${origin}/v1/embeddings`
    if (kind === 'systemone') return `${origin}/v1/systemone`
    if (kind === 'webSearch') return `${origin}/v1/search`
    return `${origin}/v1/web/fetch`
  })

  let exampleCurl = $derived.by(() => {
    const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:20130'
    const key = activeApiKey || 'YOUR_KEY'
    const defaultModel = selectedModelId || mediaModels[0]?.id || ''
    const qualifiedModel = resolveQualifiedModel(defaultModel)

    if (kind === 'video') {
      return `curl -X POST ${origin}/v1/videos/generations \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '{"model":"${qualifiedModel}","prompt":"${exampleInput || 'A serene lake at sunset'}"}'`
    }
    if (kind === 'image') {
      return `curl -X POST ${origin}/v1/images/generations \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '{"model":"${qualifiedModel}","prompt":"${exampleInput || 'A cute cat wearing a hat'}","n":1,"size":"auto","quality":"auto","background":"auto","image_detail":"high","output_format":"png"}'`
    }
    if (kind === 'embedding') {
      return `curl -X POST ${origin}/v1/embeddings \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '{"model":"${qualifiedModel}","input":"${exampleInput || 'The quick brown fox jumps over the lazy dog'}"}'`
    }
    if (kind === 'tts') {
      return `curl -X POST ${origin}/v1/audio/speech \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '{"model":"${qualifiedModel}/${selectedVoice || 'alloy'}","input":"${exampleInput || 'Hello, this is a text to speech test.'}"}' \\\n  --output speech.mp3`
    }
    if (kind === 'stt') {
      return `curl -X POST ${origin}/v1/audio/transcriptions \\\n  -H "Authorization: Bearer ${key}" \\\n  -F "file=@${exampleInput || 'audio.mp3'}" \\\n  -F "model=${qualifiedModel}" \\\n  -F "response_format=${sttResponseFormat}"`
    }
    if (kind === 'systemone') {
      return `curl -X POST ${origin}/v1/systemone \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '{"model":"${qualifiedModel}","state":"${exampleInput}","questions":{"is_urgent":{"type":"noul","instructions":"${exampleQuestion}"}}}'`
    }
    if (kind === 'webSearch') {
      const extra: Record<string, any> = {
        search_type: searchType,
        max_results: maxResults,
      }
      if (country.trim()) extra.country = country.trim()
      if (language.trim()) extra.language = language.trim()
      const reqObj = {
        model: provider.alias || provider.id,
        query: exampleInput,
        ...extra,
      }
      return `curl -X POST ${origin}/v1/search \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '${JSON.stringify(reqObj)}'`
    }
    if (kind === 'webFetch') {
      const reqObj: Record<string, any> = {
        model: provider.alias || provider.id,
        url: exampleInput,
        format: fetchFormat,
      }
      if (fetchMaxChars > 0) reqObj.max_characters = fetchMaxChars
      return `curl -X POST ${origin}/v1/web/fetch \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '${JSON.stringify(reqObj)}'`
    }
    return ''
  })

  let defaultResponse = $derived.by(() => {
    if (kind === 'video') return `{\n  "data": [\n    { "url": "https://..." }\n  ]\n}`
    if (kind === 'image') return `{\n  "data": [\n    { "url": "https://...", "b64_json": "..." }\n  ]\n}`
    if (kind === 'tts') return `// Audio generated (binary MP3)\n// Or response_format=json:\n{\n  "format": "mp3",\n  "audio": "//NExAANa..."\n}`
    if (kind === 'stt') return `{\n  "text": "Hello world..."\n}`
    if (kind === 'embedding') return `{\n  "object": "list",\n  "data": [{\n    "object": "embedding",\n    "index": 0,\n    "embedding": [0.002301, -0.019212, 0.004815, ...]\n  }],\n  "model": "${selectedModelId || 'default'}",\n  "usage": { "prompt_tokens": 9, "total_tokens": 9 }\n}`
    if (kind === 'systemone') return `{\n  "model": "${selectedModelId || 'jev-1.13'}",\n  "answers": {\n    "is_urgent": { "type": "noul", "noul": 0.99 }\n  },\n  "usage": { "input_tokens": 312, "output_tokens": 48 }\n}`
    if (kind === 'webSearch') return `{\n  "results": [\n    { "title": "Example Domain", "url": "https://example.com", "snippet": "Hello world..." }\n  ]\n}`
    return `{\n  "title": "Example Domain",\n  "text": "Hello world..."\n}`
  })

  function copyCurl() {
    copyToClipboard(exampleCurl)
    copiedCurl = true
    setTimeout(() => { copiedCurl = false }, 2000)
  }

  async function handleTestExample() {
    isTestingExample = true
    exampleResponse = null
    const start = performance.now()
    try {
      const defaultModel = selectedModelId || mediaModels[0]?.id || ''
      const qualifiedModel = resolveQualifiedModel(defaultModel)

      const headers: Record<string, string> = {}
      if (activeApiKey) headers['Authorization'] = `Bearer ${activeApiKey}`
      if (selectedConnId) {
        headers['x-connection-id'] = selectedConnId
        headers['x-provider-connection-id'] = selectedConnId
      }

      let res: Response
      if (kind === 'video') {
        headers['Content-Type'] = 'application/json'
        res = await fetch('/v1/videos/generations', {
          method: 'POST',
          headers,
          body: JSON.stringify({ model: qualifiedModel, prompt: exampleInput })
        })
      } else if (kind === 'image') {
        headers['Content-Type'] = 'application/json'
        res = await fetch('/v1/images/generations', {
          method: 'POST',
          headers,
          body: JSON.stringify({ model: qualifiedModel, prompt: exampleInput, n: 1, size: 'auto', quality: 'auto', background: 'auto', image_detail: 'high', output_format: 'png' })
        })
      } else if (kind === 'tts') {
        headers['Content-Type'] = 'application/json'
        res = await fetch(`/v1/audio/speech?response_format=${ttsResponseFormat || 'json'}`, {
          method: 'POST',
          headers,
          body: JSON.stringify({
            model: `${qualifiedModel}/${selectedVoice || 'alloy'}`,
            input: exampleInput,
            voice: selectedVoice || 'alloy',
            response_format: ttsResponseFormat || 'json'
          })
        })
      } else if (kind === 'stt') {
        const formData = new FormData()
        formData.append('model', qualifiedModel)
        formData.append('response_format', sttResponseFormat || 'json')
        const dummyBlob = new Blob(['ID3...'], { type: 'audio/mp3' })
        formData.append('file', dummyBlob, exampleInput || 'audio.mp3')
        res = await fetch('/v1/audio/transcriptions', {
          method: 'POST',
          headers,
          body: formData
        })
      } else if (kind === 'embedding') {
        headers['Content-Type'] = 'application/json'
        res = await fetch('/v1/embeddings', {
          method: 'POST',
          headers,
          body: JSON.stringify({ model: qualifiedModel, input: exampleInput })
        })
      } else if (kind === 'systemone') {
        headers['Content-Type'] = 'application/json'
        res = await fetch('/v1/systemone', {
          method: 'POST',
          headers,
          body: JSON.stringify({ model: qualifiedModel, state: exampleInput, questions: { is_urgent: { type: 'noul', instructions: exampleQuestion } } })
        })
      } else if (kind === 'webSearch') {
        headers['Content-Type'] = 'application/json'
        const extra: Record<string, any> = {
          search_type: searchType,
          max_results: maxResults,
        }
        if (country.trim()) extra.country = country.trim()
        if (language.trim()) extra.language = language.trim()
        res = await fetch('/v1/search', {
          method: 'POST',
          headers,
          body: JSON.stringify({
            model: provider.alias || provider.id,
            query: exampleInput,
            ...extra,
          }),
        })
      } else if (kind === 'webFetch') {
        headers['Content-Type'] = 'application/json'
        const reqObj: Record<string, any> = {
          model: provider.alias || provider.id,
          url: exampleInput,
          format: fetchFormat,
        }
        if (fetchMaxChars > 0) reqObj.max_characters = fetchMaxChars
        res = await fetch('/v1/web/fetch', {
          method: 'POST',
          headers,
          body: JSON.stringify(reqObj),
        })
      }

      exampleLatency = Math.round(performance.now() - start)
      const cType = res.headers.get('content-type') || ''
      if (cType.includes('application/json')) {
        const json = await res.json()
        exampleResponse = JSON.stringify(json, null, 2)
      } else if (cType.includes('audio/') || cType.includes('octet-stream')) {
        const blob = await res.blob()
        exampleResponse = `// Received audio binary: ${blob.type || 'audio/mp3'} (${blob.size} bytes)`
      } else {
        const txt = await res.text()
        exampleResponse = txt || `Status: ${res.status} ${res.statusText}`
      }
    } catch (err) {
      exampleResponse = `Error: ${err instanceof Error ? err.message : String(err)}`
      exampleLatency = Math.round(performance.now() - start)
    } finally {
      isTestingExample = false
    }
  }

  let icon = $derived(getIconPath(provider.id))
</script>

<div class="flex flex-col gap-8 animate-fade-in max-w-7xl mx-auto">
  <!-- 1. Header (Back button, Provider Avatar, Name, Get Key, Service Badges) -->
  <div>
    <a
      class="inline-flex items-center gap-1 text-sm text-text-muted hover:text-primary transition-colors mb-4 cursor-pointer"
      href={parentPath}
      onclick={(e) => { e.preventDefault(); onBack() }}
    >
      <span class="material-symbols-outlined text-lg">arrow_back</span>
      {kindTitle}
    </a>
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:gap-4">
      <div
        class="size-12 rounded-lg flex items-center justify-center shrink-0 border border-border/40 overflow-hidden"
        style="background-color: {provider.color ? provider.color + '15' : '#88888815'}"
      >
        <img
          src={icon}
          alt={provider.name}
          width="48"
          height="48"
          class="object-contain size-8 rounded"
          onerror={(e) => { (e.currentTarget as HTMLElement).style.display = 'none' }}
        />
      </div>
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-3">
          <h1 class="text-2xl sm:text-3xl font-semibold tracking-tight text-text-main">{provider.name}</h1>
          {#if provider.notice?.apiKeyUrl || provider.website}
            <a
              href={provider.notice?.apiKeyUrl || provider.website}
              target="_blank"
              rel="noopener noreferrer"
              class="text-xs text-primary hover:underline inline-flex items-center gap-1"
            >
              <span class="material-symbols-outlined text-sm">open_in_new</span>
              Get API Key
            </a>
          {/if}
        </div>
        <div class="flex items-center gap-1.5 mt-1 flex-wrap">
          {#each provider.serviceKinds || [] as sk}
            <Badge tone={sk.toLowerCase() === kind.toLowerCase() ? 'primary' : 'default'} size="sm">
              {sk.toUpperCase()}
            </Badge>
          {/each}
        </div>
      </div>
    </div>
  </div>

  <!-- 2. Notice Banner (Free tier / sign up) -->
  {#if provider.notice?.text}
    <div class="flex flex-col gap-2 rounded-lg border border-blue-500/30 bg-blue-500/10 px-3 py-2 sm:flex-row sm:items-center">
      <span class="material-symbols-outlined text-[16px] text-blue-500 shrink-0">info</span>
      <p class="min-w-0 flex-1 text-xs leading-relaxed text-blue-600 dark:text-blue-400">{provider.notice.text}</p>
      {#if provider.notice.apiKeyUrl}
        <a
          href={provider.notice.apiKeyUrl}
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex justify-center rounded bg-blue-500 px-2 py-1 text-xs font-medium text-white transition-colors hover:bg-blue-600 sm:py-0.5"
        >
          Get API Key →
        </a>
      {/if}
    </div>
  {/if}

  <!-- 3. Connections Card / NoAuthProxyCard -->
  {#if provider.noAuth}
    <NoAuthProxyCard providerId={provider.id} />
  {:else}
    <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between mb-4">
        <h2 class="text-lg font-semibold text-text-main">Connections</h2>
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-xs text-text-muted font-medium">Round Robin</span>
          <div class="flex items-center gap-3">
            <button
              type="button"
              role="switch"
              aria-checked={isRoundRobin}
              onclick={toggleRoundRobin}
              class="relative inline-flex shrink-0 cursor-pointer rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-brand-500/30 {isRoundRobin ? 'bg-brand-500' : 'bg-surface-3'} w-11 h-6"
            >
              <span class="pointer-events-none inline-block rounded-full bg-white shadow-sm transform transition duration-200 ease-in-out {isRoundRobin ? 'translate-x-5' : 'translate-x-0.5'} size-5 mt-0.5"></span>
            </button>
          </div>
          {#if isRoundRobin}
            <div class="flex flex-wrap items-center gap-1.5 ml-2">
              <span class="text-xs text-text-muted">Sticky:</span>
              <input
                type="number"
                min="1"
                bind:value={stickyLimit}
                onchange={handleStickyLimitChange}
                class="w-16 px-2 py-1 text-xs border border-border rounded-md bg-background text-text-main focus:outline-none focus:border-brand-500"
              />
            </div>
          {/if}
        </div>
      </div>

      {#if providerConns.length === 0}
        <div class="flex flex-col items-center justify-center py-8 border border-dashed border-border rounded-xl text-text-muted text-xs gap-2">
          <span>No connections yet for {provider.name}.</span>
          <button
            type="button"
            onclick={() => (showAddConnModal = true)}
            class="inline-flex items-center gap-1 px-3 py-1.5 rounded-lg bg-primary text-white font-medium hover:bg-primary/90 transition-colors cursor-pointer"
          >
            <span class="material-symbols-outlined text-sm">add</span>
            Add Connection
          </button>
        </div>
      {:else}
        <div class="flex flex-col gap-2">
          {#each providerConns as conn, idx (conn.id)}
            <div class="flex flex-col gap-2 p-3 rounded-lg border border-border bg-sidebar/50 sm:flex-row sm:items-center sm:justify-between">
              <div class="flex items-center gap-2 min-w-0">
                <div class="flex flex-col items-center gap-0.5 shrink-0">
                  <button
                    type="button"
                    disabled={idx === 0 || !!reorderingConnId}
                    onclick={() => handleMovePriority(idx, -1)}
                    class="p-1 text-text-muted hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                    title="Move Up"
                  >
                    <span class="material-symbols-outlined text-sm">keyboard_arrow_up</span>
                  </button>
                  <button
                    type="button"
                    disabled={idx === providerConns.length - 1 || !!reorderingConnId}
                    onclick={() => handleMovePriority(idx, 1)}
                    class="p-1 text-text-muted hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                    title="Move Down"
                  >
                    <span class="material-symbols-outlined text-sm">keyboard_arrow_down</span>
                  </button>
                </div>
                <span class="material-symbols-outlined text-base text-text-muted shrink-0">key</span>
                <span class="text-sm font-medium text-text-main truncate">{conn.displayName || conn.name || conn.email || conn.id}</span>
                <button
                  type="button"
                  onclick={() => handleToggleConnActive(conn)}
                  class="px-2 py-0.5 rounded text-[11px] font-semibold cursor-pointer transition-colors {conn.isActive === 1 ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20' : 'bg-surface-2 text-text-muted border border-border'}"
                >
                  {conn.isActive === 1 ? 'active' : 'disabled'}
                </button>
                <span class="text-xs text-text-muted font-mono">#{idx + 1}</span>
              </div>
              <div class="flex items-center gap-1.5 shrink-0">
                <button
                  type="button"
                  onclick={() => handleOpenEditConn(conn)}
                  class="inline-flex items-center gap-1 px-2.5 py-1 text-xs rounded-lg border border-border hover:bg-surface-2 text-text-main transition-colors cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">edit</span>
                  Edit
                </button>
                <button
                  type="button"
                  onclick={() => handleDeleteConn(conn.id)}
                  class="inline-flex items-center gap-1 px-2.5 py-1 text-xs rounded-lg border border-red-500/20 text-red-500 hover:bg-red-500/10 transition-colors cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">delete</span>
                  Delete
                </button>
              </div>
            </div>
          {/each}
          <div class="pt-2">
            <button
              type="button"
              onclick={() => (showAddConnModal = true)}
              class="inline-flex items-center gap-1 px-3 py-1.5 text-xs font-semibold rounded-lg bg-primary text-white hover:bg-primary/90 transition-colors cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">add</span>
              Add
            </button>
          </div>
        </div>
      {/if}
    </div>
  {/if}

  <!-- 4. Models Card (for kinds that have models) -->
  {#if kind !== 'tts' && kind !== 'webSearch' && kind !== 'webFetch'}
    <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-lg font-semibold text-text-main">Models — {kind.toUpperCase()}</h2>
      </div>
      {#if mediaModels.length === 0}
        <p class="text-xs text-text-muted italic">No models registered for this kind.</p>
      {:else}
        <div class="flex flex-wrap gap-3">
          {#each mediaModels as m (m.id)}
            {@const verdict = modelTestResults[m.id]}
            <div
              class="group px-3 py-2 rounded-lg border flex items-center gap-2 hover:bg-sidebar/50
                {verdict?.ok ? 'border-green-500/40' : verdict ? 'border-red-500/40' : 'border-border'}"
            >
              <span
                class="material-symbols-outlined text-base text-text-muted"
                style="color: {verdict?.ok ? '#22c55e' : verdict ? '#ef4444' : 'inherit'}"
              >
                {verdict?.ok ? 'check_circle' : verdict ? 'cancel' : 'smart_toy'}
              </span>
              <div class="flex flex-col gap-1 min-w-0">
                <code class="text-xs text-text-muted font-mono bg-sidebar px-1.5 py-0.5 rounded">{m.id}</code>
                <span class="text-[9px] text-text-muted/70 italic pl-1">{m.name || m.id}</span>
                {#if verdict}
                  <span
                    class="text-[9px] pl-1 max-w-[220px] truncate {verdict.ok ? 'text-green-600 dark:text-green-400' : 'text-red-500'}"
                    title={verdict.error || verdict.note || ''}
                  >
                    {verdict.ok
                      ? `${verdict.latency ?? 0} ms${verdict.note ? ` · ${verdict.note}` : ''}`
                      : verdict.error || 'Test failed'}
                  </span>
                {/if}
              </div>
              <div class="flex items-center gap-1 ml-2">
                <button
                  type="button"
                  onclick={() => handleTestModel(m.id)}
                  disabled={testingModelId === m.id}
                  class="p-1 hover:bg-surface-2 rounded text-text-muted hover:text-primary transition-colors cursor-pointer disabled:cursor-not-allowed"
                  title={verdict?.error || (verdict?.ok ? `Test passed in ${verdict.latency ?? 0} ms` : 'Test Model')}
                >
                  <span class="material-symbols-outlined text-sm {testingModelId === m.id ? 'animate-spin' : ''}">science</span>
                </button>
                <button
                  type="button"
                  onclick={() => handleCopyModel(m.id)}
                  class="p-1 hover:bg-surface-2 rounded text-text-muted hover:text-primary transition-colors cursor-pointer"
                  title="Copy Model ID"
                >
                  <span class="material-symbols-outlined text-sm">{copiedModelId === m.id ? 'check' : 'content_copy'}</span>
                </button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}

  <!-- 5. Config Card -->
  {#if configRows.length > 0 || provider.notice?.text}
    <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-lg font-semibold text-text-main">{kindTitle} Config</h2>
        {#if provider.notice?.apiKeyUrl || provider.website}
          <a
            href={provider.notice?.apiKeyUrl || provider.website}
            target="_blank"
            rel="noopener noreferrer"
            class="text-xs text-primary hover:underline inline-flex items-center gap-1"
          >
            <span class="material-symbols-outlined text-sm">open_in_new</span>
            Get API Key
          </a>
        {/if}
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-2">
        {#each configRows as r (r.key)}
          <div class="flex items-center gap-3 min-w-0">
            <span class="text-xs text-text-muted w-28 shrink-0">{r.label}</span>
            {#if r.isLink}
              <a
                href={r.raw}
                target="_blank"
                rel="noopener noreferrer"
                class="text-sm text-primary hover:underline truncate {r.mono ? 'font-mono' : ''}"
              >
                {r.value}
              </a>
            {:else}
              <span class="text-sm text-text-main truncate {r.mono ? 'font-mono' : ''}">
                {r.value}
              </span>
            {/if}
          </div>
        {/each}
        {#if provider.notice?.text}
          <div class="flex items-start gap-3 min-w-0 sm:col-span-2">
            <span class="text-xs text-text-muted w-28 shrink-0 mt-0.5">Notice</span>
            <span class="text-sm text-text-main leading-relaxed">{provider.notice.text}</span>
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- 6. Example Playground Card -->
  {#if kind === 'tts'}
    <TtsExampleCard providerId={provider.id} {apiKeys} {connections} />
  {:else if kind === 'stt'}
    <SttExampleCard providerId={provider.id} {apiKeys} {connections} />
  {:else}
    <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
    <h2 class="text-lg font-semibold text-text-main mb-4">Example</h2>
    <div class="flex flex-col gap-2.5">
      <!-- Model selector (if media models exist) -->
      {#if mediaModels.length > 0}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Model</span>
          <div class="w-full min-w-0 flex-1">
            <select
              bind:value={selectedModelId}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
            >
              {#each mediaModels as m}
                <option value={m.id}>{m.name || m.id}</option>
              {/each}
            </select>
          </div>
        </div>
      {/if}

      <!-- Endpoint -->
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Endpoint</span>
        <div class="w-full min-w-0 flex-1">
          <span class="px-3 py-1.5 text-sm font-mono text-text-main bg-sidebar rounded-lg truncate block">
            {exampleEndpoint}
          </span>
        </div>
      </div>

      <!-- API Key -->
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">API Key</span>
        <div class="w-full min-w-0 flex-1">
          <span class="px-3 py-1.5 text-sm font-mono text-text-main bg-sidebar rounded-lg truncate block">
            {#if activeApiKey}
              {activeApiKey.slice(0, 8) + '•'.repeat(Math.min(20, Math.max(0, activeApiKey.length - 8)))}
            {:else}
              <span class="text-text-muted italic">No key configured</span>
            {/if}
          </span>
        </div>
      </div>

      <!-- Connection selector -->
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Connection</span>
        <div class="w-full min-w-0 flex-1">
          <select
            bind:value={selectedConnId}
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
          >
            <option value="">Auto (by priority)</option>
            {#each providerConns as conn}
              <option value={conn.id}>{conn.displayName || conn.name || conn.email || conn.id.slice(0, 8)}</option>
            {/each}
          </select>
        </div>
      </div>

      <!-- Kind-specific inputs -->
      {#if kind === 'video' || kind === 'image'}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Prompt</span>
          <div class="w-full min-w-0 flex-1 relative">
            <input
              bind:value={exampleInput}
              class="w-full px-3 py-1.5 pr-7 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
            {#if exampleInput}
              <button
                type="button"
                onclick={() => (exampleInput = '')}
                class="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-primary transition-colors cursor-pointer"
              >
                <span class="material-symbols-outlined text-[14px]">close</span>
              </button>
            {/if}
          </div>
        </div>
      {:else if kind === 'tts'}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Voice</span>
          <div class="w-full min-w-0 flex-1">
            <select
              bind:value={selectedVoice}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
            >
              {#each ttsVoices as v}
                <option value={v}>{v.charAt(0).toUpperCase() + v.slice(1)}</option>
              {/each}
            </select>
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Input</span>
          <div class="w-full min-w-0 flex-1 relative">
            <input
              bind:value={exampleInput}
              class="w-full px-3 py-1.5 pr-7 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
            {#if exampleInput}
              <button
                type="button"
                onclick={() => (exampleInput = '')}
                class="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-primary transition-colors cursor-pointer"
              >
                <span class="material-symbols-outlined text-[14px]">close</span>
              </button>
            {/if}
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Format</span>
          <div class="w-full min-w-0 flex-1">
            <select
              bind:value={ttsResponseFormat}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
            >
              <option value="json">json (base64 preview)</option>
              <option value="mp3">mp3 (direct audio)</option>
              <option value="opus">opus</option>
              <option value="aac">aac</option>
              <option value="flac">flac</option>
              <option value="wav">wav</option>
              <option value="pcm">pcm</option>
            </select>
          </div>
        </div>
      {:else if kind === 'embedding'}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Input</span>
          <div class="w-full min-w-0 flex-1 relative">
            <input
              bind:value={exampleInput}
              class="w-full px-3 py-1.5 pr-7 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
            {#if exampleInput}
              <button
                type="button"
                onclick={() => (exampleInput = '')}
                class="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-primary transition-colors cursor-pointer"
              >
                <span class="material-symbols-outlined text-[14px]">close</span>
              </button>
            {/if}
          </div>
        </div>
      {:else if kind === 'stt'}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Audio File</span>
          <div class="w-full min-w-0 flex-1">
            <input
              type="text"
              bind:value={exampleInput}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Response Format</span>
          <div class="w-full min-w-0 flex-1">
            <select
              bind:value={sttResponseFormat}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
            >
              <option value="json">json</option>
              <option value="text">text</option>
              <option value="srt">srt</option>
              <option value="verbose_json">verbose_json</option>
              <option value="vtt">vtt</option>
            </select>
          </div>
        </div>
      {:else if kind === 'systemone'}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">State</span>
          <div class="w-full min-w-0 flex-1">
            <input
              bind:value={exampleInput}
              placeholder="Situation, support ticket, or text to evaluate"
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Question</span>
          <div class="w-full min-w-0 flex-1">
            <input
              bind:value={exampleQuestion}
              placeholder="Enter evaluation question or criteria"
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
      {:else if kind === 'webSearch'}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Query</span>
          <div class="w-full min-w-0 flex-1 relative">
            <input
              bind:value={exampleInput}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Type</span>
          <div class="w-full min-w-0 flex-1">
            <select
              bind:value={searchType}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
            >
              <option value="web">web</option>
              <option value="news">news</option>
            </select>
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Max results</span>
          <div class="w-full min-w-0 flex-1">
            <input
              type="number"
              min="1"
              max="100"
              bind:value={maxResults}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Country</span>
          <div class="w-full min-w-0 flex-1">
            <input
              type="text"
              bind:value={country}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Language</span>
          <div class="w-full min-w-0 flex-1">
            <input
              type="text"
              bind:value={language}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
      {:else if kind === 'webFetch'}
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">URL</span>
          <div class="w-full min-w-0 flex-1">
            <input
              bind:value={exampleInput}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Format</span>
          <div class="w-full min-w-0 flex-1">
            <select
              bind:value={fetchFormat}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
            >
              <option value="markdown">markdown</option>
              <option value="text">text</option>
              <option value="html">html</option>
            </select>
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
          <span class="w-full text-xs font-medium text-text-muted sm:w-20 sm:shrink-0">Max chars</span>
          <div class="w-full min-w-0 flex-1">
            <input
              type="number"
              min="0"
              bind:value={fetchMaxChars}
              class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
            />
          </div>
        </div>
      {/if}

      <!-- REQUEST Section -->
      <div class="mt-2">
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between mb-1.5">
          <span class="text-xs font-semibold text-text-muted uppercase tracking-wider">Request</span>
          <div class="flex w-full flex-col gap-2 sm:w-auto sm:flex-row sm:items-center">
            <button
              type="button"
              onclick={copyCurl}
              class="inline-flex items-center gap-1 text-xs text-text-muted hover:text-primary transition-colors cursor-pointer mr-2"
            >
              <span class="material-symbols-outlined text-[14px]">content_copy</span>
              {copiedCurl ? 'Copied' : 'Copy'}
            </button>
            <button
              type="button"
              onclick={handleTestExample}
              disabled={isTestingExample}
              class="flex w-full sm:w-auto items-center justify-center gap-1.5 px-3 py-1 rounded-lg bg-primary text-white text-xs font-medium hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
            >
              <span class="material-symbols-outlined text-[14px]">play_arrow</span>
              {isTestingExample ? 'Running...' : 'Run'}
            </button>
          </div>
        </div>
        <pre class="bg-sidebar rounded-lg px-3 py-2.5 text-xs font-mono text-text-main overflow-x-auto whitespace-pre-wrap break-all">{exampleCurl}</pre>
      </div>

      <!-- RESPONSE Section -->
      <div>
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between mb-1.5">
          <span class="text-xs font-semibold text-text-muted uppercase tracking-wider">
            Response {exampleLatency ? `(${exampleLatency}ms)` : ''}
          </span>
        </div>
        <pre class="bg-sidebar rounded-lg px-3 py-2.5 text-xs font-mono text-text-main overflow-x-auto whitespace-pre-wrap break-all opacity-70">{exampleResponse || defaultResponse}</pre>
      </div>
    </div>
    </div>
  {/if}
</div>

<!-- Modal: Add Connection -->
{#if showAddConnModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-[2px] transition-opacity"
      onclick={() => (showAddConnModal = false)}
      role="presentation"
    ></div>
    <div class="relative w-full max-w-md bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] p-6 z-10 flex flex-col gap-4">
      <div class="flex items-center justify-between">
        <h3 class="text-base font-semibold text-text-main">Add {provider.name} Connection</h3>
        <button
          type="button"
          onclick={() => (showAddConnModal = false)}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      {#if newConnError}
        <div class="p-2.5 rounded-lg bg-red-500/10 border border-red-500/30 text-red-500 text-xs">
          {newConnError}
        </div>
      {/if}
      <div class="flex flex-col gap-1.5">
        <label class="text-xs font-medium text-text-muted">API Key / Token</label>
        <input
          type="password"
          bind:value={newConnKey}
          placeholder="Enter API key"
          class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background text-text-main focus:outline-none focus:border-brand-500 font-mono"
        />
      </div>
      <div class="flex flex-col gap-1.5">
        <label class="text-xs font-medium text-text-muted">Display Name (optional)</label>
        <input
          type="text"
          bind:value={newConnName}
          placeholder="e.g. Primary Account"
          class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background text-text-main focus:outline-none focus:border-brand-500"
        />
      </div>
      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border">
        <button
          type="button"
          onclick={() => (showAddConnModal = false)}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={handleSaveNewConnection}
          disabled={isSavingConn}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-primary text-white hover:bg-primary/90 transition-colors cursor-pointer disabled:opacity-50"
        >
          {isSavingConn ? 'Saving...' : 'Add Connection'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Modal: Edit Connection -->
{#if editingConn}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-[2px] transition-opacity"
      onclick={() => (editingConn = null)}
      role="presentation"
    ></div>
    <div class="relative w-full max-w-md bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] p-6 z-10 flex flex-col gap-4">
      <div class="flex items-center justify-between">
        <h3 class="text-base font-semibold text-text-main">Edit Connection</h3>
        <button
          type="button"
          onclick={() => (editingConn = null)}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      <div class="flex flex-col gap-1.5">
        <label class="text-xs font-medium text-text-muted">Display Name</label>
        <input
          type="text"
          bind:value={editConnName}
          class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background text-text-main focus:outline-none focus:border-brand-500"
        />
      </div>
      <div class="flex items-center justify-between py-1">
        <span class="text-xs font-medium text-text-muted">Active</span>
        <input type="checkbox" bind:checked={isEditingActive} class="size-4 rounded text-brand-500 focus:ring-brand-500" />
      </div>
      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border">
        <button
          type="button"
          onclick={() => (editingConn = null)}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={handleSaveEditedConn}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-primary text-white hover:bg-primary/90 transition-colors cursor-pointer"
        >
          Save
        </button>
      </div>
    </div>
  </div>
{/if}
