<script lang="ts">
  import { onMount } from 'svelte'
  import { api, getStoredAPIKey, type APIKey, type ProviderConnection } from '../../api/client'
  import { copyToClipboard } from '../../lib/clipboard'
  import { getModelKind, getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
  import { parseCustomModelsResponse, subscribeCustomModelsChanged } from '../../lib/customModels'
  import Card from '../../lib/ui/Card.svelte'

  interface Props {
    providerId: string
    apiKeys?: APIKey[]
    connections?: ProviderConnection[]
  }

  let { providerId, apiKeys = [], connections = [] }: Props = $props()

  let builtinSttModels = $derived(getModelsByProviderId(providerId).filter((m) => getModelKind(m) === 'stt'))
  let customSttModels = $state<any[]>([])
  let sttModels = $derived([...builtinSttModels, ...customSttModels])

  let selectedModel = $state('')
  $effect(() => {
    if (!selectedModel && sttModels.length > 0) {
      selectedModel = sttModels[0].id
    }
  })

  let selectedModelObj = $derived(sttModels.find((m) => m.id === selectedModel))
  let allowedParams = $derived(Array.isArray(selectedModelObj?.params) ? selectedModelObj.params : ['response_format'])

  let audioFile = $state<File | null>(null)
  let language = $state('')
  let prompt = $state('')
  let responseFormat = $state('json')
  let temperature = $state('')
  let granularity = $state('')
  let tagAudioEvents = $state(false)
  let diarize = $state(false)
  let numSpeakers = $state('')
  let useTunnel = $state(false)
  let localEndpoint = $state(typeof window !== 'undefined' ? window.location.origin : 'http://localhost:20130')
  let tunnelEndpoint = $state('')
  let result = $state<any>(null)
  let latency = $state<number | null>(null)
  let running = $state(false)
  let error = $state('')
  let copiedCurl = $state(false)
  let copiedRes = $state(false)

  let activeApiKey = $state('')
  let connectionCount = $derived(connections.filter((c) => c.provider === providerId && c.isActive !== 0).length)

  onMount(() => {
    const stored = getStoredAPIKey()
    if (stored) {
      activeApiKey = stored
    } else {
      const active = (apiKeys || []).find((k) => k.isActive !== 0 && k.key)
      if (active?.key) activeApiKey = active.key
    }
    if (typeof window !== 'undefined') {
      localEndpoint = window.location.origin
    }
    api.getTunnelStatus?.().then((d: any) => {
      const pub = d?.tunnel?.publicUrl || d?.tunnel?.tunnelUrl || d?.publicUrl
      if (pub) tunnelEndpoint = pub
    }).catch(() => {})

    // Upstream parity: (d.models || []) filtered by kind + providerAlias,
    // reload on focus + customModelChanged (upstream SttExampleCard loadCustom).
    const storageAlias = PROVIDER_ID_TO_ALIAS[providerId] || providerId
    const loadCustom = () => {
      api.getCustomModels?.().then((d: any) => {
        const list = parseCustomModelsResponse(d).filter(
          (m: any) => getModelKind(m) === 'stt' && (m.providerAlias === storageAlias || m.providerAlias === providerId)
        )
        customSttModels = list
      }).catch(() => {})
    }
    loadCustom()
    return subscribeCustomModelsChanged(loadCustom)
  })

  let effectiveEndpoint = $derived(useTunnel && tunnelEndpoint ? tunnelEndpoint : localEndpoint)
  let modelFull = $derived(selectedModel ? `${providerId}/${selectedModel}` : '')

  let curlSnippet = $derived.by(() => {
    const key = activeApiKey || 'YOUR_KEY'
    let s = `curl -X POST ${effectiveEndpoint}/v1/audio/transcriptions \\\n  -H "Authorization: Bearer ${key}" \\\n  -F "file=@${audioFile?.name || 'audio.mp3'}" \\\n  -F "model=${modelFull}"`
    if (allowedParams.includes('language') && language) {
      s += ` \\\n  -F "language=${language}"`
    }
    if (allowedParams.includes('response_format')) {
      s += ` \\\n  -F "response_format=${responseFormat}"`
    }
    if (allowedParams.includes('temperature') && temperature) {
      s += ` \\\n  -F "temperature=${temperature}"`
    }
    if (allowedParams.includes('prompt') && prompt) {
      s += ` \\\n  -F "prompt=${prompt}"`
    }
    if (allowedParams.includes('timestamps_granularity') && granularity) {
      s += ` \\\n  -F "timestamps_granularity=${granularity}"`
    }
    if (allowedParams.includes('tag_audio_events') && tagAudioEvents) {
      s += ` \\\n  -F "tag_audio_events=true"`
    }
    // diarize and num_speakers are mutually exclusive upstream — only one goes out.
    if (allowedParams.includes('diarize') && diarize) {
      s += ` \\\n  -F "diarize=true"`
    } else if (allowedParams.includes('num_speakers') && numSpeakers) {
      s += ` \\\n  -F "num_speakers=${numSpeakers}"`
    }
    return s
  })

  async function handleCopyCurl() {
    await copyToClipboard(curlSnippet)
    copiedCurl = true
    setTimeout(() => { copiedCurl = false }, 2000)
  }

  async function handleCopyRes() {
    if (!resultStr) return
    await copyToClipboard(resultStr)
    copiedRes = true
    setTimeout(() => { copiedRes = false }, 2000)
  }

  async function handleRun() {
    if (!audioFile || !modelFull) return
    running = true
    error = ''
    result = null
    const start = performance.now()
    try {
      const fd = new FormData()
      fd.append('file', audioFile)
      fd.append('model', modelFull)
      if (allowedParams.includes('language') && language) fd.append('language', language)
      if (allowedParams.includes('response_format')) fd.append('response_format', responseFormat)
      if (allowedParams.includes('temperature') && temperature) fd.append('temperature', temperature)
      if (allowedParams.includes('prompt') && prompt) fd.append('prompt', prompt)
      if (allowedParams.includes('timestamps_granularity') && granularity) fd.append('timestamps_granularity', granularity)
      if (allowedParams.includes('tag_audio_events') && tagAudioEvents) fd.append('tag_audio_events', 'true')
      // diarize and num_speakers are mutually exclusive upstream — only one goes out.
      if (allowedParams.includes('diarize') && diarize) {
        fd.append('diarize', 'true')
      } else if (allowedParams.includes('num_speakers') && numSpeakers) {
        fd.append('num_speakers', numSpeakers)
      }

      const headers: Record<string, string> = {}
      if (activeApiKey) headers['Authorization'] = `Bearer ${activeApiKey}`
      const res = await fetch(`${effectiveEndpoint}/v1/audio/transcriptions`, {
        method: 'POST',
        headers,
        body: fd,
      })
      latency = Math.round(performance.now() - start)
      const ct = res.headers.get('content-type') || ''
      const data = ct.includes('application/json') ? await res.json() : await res.text()
      if (!res.ok) {
        error = data?.error?.message || data?.error || (typeof data === 'string' ? data : `HTTP ${res.status}`)
        return
      }
      result = data
    } catch (e: any) {
      error = e.message || 'Network error'
    } finally {
      running = false
    }
  }

  let resultStr = $derived(
    typeof result === 'string'
      ? result
      : result
        ? JSON.stringify(result, null, 2)
        : '{\n  "text": "Hello world..."\n}'
  )
</script>

<Card>
  <h2 class="text-lg font-semibold text-text-main mb-4">Example</h2>
  <div class="flex flex-col gap-2.5">
    <!-- Model Row -->
    {#if sttModels.length > 0}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Model</span>
        <div class="w-full min-w-0 flex-1">
          <select
            bind:value={selectedModel}
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
          >
            {#each sttModels as m (m.id)}
              <option value={m.id}>{m.name || m.id}</option>
            {/each}
          </select>
        </div>
      </div>
    {:else}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Model</span>
        <div class="w-full min-w-0 flex-1">
          <input
            bind:value={selectedModel}
            placeholder="Enter model id"
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
          />
        </div>
      </div>
    {/if}

    <!-- Endpoint Row -->
    <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
      <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Endpoint</span>
      <div class="flex w-full flex-col gap-2 sm:w-auto sm:flex-1 sm:flex-row sm:items-center">
        <span class="w-full min-w-0 flex-1 px-3 py-1.5 text-sm font-mono text-text-main bg-sidebar rounded-lg truncate">
          {effectiveEndpoint}/v1/audio/transcriptions
        </span>
        {#if tunnelEndpoint}
          <button
            type="button"
            onclick={() => (useTunnel = !useTunnel)}
            title={useTunnel ? 'Using tunnel' : 'Using local'}
            class="flex items-center gap-1 text-xs px-2 py-1.5 rounded-lg border shrink-0 transition-colors cursor-pointer {useTunnel ? 'border-primary/40 bg-primary/10 text-primary' : 'border-border text-text-muted hover:text-primary'}"
          >
            <span class="material-symbols-outlined text-[14px]">wifi_tethering</span>
            Tunnel
          </button>
        {/if}
      </div>
    </div>

    <!-- API Key Row -->
    <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
      <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">API Key</span>
      <div class="w-full min-w-0 flex-1">
        <span class="px-3 py-1.5 text-sm font-mono text-text-main bg-sidebar rounded-lg truncate block">
          {#if activeApiKey}
            {activeApiKey.slice(0, 8)}{'•'.repeat(Math.min(20, Math.max(0, activeApiKey.length - 8)))}
          {:else if connectionCount > 0}
            <span class="text-text-muted italic">Using stored key(s) · {connectionCount} connection{connectionCount > 1 ? 's' : ''}</span>
          {:else}
            <span class="text-text-muted italic">No key configured</span>
          {/if}
        </span>
      </div>
    </div>

    <!-- Audio file input -->
    <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
      <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Audio File</span>
      <div class="flex flex-col gap-1.5 flex-1 min-w-0">
        <input
          type="file"
          accept="audio/*,video/mp4,.m4a,.mp3,.wav,.ogg,.flac,.webm,.opus"
          onchange={(e) => {
            const target = e.currentTarget as HTMLInputElement
            audioFile = target.files?.[0] || null
          }}
          class="w-full text-xs text-text-muted file:mr-2 file:py-1.5 file:px-3 file:rounded-lg file:border file:border-border file:bg-background file:text-text-main hover:file:bg-sidebar file:cursor-pointer"
        />
        {#if audioFile}
          <span class="text-xs text-text-muted font-mono">
            {audioFile.name} · {(audioFile.size / 1024).toFixed(1)} KB
          </span>
        {/if}
      </div>
    </div>

    <!-- Language (if model supports) -->
    {#if allowedParams.includes('language')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Language</span>
        <div class="w-full min-w-0 flex-1">
          <input
            bind:value={language}
            placeholder="e.g. en, vi, id, ja (auto-detect if empty)"
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
          />
        </div>
      </div>
    {/if}

    <!-- Prompt (if model supports) -->
    {#if allowedParams.includes('prompt')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Prompt</span>
        <div class="w-full min-w-0 flex-1">
          <input
            bind:value={prompt}
            placeholder="optional context to improve accuracy"
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
          />
        </div>
      </div>
    {/if}

    <!-- Temperature (if model supports) -->
    {#if allowedParams.includes('temperature')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Temperature</span>
        <div class="w-full min-w-0 flex-1">
          <input
            type="number"
            step="0.1"
            min="0"
            max="1"
            bind:value={temperature}
            placeholder="0 - 1 (default 0)"
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
          />
        </div>
      </div>
    {/if}

    <!-- Response format (if model supports) -->
    {#if allowedParams.includes('response_format')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Response Format</span>
        <div class="w-full min-w-0 flex-1">
          <select
            bind:value={responseFormat}
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
    {/if}

    <!-- Scribe-style extras — shown only when the model declares the param -->
    {#if allowedParams.includes('timestamps_granularity')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Timestamps</span>
        <div class="w-full min-w-0 flex-1">
          <select
            bind:value={granularity}
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
          >
            <option value="">provider default</option>
            <option value="word">word</option>
            <option value="character">character</option>
            <option value="none">none</option>
          </select>
        </div>
      </div>
    {/if}

    {#if allowedParams.includes('tag_audio_events')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Tag audio events</span>
        <div class="w-full min-w-0 flex-1">
          <label class="flex items-center gap-2 text-sm text-text-muted">
            <input
              type="checkbox"
              bind:checked={tagAudioEvents}
              class="accent-primary"
            />
            Label non-speech sounds as [laughter], [applause], ...
          </label>
        </div>
      </div>
    {/if}

    {#if allowedParams.includes('diarize')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Speaker labels</span>
        <div class="w-full min-w-0 flex-1">
          <label class="flex items-center gap-2 text-sm text-text-muted">
            <input
              type="checkbox"
              bind:checked={diarize}
              class="accent-primary"
            />
            Detect speakers automatically (overrides speaker count)
          </label>
        </div>
      </div>
    {/if}

    {#if allowedParams.includes('num_speakers')}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Speaker count</span>
        <div class="w-full min-w-0 flex-1">
          <input
            type="number"
            step="1"
            min="1"
            max="32"
            bind:value={numSpeakers}
            disabled={diarize}
            placeholder="auto-detect"
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main disabled:opacity-50"
          />
        </div>
      </div>
    {/if}

    <!-- Curl + Run -->
    <div class="mt-1">
      <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between mb-1.5">
        <span class="text-xs font-semibold text-text-muted uppercase tracking-wider">Request</span>
        <div class="flex w-full flex-col gap-2 sm:w-auto sm:flex-row sm:items-center">
          <button
            type="button"
            onclick={handleCopyCurl}
            class="inline-flex items-center gap-1 text-xs text-text-muted hover:text-primary transition-colors cursor-pointer"
          >
            <span class="material-symbols-outlined text-[14px]">{copiedCurl ? 'check' : 'content_copy'}</span>
            {copiedCurl ? 'Copied' : 'Copy'}
          </button>
          <button
            type="button"
            onclick={handleRun}
            disabled={running || !audioFile || !modelFull}
            class="flex w-full sm:w-auto items-center justify-center gap-1.5 px-3 py-1 rounded-lg bg-primary text-white text-xs font-medium hover:bg-primary/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
          >
            <span class="material-symbols-outlined text-[14px] {running ? 'animate-spin' : ''}">
              play_arrow
            </span>
            {running ? 'Transcribing...' : 'Run'}
          </button>
        </div>
      </div>
      <pre class="bg-sidebar rounded-lg px-3 py-2.5 text-xs font-mono text-text-main overflow-x-auto whitespace-pre-wrap break-all">{curlSnippet}</pre>
    </div>

    {#if error}
      <p class="text-xs text-red-500 break-words mt-1">{error}</p>
    {/if}

    <!-- Response preview -->
    <div class="mt-1">
      <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between mb-1.5">
        <span class="text-xs font-semibold text-text-muted uppercase tracking-wider">
          Response {#if result && latency}<span class="font-normal normal-case">⚡ {latency}ms</span>{/if}
        </span>
        {#if result}
          <button
            type="button"
            onclick={handleCopyRes}
            class="inline-flex items-center gap-1 text-xs text-text-muted hover:text-primary transition-colors cursor-pointer"
          >
            <span class="material-symbols-outlined text-[14px]">{copiedRes ? 'check' : 'content_copy'}</span>
            {copiedRes ? 'Copied' : 'Copy'}
          </button>
        {/if}
      </div>
      <pre class="bg-sidebar rounded-lg px-3 py-2.5 text-xs font-mono text-text-main overflow-x-auto whitespace-pre-wrap break-all opacity-70">
{resultStr}
      </pre>
    </div>
  </div>
</Card>
