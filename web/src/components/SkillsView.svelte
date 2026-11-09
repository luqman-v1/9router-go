<script lang="ts">
  import { copyToClipboard } from '../lib/clipboard'

  interface Skill {
    id: string
    name: string
    description: string
    endpoint: string | null
    icon: string
    isEntry?: boolean
  }

  const repo = 'luqman-v1/9router-go'
  const branch = 'main'
  const baseFolder = 'skills'
  const githubRepoUrl = `https://github.com/${repo}`
  const rawBaseUrl = `https://raw.githubusercontent.com/${repo}/refs/heads/${branch}/${baseFolder}`
  const blobBaseUrl = `https://github.com/${repo}/blob/${branch}/${baseFolder}`

  const skills: Skill[] = [
    {
      id: '9router',
      name: '9router-go (Entry)',
      description:
        'Setup + index of all capabilities. Start here — covers base URL, auth, model discovery, and links to every capability skill.',
      endpoint: null,
      icon: 'hub',
      isEntry: true,
    },
    {
      id: '9router-chat',
      name: 'Chat',
      description: 'Chat / code-gen via OpenAI or Anthropic format with streaming.',
      endpoint: '/v1/chat/completions',
      icon: 'chat',
    },
    {
      id: '9router-image',
      name: 'Image Generation',
      description: 'Text-to-image via DALL-E, Imagen, FLUX, MiniMax, SDWebUI…',
      endpoint: '/v1/images/generations',
      icon: 'image',
    },
    {
      id: '9router-tts',
      name: 'Text-to-Speech',
      description: 'OpenAI / ElevenLabs / Edge / Google / Deepgram voices.',
      endpoint: '/v1/audio/speech',
      icon: 'record_voice_over',
    },
    {
      id: '9router-stt',
      name: 'Speech-to-Text',
      description: 'Transcribe audio via OpenAI Whisper, Groq, Gemini, Deepgram, AssemblyAI…',
      endpoint: '/v1/audio/transcriptions',
      icon: 'mic',
    },
    {
      id: '9router-video',
      name: 'Video Generation',
      description: 'Text-to-video via Kling, Hailuo, Runway, Luma, Pika…',
      endpoint: '/v1/video/generations',
      icon: 'movie',
    },
    {
      id: '9router-embeddings',
      name: 'Embeddings',
      description: 'Vectors for RAG / semantic search via OpenAI, Gemini, Mistral…',
      endpoint: '/v1/embeddings',
      icon: 'scatter_plot',
    },
    {
      id: '9router-web-search',
      name: 'Web Search',
      description:
        'Web and X search via Tavily / Exa / Brave / Serper / SearXNG / Google PSE / You.com / Xquik.',
      endpoint: '/v1/search',
      icon: 'search',
    },
    {
      id: '9router-web-fetch',
      name: 'Web Fetch',
      description: 'URL → markdown / text / HTML via Firecrawl, Jina, Tavily, Exa.',
      endpoint: '/v1/web/fetch',
      icon: 'language',
    },
  ]

  let copiedMap = $state<Record<string, boolean>>({})

  function getSkillRawUrl(id: string): string {
    return `${rawBaseUrl}/${id}/SKILL.md`
  }

  function getSkillBlobUrl(id: string): string {
    return `${blobBaseUrl}/${id}/SKILL.md`
  }

  async function handleCopy(key: string, text: string) {
    const ok = await copyToClipboard(text)
    if (ok) {
      copiedMap[key] = true
      setTimeout(() => {
        copiedMap[key] = false
      }, 2000)
    }
  }

  const mainSkillUrl = getSkillRawUrl('9router')
  const promptInstruction = `Read this skill and use it: ${mainSkillUrl}`
</script>

<div class="max-w-4xl mx-auto space-y-6">
  <!-- Top instruction banner -->
  <div class="p-5 rounded-[14px] bg-surface border border-border-subtle shadow-[var(--shadow-soft)]">
    <div class="text-xs font-medium text-text-muted mb-2">Paste this to your AI:</div>
    <div
      class="px-3.5 py-2.5 rounded-lg bg-surface-2 border border-border-subtle font-mono text-[12px] text-text-main flex items-center justify-between gap-3 flex-wrap sm:flex-nowrap"
    >
      <span class="truncate">{promptInstruction}</span>
      <button
        type="button"
        onclick={() => handleCopy('main', promptInstruction)}
        class="px-2.5 py-1.5 rounded-md bg-brand-500 hover:bg-brand-600 text-white text-[11px] font-medium transition-colors cursor-pointer shrink-0 inline-flex items-center gap-1.5"
        title={promptInstruction}
      >
        <span class="material-symbols-outlined text-[14px]">
          {copiedMap['main'] ? 'check' : 'content_copy'}
        </span>
        <span>{copiedMap['main'] ? 'Copied!' : 'Copy prompt'}</span>
      </button>
    </div>
  </div>

  <!-- List of skill cards -->
  <div class="space-y-3">
    {#each skills as skill (skill.id)}
      {@const rawUrl = getSkillRawUrl(skill.id)}
      {@const isCopied = !!copiedMap[skill.id]}
      <div
        class="flex items-start gap-3.5 p-4 rounded-[14px] border shadow-[var(--shadow-soft)] transition-colors {skill.isEntry
          ? 'border-brand-500/40 bg-brand-500/5'
          : 'border-border-subtle bg-surface hover:bg-surface-2'}"
      >
        <!-- Icon box -->
        <div
          class="size-10 rounded-xl flex items-center justify-center shrink-0 {skill.isEntry
            ? 'bg-brand-500 text-white shadow-[var(--shadow-warm)]'
            : 'bg-brand-500/10 text-brand-600 dark:text-brand-400'}"
        >
          <span class="material-symbols-outlined text-[20px]">{skill.icon}</span>
        </div>

        <!-- Details -->
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2 flex-wrap">
            <h3 class="font-semibold text-sm text-text-main">{skill.name}</h3>

            {#if skill.isEntry}
              <span
                class="px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider bg-brand-500/15 text-brand-600 dark:text-brand-400 border border-brand-500/30"
              >
                START HERE
              </span>
            {/if}

            {#if skill.endpoint}
              <code
                class="px-2 py-0.5 rounded text-[11px] font-mono bg-surface-3 text-text-muted border border-border-subtle"
              >
                {skill.endpoint}
              </code>
            {/if}
          </div>

          <p class="text-xs text-text-muted mt-1 leading-relaxed">{skill.description}</p>

          <a
            href={getSkillBlobUrl(skill.id)}
            target="_blank"
            rel="noreferrer"
            class="text-[11px] text-text-muted hover:text-brand-500 mt-2 inline-flex items-center gap-1 break-all transition-colors"
          >
            <span>{rawUrl}</span>
            <span class="material-symbols-outlined text-[12px]">open_in_new</span>
          </a>
        </div>

        <!-- Copy button -->
        <button
          type="button"
          onclick={() => handleCopy(skill.id, rawUrl)}
          class="px-2.5 py-1.5 rounded-md bg-brand-500 hover:bg-brand-600 text-white text-[11px] font-medium transition-colors cursor-pointer shrink-0 inline-flex items-center gap-1.5"
          title={rawUrl}
        >
          <span class="material-symbols-outlined text-[13px]">
            {isCopied ? 'check' : 'content_copy'}
          </span>
          <span>{isCopied ? 'Copied!' : 'Copy link'}</span>
        </button>
      </div>
    {/each}
  </div>

  <!-- Bottom card: More on GitHub -->
  <div class="p-5 rounded-[14px] bg-surface border border-border-subtle shadow-[var(--shadow-soft)]">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <div>
        <h2 class="text-sm font-semibold text-text-main">More on GitHub</h2>
        <p class="text-xs text-text-muted mt-0.5">Browse source, README, and examples.</p>
      </div>

      <a
        href={`${githubRepoUrl}/tree/${branch}/${baseFolder}`}
        target="_blank"
        rel="noreferrer"
        class="text-sm font-medium text-brand-500 hover:text-brand-600 hover:underline inline-flex items-center gap-1.5 transition-colors"
      >
        <span>View on GitHub</span>
        <span class="material-symbols-outlined text-[16px]">open_in_new</span>
      </a>
    </div>
  </div>
</div>
