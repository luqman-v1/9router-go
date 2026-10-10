<script lang="ts">
  import { onMount } from 'svelte'
  import { api, getStoredAPIKey, type APIKey, type ProviderConnection } from '../../api/client'
  import { copyToClipboard } from '../../lib/clipboard'
  import { getModelKind, getModelsByProviderId } from '../../lib/models'
  import { TTS_PROVIDER_CONFIG } from '../../lib/ttsProviders'
  import Card from '../../lib/ui/Card.svelte'

  interface Props {
    providerId: string
    apiKeys?: APIKey[]
    connections?: ProviderConnection[]
  }

  let { providerId, apiKeys = [], connections = [] }: Props = $props()

  const DEFAULT_TTS_RESPONSE_EXAMPLE = `// Audio will appear here after running.
// Example JSON response (response_format=json):
{
  "format": "mp3",
  "audio": "//NExAANaAIIAUAAANNNNNNNN..." // base64 encoded MP3
}`

  let config = $derived(TTS_PROVIDER_CONFIG[providerId] || TTS_PROVIDER_CONFIG['edge-tts'])

  let selectedVoice = $state('')
  let selectedVoiceName = $state('')
  let voiceId = $state('')
  let countryVoices = $state<Array<{ id: string; name: string }>>([])
  let selectedLang = $state('')
  let selectedModel = $state('')

  let input = $state('Hello, this is a text to speech test.')
  let style = $state('')
  let languageHint = $state('')
  let responseFormat = $state<'mp3' | 'json'>('mp3')
  let audioUrl = $state('')
  let jsonResponse = $state<any>(null)
  let running = $state(false)
  let error = $state('')
  let latency = $state<number | null>(null)
  let copiedCurl = $state(false)

  // Language modal state
  let modalOpen = $state(false)
  let languages = $state<Array<{ code: string; name: string; voices: Array<{ id: string; name: string }> }>>([])
  let modalLoading = $state(false)
  let modalSearch = $state('')
  let modalError = $state('')
  let byLang = $state<Record<string, any>>({})

  let endpoint = $state(typeof window !== 'undefined' ? window.location.origin : 'http://localhost:20130')
  let tunnelEndpoint = $state('')
  let useTunnel = $state(false)

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
      endpoint = window.location.origin
    }
    api.getTunnelStatus?.().then((d: any) => {
      const pub = d?.tunnel?.publicUrl || d?.tunnel?.tunnelUrl || d?.publicUrl
      if (pub) tunnelEndpoint = pub
    }).catch(() => {})
    selectedVoice = config.defaultVoiceId || ''
    voiceId = config.defaultVoiceId || ''

    if (config.hasModelSelector) {
      const models = getModelsByProviderId(config.modelKey || providerId).filter((m) => getModelKind(m) === 'tts')
      if (models.length > 0) {
        selectedModel = models[0].id
      }
    }
  })

  async function openModal() {
    modalOpen = true
    modalSearch = ''
    modalError = ''
    if (languages.length > 0) return

    modalLoading = true
    try {
      if (config.voiceSource === 'hardcoded') {
        const voiceKey = config.voiceKey || providerId
        const voices = getModelsByProviderId(voiceKey).filter((m) => getModelKind(m) === 'tts')
        const byLangMap: Record<string, any> = {}
        for (const v of voices) {
          if (!byLangMap[v.id]) {
            byLangMap[v.id] = { code: v.id, name: v.name || v.id, voices: [{ id: v.id, name: v.name || v.id }] }
          }
        }
        byLang = byLangMap
        languages = Object.values(byLangMap).sort((a, b) => a.name.localeCompare(b.name))
      } else {
        const url = config.apiEndpoint
          ? config.apiEndpoint
          : `/api/media-providers/tts/voices?provider=${providerId === 'local-device' ? 'local-device' : 'edge-tts'}`
        const r = await fetch(url)
        if (!r.ok) {
          const err = await r.json().catch(() => ({}))
          throw new Error(err.error || `HTTP ${r.status}`)
        }
        const d = await r.json()
        languages = d.languages || []
        byLang = d.byLang || {}
      }
    } catch (e: any) {
      modalError = e.message || 'Failed to load voices'
    } finally {
      modalLoading = false
    }
  }

  function handlePickLanguage(lang: { code: string; name: string; voices: Array<{ id: string; name: string }> }) {
    modalOpen = false
    selectedLang = lang.code
    const voices = byLang[lang.code]?.voices || []
    countryVoices = voices
    if (voices.length > 0) {
      selectedVoice = voices[0].id
      selectedVoiceName = voices[0].name
      if (config.hasVoiceIdInput) voiceId = voices[0].id
    }
  }

  let filteredLanguages = $derived(
    modalSearch
      ? languages.filter((c) =>
          c.name.toLowerCase().includes(modalSearch.toLowerCase()) ||
          c.code.toLowerCase().includes(modalSearch.toLowerCase())
        )
      : languages
  )

  let activeVoiceId = $derived(config.hasVoiceIdInput ? (voiceId || selectedVoice) : selectedVoice)

  let modelFull = $derived.by(() => {
    const alias = providerId
    // Upstream parity (ee323400): a self-hosted server decides which models and
    // voices exist, so both halves are typed by hand and the voice is optional.
    if (providerId === 'selfhosted-tts') {
      const model = selectedModel.trim()
      const voice = activeVoiceId.trim()
      return model ? `${alias}/${model}${voice ? `/${voice}` : ''}` : ''
    }
    if (config.hasModelSelector && selectedModel && activeVoiceId) return `${alias}/${selectedModel}/${activeVoiceId}`
    if (config.hasModelSelector && selectedModel) return `${alias}/${selectedModel}`
    if (activeVoiceId) return `${alias}/${activeVoiceId}`
    return ''
  })

  let ttsBody = $derived.by(() => {
    const b: Record<string, any> = { model: modelFull, input }
    if (config.hasLanguageHint && languageHint) b.language = languageHint
    if (config.hasStyleInput && style.trim()) b.style = style.trim()
    return b
  })

  let effectiveEndpoint = $derived(useTunnel && tunnelEndpoint ? tunnelEndpoint : endpoint)

  let curlSnippet = $derived.by(() => {
    const key = activeApiKey || 'YOUR_KEY'
    const url = `${effectiveEndpoint}/v1/audio/speech${responseFormat === 'json' ? '?response_format=json' : ''}`
    return `curl -X POST ${url} \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer ${key}" \\\n  -d '${JSON.stringify(ttsBody)}' \\\n  ${responseFormat === 'json' ? '' : '--output speech.mp3'}`
  })

  async function handleCopyCurl() {
    await copyToClipboard(curlSnippet)
    copiedCurl = true
    setTimeout(() => { copiedCurl = false }, 2000)
  }

  async function handleRun() {
    error = ''
    audioUrl = ''
    jsonResponse = null
    running = true
    const start = performance.now()
    try {
      const headers: Record<string, string> = { 'Content-Type': 'application/json' }
      if (activeApiKey) headers['Authorization'] = `Bearer ${activeApiKey}`
      const url = `${effectiveEndpoint}/v1/audio/speech${responseFormat === 'json' ? '?response_format=json' : ''}`
      const res = await fetch(url, {
        method: 'POST',
        headers,
        body: JSON.stringify(ttsBody),
      })
      latency = Math.round(performance.now() - start)
      if (!res.ok) {
        const text = await res.text().catch(() => '')
        let msg = `HTTP ${res.status}`
        try {
          const json = JSON.parse(text)
          msg = json.error?.message || json.message || text || msg
        } catch { if (text) msg = text }
        throw new Error(msg)
      }
      if (responseFormat === 'json') {
        const json = await res.json()
        jsonResponse = json
        if (json.audio) {
          const b64 = json.audio
          const mime = json.format === 'wav' ? 'audio/wav' : json.format === 'ogg' ? 'audio/ogg' : 'audio/mpeg'
          audioUrl = `data:${mime};base64,${b64}`
        }
      } else {
        const blob = await res.blob()
        audioUrl = URL.createObjectURL(blob)
      }
    } catch (e: any) {
      error = e.message || 'Network error'
    } finally {
      running = false
    }
  }
</script>

<Card>
  <h2 class="text-lg font-semibold text-text-main mb-4">Example</h2>

  <div class="flex flex-col gap-2.5">
    <!-- Endpoint Row -->
    <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
      <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Endpoint</span>
      <div class="flex w-full flex-col gap-2 sm:w-auto sm:flex-1 sm:flex-row sm:items-center">
        <span class="w-full min-w-0 flex-1 px-3 py-1.5 text-sm font-mono text-text-main bg-sidebar rounded-lg truncate">
          {effectiveEndpoint}/v1/audio/speech
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

    <!-- Model Selector (if supported) -->
    {#if providerId === 'selfhosted-tts'}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Model</span>
        <div class="w-full min-w-0 flex-1">
          <input
            bind:value={selectedModel}
            placeholder="e.g. kokoro or Qwen3-TTS-12Hz-1.7B-CustomVoice-bf16"
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
          />
        </div>
      </div>
    {:else if config.hasModelSelector}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Model</span>
        <div class="w-full min-w-0 flex-1">
          <select
            bind:value={selectedModel}
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
          >
            {#each getModelsByProviderId(config.modelKey || providerId).filter((m) => getModelKind(m) === 'tts') as m (m.id)}
              <option value={m.id}>{m.name || m.id}</option>
            {/each}
          </select>
        </div>
      </div>
    {/if}

    <!-- Language row + Browse button (edge-tts, local-device, elevenlabs) -->
    {#if config.hasBrowseButton}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Language</span>
        <div class="flex w-full flex-col gap-2 sm:w-auto sm:flex-1 sm:flex-row sm:items-center">
          <button
            type="button"
            onclick={openModal}
            class="w-full min-w-0 flex-1 px-3 py-1.5 text-sm border border-border rounded-lg bg-background font-mono truncate text-left hover:border-primary/40 transition-colors cursor-pointer"
          >
            {#if selectedLang}
              <span class="text-text-main">{languages.find((l) => l.code === selectedLang)?.name || selectedLang}</span>
            {:else}
              <span class="text-text-muted">No language selected</span>
            {/if}
          </button>
          <button
            type="button"
            onclick={openModal}
            class="flex w-full items-center justify-center gap-1 text-xs px-2.5 py-1.5 rounded-lg border border-border text-text-muted hover:text-primary hover:border-primary/40 transition-colors sm:w-auto sm:shrink-0 cursor-pointer"
          >
            <span class="material-symbols-outlined text-[14px]">language</span>
            Select language
          </button>
        </div>
      </div>
    {/if}

    <!-- Voice chips -->
    {#if countryVoices.length > 0}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Voice</span>
        <div class="w-full min-w-0 flex-1 flex flex-wrap gap-1.5">
          {#each countryVoices as v (v.id)}
            <button
              type="button"
              onclick={() => {
                selectedVoice = v.id
                selectedVoiceName = v.name
                if (config.hasVoiceIdInput) voiceId = v.id
              }}
              class="text-xs px-2.5 py-1 rounded-lg border transition-colors cursor-pointer {activeVoiceId === v.id ? 'border-primary bg-primary/10 text-primary font-medium' : 'border-border text-text-muted hover:border-primary/40 hover:text-text-main'}"
            >
              {v.name}
            </button>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Voice ID manual input -->
    {#if config.hasVoiceIdInput}
      <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
        <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Voice ID</span>
        <div class="w-full min-w-0 flex-1">
          <input
            bind:value={voiceId}
            placeholder={providerId === 'selfhosted-tts' ? 'e.g. af_heart or vivian (optional)' : 'Custom voice ID or name...'}
            class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
          />
        </div>
      </div>
    {/if}

    <!-- Input row -->
    <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
      <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Input</span>
      <div class="w-full min-w-0 flex-1 relative">
        <input
          bind:value={input}
          class="w-full px-3 py-1.5 pr-7 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main font-mono"
        />
        {#if input}
          <button
            type="button"
            onclick={() => (input = '')}
            class="absolute right-2 top-1/2 -translate-y-1/2 text-text-muted hover:text-primary transition-colors cursor-pointer"
          >
            <span class="material-symbols-outlined text-[14px]">close</span>
          </button>
        {/if}
      </div>
    </div>

    <!-- Output Format selector -->
    <div class="flex min-w-0 flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-3">
      <span class="w-full text-xs font-medium text-text-muted sm:w-24 sm:shrink-0">Output Format</span>
      <div class="w-full min-w-0 flex-1">
        <select
          bind:value={responseFormat}
          class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
        >
          <option value="mp3">MP3 (Binary)</option>
          <option value="json">JSON (base64 audio)</option>
        </select>
      </div>
    </div>

    <!-- Request curl snippet -->
    <div class="mt-2">
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
            disabled={running}
            class="inline-flex items-center justify-center gap-1.5 h-8 px-4 rounded-lg bg-primary hover:bg-primary-hover text-white text-xs font-semibold transition-colors disabled:opacity-50 cursor-pointer shadow-sm"
          >
            <span class="material-symbols-outlined text-[16px]">{running ? 'hourglass_empty' : 'play_arrow'}</span>
            {running ? 'Generating...' : 'Run'}
          </button>
        </div>
      </div>
      <pre class="bg-sidebar rounded-lg px-3 py-2.5 text-xs font-mono text-text-main overflow-x-auto whitespace-pre-wrap break-all">{curlSnippet}</pre>
    </div>

    {#if error}
      <p class="text-xs text-red-500 break-words mt-1">{error}</p>
    {/if}

    <!-- Audio player / Response -->
    {#if audioUrl}
      <div class="mt-2">
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between mb-1.5">
          <span class="text-xs font-semibold text-text-muted uppercase tracking-wider">
            Response {#if latency}<span class="font-normal normal-case">⚡ {latency}ms</span>{/if}
          </span>
          <a
            href={audioUrl}
            download="speech.mp3"
            class="inline-flex items-center gap-1 text-xs text-text-muted hover:text-primary transition-colors"
          >
            <span class="material-symbols-outlined text-[14px]">download</span>
            Download
          </a>
        </div>
        <audio controls src={audioUrl} class="w-full mt-1"></audio>

        {#if jsonResponse}
          <div class="mt-3">
            <span class="text-xs font-semibold text-text-muted uppercase tracking-wider">JSON Response</span>
            <pre class="mt-1 bg-sidebar rounded-lg px-3 py-2.5 text-xs font-mono text-text-main overflow-x-auto whitespace-pre-wrap break-all">
{JSON.stringify({
  format: jsonResponse.format,
  audio: jsonResponse.audio ? `${jsonResponse.audio.substring(0, 100)}...` : ''
}, null, 2)}
            </pre>
          </div>
        {/if}
      </div>
    {:else}
      <div class="mt-2">
        <span class="text-xs font-semibold text-text-muted uppercase tracking-wider">Response</span>
        <pre class="mt-1.5 bg-sidebar rounded-lg px-3 py-2.5 text-xs font-mono text-text-main overflow-x-auto whitespace-pre-wrap break-all opacity-50">{DEFAULT_TTS_RESPONSE_EXAMPLE}</pre>
      </div>
    {/if}
  </div>
</Card>

<!-- Select Language Modal -->
{#if modalOpen}
  <div
    class="fixed inset-0 z-50 flex items-end justify-center sm:items-center"
    style="background-color: rgba(0,0,0,0.6); backdrop-filter: blur(2px);"
    onclick={() => (modalOpen = false)}
    onkeydown={(e) => { if (e.key === 'Escape') modalOpen = false }}
    role="dialog"
    tabindex="-1"
  >
    <div
      class="border border-border rounded-xl shadow-2xl w-full max-w-md mx-4 flex flex-col max-h-[80vh] bg-surface"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="document"
      tabindex="0"
    >
      <!-- Header -->
      <div class="flex items-center justify-between px-4 py-3 border-b border-border shrink-0 rounded-t-xl">
        <h3 class="text-sm font-semibold text-text-main">Select Language</h3>
        <button
          type="button"
          onclick={() => (modalOpen = false)}
          class="text-text-muted hover:text-primary transition-colors cursor-pointer"
        >
          <span class="material-symbols-outlined text-[20px]">close</span>
        </button>
      </div>

      <!-- Search -->
      <div class="px-4 py-2.5 border-b border-border shrink-0">
        <input
          bind:value={modalSearch}
          placeholder="Search language..."
          class="w-full px-3 py-1.5 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary text-text-main"
        />
      </div>

      <!-- Language list -->
      <div class="overflow-y-auto flex-1 p-2">
        {#if modalError}
          <p class="text-xs text-red-500 px-2 py-1">{modalError}</p>
        {/if}
        {#if modalLoading}
          <p class="text-xs text-text-muted px-2 py-3">Loading voices...</p>
        {:else}
          <div class="flex flex-col gap-0.5">
            {#each filteredLanguages as c (c.code)}
              <button
                type="button"
                onclick={() => handlePickLanguage(c)}
                class="flex items-center justify-between w-full px-3 py-2 rounded-lg text-left hover:bg-sidebar transition-colors cursor-pointer {selectedLang === c.code ? 'bg-primary/10 text-primary' : 'text-text-main'}"
              >
                <span class="text-sm font-medium">{c.name}</span>
                <div class="flex items-center gap-2 shrink-0">
                  <span class="text-xs text-text-muted">{c.voices?.length || 0} voices</span>
                  {#if selectedLang === c.code}
                    <span class="material-symbols-outlined text-[16px] text-primary">check</span>
                  {/if}
                </div>
              </button>
            {/each}
            {#if filteredLanguages.length === 0}
              <p class="text-xs text-text-muted px-2 py-3">No languages found.</p>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}
