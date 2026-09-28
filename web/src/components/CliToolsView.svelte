<script lang="ts">
  import { onMount } from 'svelte'
  import {
    AlertCircle,
    Check,
    Code2,
    Copy,
    ExternalLink,
    FolderKanban,
    HelpCircle,
    Key,
    Layers,
    Loader2,
    RefreshCw,
    Search,
    Shield,
    Terminal,
    Wrench,
    X,
    Zap
  } from 'lucide-svelte'
  import Card from '../lib/ui/Card.svelte'
  import { api, type APIKey } from '../api/client'

  interface Props {
    apiKeys?: APIKey[]
    onRefresh?: () => void
  }

  let {
    apiKeys = [],
    onRefresh
  }: Props = $props()

  let statuses = $state<Record<string, { installed?: boolean; version?: string | null; has9Router?: boolean } | null>>({})
  let isLoading = $state(true)
  let searchQuery = $state('')
  let activeCategory = $state<'all' | 'cli' | 'ide' | 'mitm'>('all')
  let copiedSnippetId = $state<string | null>(null)
  // Modal target for the tool-detail dialog. d8aa5ec3 dropped this declaration
  // while leaving ~20 references behind, so the template threw
  // "ReferenceError: selectedTool is not defined" and the view never rendered
  // past the loading state. tsc does not typecheck .svelte files and the svelte
  // compiler does not scope-check templates, so nothing in CI noticed.
  let selectedTool = $state<ToolItem | null>(null)
  // SSR fallback uses the Go default port 20130; live origin wins on mount.
  let localOrigin = $state(typeof window !== 'undefined' ? window.location.origin : 'http://localhost:20130')
  onMount(() => {
    if (typeof window !== 'undefined') {
      localOrigin = window.location.origin
    }
    loadStatuses()
  })

  async function loadStatuses() {
    isLoading = true
    try {
      statuses = await api.getCliToolsStatuses()
    } finally {
      isLoading = false
    }
  }

  interface ToolItem {
    id: string
    name: string
    category: 'cli' | 'ide' | 'mitm'
    description: string
    color: string
    image?: string
    icon?: string
    configType: 'env' | 'settings' | 'guide' | 'mitm'
    envVars?: Record<string, string>
    instructions?: string[]
    defaultKey?: string
  }

  let toolsCatalog: ToolItem[] = $derived.by(() => [
    {
      id: 'claude',
      name: 'Claude Code',
      category: 'cli',
      image: '/providers/claude.png',
      description: 'Anthropic Claude Code CLI research & coding agent',
      color: '#D97757',
      configType: 'env',
      envVars: {
        ANTHROPIC_BASE_URL: `${localOrigin}`,
        ANTHROPIC_API_KEY: 'sk-9router-local-token',
      },
      instructions: [
        `Add to your shell profile (~/.zshrc or ~/.bashrc):`,
        `export ANTHROPIC_BASE_URL="${localOrigin}"`,
        `export ANTHROPIC_API_KEY="<your-api-key>"`,
        `Run 'claude' in any directory to start coding with 9router-go routing.`,
      ],
    },
    {
      id: 'cursor',
      name: 'Cursor IDE',
      category: 'ide',
      image: '/providers/cursor.png',
      description: 'AI-first code editor with OpenAI/Anthropic model support',
      color: '#3B82F6',
      configType: 'settings',
      instructions: [
        'Open Cursor Settings > Models.',
        `Under OpenAI API Key: enter your 9router-go API Key.`,
        `Click Override OpenAI Base URL and enter: ${localOrigin}/v1`,
        'Enable your favorite models in the Cursor model list.',
      ],
    },
    {
      id: 'cline',
      name: 'Cline',
      category: 'ide',
      image: '/providers/cline.png',
      description: 'Autonomous coding agent extension for VS Code',
      color: '#F97316',
      configType: 'settings',
      instructions: [
        'Open Cline extension settings in VS Code.',
        'Select API Provider: "OpenAI Compatible".',
        `Set Base URL: ${localOrigin}/v1`,
        'Set API Key: enter your 9router-go API Key.',
        'Set Model ID: choose any configured model or combo.',
      ],
    },
    {
      id: 'antigravity',
      name: 'Antigravity MITM',
      category: 'mitm',
      image: '/providers/antigravity.png',
      description: 'Google Antigravity IDE proxy & tool uncloaking',
      color: '#4285F4',
      configType: 'mitm',
      instructions: [
        'Antigravity MITM intercepts Google Cloud Code PA traffic transparently.',
        `Point HTTP_PROXY or system proxy to 9router-go on port 20130.`,
        'All tools with _ide suffixes will be seamlessly uncloaked and routed to configured connections.',
      ],
    },
    {
      id: 'kiro',
      name: 'Kiro MITM',
      category: 'mitm',
      image: '/providers/kiro.png',
      description: 'Kiro AI IDE transparent request interception',
      color: '#A855F7',
      configType: 'mitm',
      instructions: [
        'Kiro MITM captures telemetry and auth token exchanges.',
        `Ensure Kiro network routing directs through 9router-go gateway.`,
      ],
    },
    {
      id: 'codex',
      name: 'OpenAI Codex CLI',
      category: 'cli',
      image: '/providers/codex.png',
      description: 'Command line coding assistant and OpenAI client',
      color: '#10B981',
      configType: 'env',
      envVars: {
        OPENAI_BASE_URL: `${localOrigin}/v1`,
        OPENAI_API_KEY: 'sk-9router-local-token',
      },
      instructions: [
        `export OPENAI_BASE_URL="${localOrigin}/v1"`,
        `export OPENAI_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'copilot',
      name: 'GitHub Copilot',
      category: 'ide',
      image: '/providers/copilot.png',
      description: 'GitHub Copilot completions and chat forwarding',
      color: '#6366F1',
      configType: 'guide',
      instructions: [
        'Configurable via VS Code HTTP proxy settings.',
        `Set "http.proxy": "${localOrigin}" in VS Code settings.json.`,
      ],
    },
    {
      id: 'droid',
      name: 'Factory Droid',
      category: 'cli',
      image: '/providers/droid.png',
      description: 'Factory Droid automated software engineering CLI',
      color: '#EC4899',
      configType: 'env',
      envVars: {
        DROID_BASE_URL: `${localOrigin}/v1`,
        DROID_API_KEY: 'sk-9router-local-token',
      },
      instructions: [
        `export DROID_BASE_URL="${localOrigin}/v1"`,
        `export DROID_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'devin',
      name: 'Devin CLI',
      category: 'cli',
      image: '/providers/devin-cli.png',
      description: 'Devin autonomous software engineer terminal CLI',
      color: '#14B8A6',
      configType: 'env',
      envVars: {
        DEVIN_BASE_URL: `${localOrigin}/v1`,
        DEVIN_API_KEY: 'sk-9router-local-token',
      },
      instructions: [
        `export DEVIN_BASE_URL="${localOrigin}/v1"`,
        `export DEVIN_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'hermes',
      name: 'Hermes Agent',
      category: 'cli',
      image: '/providers/hermes.png',
      description: 'Hermes multi-step autonomous agent environment',
      color: '#F59E0B',
      configType: 'env',
      envVars: {
        HERMES_BASE_URL: `${localOrigin}/v1`,
        HERMES_API_KEY: 'sk-9router-local-token',
      },
      instructions: [
        `export HERMES_BASE_URL="${localOrigin}/v1"`,
        `export HERMES_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'openclaw',
      name: 'Open Claw',
      category: 'cli',
      image: '/providers/openclaw.png',
      description: 'Open-source web scraping and agentic tool caller',
      color: '#8B5CF6',
      configType: 'env',
      instructions: [
        `export OPENCLAW_ENDPOINT="${localOrigin}/v1"`,
        `export OPENCLAW_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'kilo',
      name: 'Kilo Code',
      category: 'cli',
      image: '/providers/kilocode.png',
      description: 'Lightweight terminal coding assistant',
      color: '#06B6D4',
      configType: 'env',
      instructions: [
        `export KILO_BASE_URL="${localOrigin}/v1"`,
        `export KILO_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'grok-build',
      name: 'Grok Build',
      category: 'cli',
      image: '/providers/grok-cli.png',
      description: 'xAI Grok build and reasoning terminal suite',
      color: '#E11D48',
      configType: 'env',
      instructions: [
        `export GROK_BASE_URL="${localOrigin}/v1"`,
        `export GROK_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'opencode',
      name: 'OpenCode',
      category: 'cli',
      image: '/providers/opencode.png',
      description: 'Open source AI code engine with native completions',
      color: '#22C55E',
      configType: 'env',
      instructions: [
        `export OPENCODE_BASE_URL="${localOrigin}/v1"`,
        `export OPENCODE_API_KEY="<your-api-key>"`,
      ],
    },
    {
      id: 'cowork',
      name: 'Claude Cowork',
      category: 'ide',
      image: '/providers/claude.png',
      description: 'Claude Desktop MCP server and multi-agent coordination',
      color: '#D97757',
      configType: 'settings',
      instructions: [
        'Add 9router-go MCP servers to your claude_desktop_config.json.',
        `Point MCP endpoints to ${localOrigin}/v1.`,
      ],
    },
    {
      id: 'roo',
      name: 'Roo Code',
      category: 'ide',
      image: '/providers/roo.png',
      description: 'Roo Code VS Code AI assistant',
      color: '#38BDF8',
      configType: 'settings',
      instructions: [
        'In Roo Code provider settings, select OpenAI Compatible.',
        `Base URL: ${localOrigin}/v1`,
        'API Key: enter your 9router-go key.',
      ],
    },
    {
      id: 'continue',
      name: 'Continue.dev',
      category: 'ide',
      image: '/providers/continue.png',
      description: 'Open-source autopilot extension for VS Code & JetBrains',
      color: '#F43F5E',
      configType: 'settings',
      instructions: [
        'In ~/.continue/config.json, configure models with:',
        `"apiBase": "${localOrigin}/v1"`,
        '"apiKey": "<your-api-key>"',
      ],
    },
    {
      id: 'amp',
      name: 'Amp CLI',
      category: 'cli',
      image: '/providers/amp.png',
      color: '#F97316',
      description: 'Sourcegraph Amp coding assistant CLI with 9router-go model aliases',
      configType: 'guide',
      instructions: [
        `export OPENAI_BASE_URL="${localOrigin}/v1"`,
        `export OPENAI_API_KEY="<your-api-key>"`,
        `amp --model "gemini-2.5-pro"`,
      ],
    },
    {
      id: 'qwen',
      name: 'Qwen Code',
      category: 'cli',
      image: '/providers/qwen.png',
      color: '#10B981',
      description: 'Alibaba Qwen Code CLI — supports OpenAI, Anthropic & Gemini providers',
      configType: 'guide',
      instructions: [
        `Configure ~/.qwen/settings.json:`,
        `Set "selectedType": "openai", "baseUrl": "${localOrigin}/v1", "apiKey": "<your-api-key>"`,
      ],
    },
    {
      id: 'deepseek-tui',
      name: 'DeepSeek TUI',
      category: 'cli',
      image: '/providers/deepseek-tui.png',
      color: '#4D6BFE',
      description: 'DeepSeek Terminal Coding Agent (Rust TUI)',
      configType: 'env',
      instructions: [
        `Configure ~/.deepseek/config.toml:`,
        `Set provider to "openai" mode with base_url = "${localOrigin}/v1"`,
      ],
    },
    {
      id: 'jcode',
      name: 'jcode',
      category: 'cli',
      image: '/providers/jcode.png',
      color: '#FF6B35',
      description: 'High-performance Rust-based coding agent harness',
      configType: 'env',
      instructions: [
        `export JCODE_9ROUTER_API_KEY="<your-api-key>"`,
        `Configure [[providers.9router]] with base_url = "${localOrigin}/v1"`,
      ],
    },
    {
      id: 'opendesign',
      name: 'OpenDesign',
      category: 'cli',
      image: '/providers/opendesign.png',
      color: '#7C3AED',
      description: 'OpenDesign — claude.ai/design open-sourced! Agent-native design skills pack',
      configType: 'guide',
      instructions: [
        `/plugin marketplace add manalkaff/opendesign`,
        `/plugin install opendesign@opendesign`,
        `Inherits host agent's model config via 9router-go.`,
      ],
    },
  ])
  let filteredTools = $derived(
    toolsCatalog.filter((tool) => {
      const matchCategory = activeCategory === 'all' || tool.category === activeCategory
      const matchSearch =
        !searchQuery.trim() ||
        tool.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
        tool.description.toLowerCase().includes(searchQuery.toLowerCase())
      return matchCategory && matchSearch
    })
  )

  let effectiveApiKey = $derived(apiKeys[0]?.key || 'sk-8b71f86e0a1f2fb5-nhz496-cfa1c800')

  function copyText(text: string, id: string) {
    navigator.clipboard.writeText(text)
    copiedSnippetId = id
    setTimeout(() => {
      if (copiedSnippetId === id) copiedSnippetId = null
    }, 2000)
  }

  function getToolStatus(toolId: string) {
    const s = statuses[toolId]
    if (!s) {
      return { label: 'Guide', cls: 'bg-blue-500/10 text-blue-600 dark:text-blue-400 border-blue-500/20' }
    }
    if (s.installed) {
      if (s.has9Router) {
        return { label: 'Connected', cls: 'bg-success/10 text-success border-success/20' }
      }
      return { label: 'Not configured', cls: 'bg-warning/10 text-warning border-warning/20' }
    }
    return { label: 'Not installed', cls: 'bg-surface-2 text-text-subtle border-border' }
  }
</script>

<div class="flex flex-col gap-6">
  <!-- PAGE HEADER & FILTERS -->
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
    <div class="space-y-1">
      <div class="flex items-center gap-2">
        <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
          <Terminal class="w-5 h-5" />
        </div>
        <div>
          <h1 class="font-headline text-2xl sm:text-3xl font-bold text-text-main tracking-tight flex items-center gap-2">
            CLI & IDE Tools
          </h1>
          <p class="font-body text-xs sm:text-sm text-text-muted">
            Configure Cursor, Claude Code, Cline, and terminal agents to connect to 9router-go
          </p>
        </div>
      </div>
    </div>

    <button
      type="button"
      onclick={loadStatuses}
      disabled={isLoading}
      class="flex items-center gap-1.5 px-3 py-2 rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-semibold text-xs border border-border transition cursor-pointer self-start sm:self-auto"
    >
      <RefreshCw class="w-3.5 h-3.5 {isLoading ? 'animate-spin text-brand-500' : 'text-text-muted'}" />
      <span>Scan Installed Tools</span>
    </button>
  </div>

  <!-- SEARCH & CATEGORY FILTER TABS -->
  <div class="flex flex-col sm:flex-row items-center justify-between gap-3">
    <!-- Category Tabs -->
    <div class="flex items-center gap-1.5 bg-surface-2 p-1 rounded-xl border border-border w-full sm:w-auto">
      <button
        type="button"
        onclick={() => (activeCategory = 'all')}
        class="flex-1 sm:flex-initial px-3 py-1.5 rounded-lg text-xs font-semibold transition cursor-pointer {activeCategory === 'all'
          ? 'bg-surface text-brand-500 shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        All Tools ({toolsCatalog.length})
      </button>
      <button
        type="button"
        onclick={() => (activeCategory = 'cli')}
        class="flex-1 sm:flex-initial px-3 py-1.5 rounded-lg text-xs font-semibold transition cursor-pointer {activeCategory === 'cli'
          ? 'bg-surface text-brand-500 shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        CLI Agents
      </button>
      <button
        type="button"
        onclick={() => (activeCategory = 'ide')}
        class="flex-1 sm:flex-initial px-3 py-1.5 rounded-lg text-xs font-semibold transition cursor-pointer {activeCategory === 'ide'
          ? 'bg-surface text-brand-500 shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        IDE & Editors
      </button>
      <button
        type="button"
        onclick={() => (activeCategory = 'mitm')}
        class="flex-1 sm:flex-initial px-3 py-1.5 rounded-lg text-xs font-semibold transition cursor-pointer {activeCategory === 'mitm'
          ? 'bg-surface text-brand-500 shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        MITM Proxies
      </button>
    </div>

    <!-- Search Input -->
    <div class="relative w-full sm:w-64">
      <Search class="w-4 h-4 text-text-muted absolute left-3 top-1/2 -translate-y-1/2" />
      <input
        type="text"
        bind:value={searchQuery}
        placeholder="Filter tools..."
        class="w-full pl-9 pr-3 py-1.5 rounded-xl bg-surface-2 border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
      />
    </div>
  </div>

  <!-- TOOLS GRID -->
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3.5">
    {#each filteredTools as tool (tool.id)}
      {@const status = getToolStatus(tool.id)}
      <div
        role="button"
        tabindex="0"
        onclick={() => (selectedTool = tool)}
        onkeydown={(e) => e.key === 'Enter' && (selectedTool = tool)}
        class="group p-4 rounded-xl bg-surface border border-border hover:border-brand-500/40 hover:shadow-lg transition cursor-pointer flex flex-col justify-between space-y-3"
      >
        <div class="space-y-2">
          <!-- Top Row: Icon, Name, Status Badge -->
          <div class="flex items-start justify-between gap-2">
            <div class="flex items-center gap-3">
              <div class="size-10 rounded-xl flex items-center justify-center shrink-0 overflow-hidden bg-surface-2 border border-border/50 p-1">
                {#if tool.image}
                  <img
                    src={tool.image}
                    alt={tool.name}
                    class="size-8 object-contain rounded-lg"
                    onerror={(e) => {
                      const img = e.currentTarget as HTMLElement
                      img.style.display = 'none'
                      const fallback = img.nextElementSibling as HTMLElement
                      if (fallback) fallback.style.display = 'flex'
                    }}
                    loading="lazy"
                    decoding="async"
                  />
                  <div
                    class="size-8 rounded-lg hidden items-center justify-center font-bold text-white text-xs shadow-sm"
                    style="background-color: {tool.color}"
                  >
                    {tool.name.slice(0, 2).toUpperCase()}
                  </div>
                {:else if tool.icon}
                  <span class="material-symbols-outlined text-[24px]" style="color: {tool.color}">
                    {tool.icon}
                  </span>
                {:else}
                  <div
                    class="size-8 rounded-lg flex items-center justify-center font-bold text-white text-xs shadow-sm"
                    style="background-color: {tool.color}"
                  >
                    {tool.name.slice(0, 2).toUpperCase()}
                  </div>
                {/if}
              </div>
              <div>
                <h3 class="font-bold text-sm text-text-main group-hover:text-brand-500 transition-colors">
                  {tool.name}
                </h3>
                <span class="text-[10px] font-mono text-text-subtle capitalize">
                  {tool.category} tool
                </span>
              </div>
            </div>

            <span class="text-[10px] font-semibold px-2 py-0.5 rounded border shrink-0 {status.cls}">
              {status.label}
            </span>
          </div>

          <p class="text-xs text-text-muted line-clamp-2 leading-relaxed">
            {tool.description}
          </p>
        </div>

        <div class="flex items-center justify-between pt-2 border-t border-border/40 text-xs">
          <span class="text-[11px] text-brand-500 font-semibold flex items-center gap-1">
            <span>Configure</span>
            <ExternalLink class="w-3 h-3" />
          </span>

          <span class="text-[10px] font-mono text-text-subtle">
            {tool.configType === 'env' ? 'Environment' : tool.configType === 'mitm' ? 'Transparent' : 'Settings UI'}
          </span>
        </div>
      </div>
    {/each}
  </div>
</div>

<!-- MODAL: Tool Configuration Detail -->
{#if selectedTool}
  {@const status = getToolStatus(selectedTool.id)}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-xl p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-5">
      <!-- Modal Header -->
      <div class="flex items-start justify-between pb-3 border-b border-border">
        <div class="flex items-center gap-3">
          <div class="size-12 rounded-xl flex items-center justify-center shrink-0 overflow-hidden bg-surface-2 border border-border/50 p-1.5">
            {#if selectedTool.image}
              <img
                src={selectedTool.image}
                alt={selectedTool.name}
                class="size-9 object-contain rounded-lg"
                onerror={(e) => {
                  const img = e.currentTarget as HTMLElement
                  img.style.display = 'none'
                  const fallback = img.nextElementSibling as HTMLElement
                  if (fallback) fallback.style.display = 'flex'
                }}
                loading="lazy"
                decoding="async"
              />
              <div
                class="size-9 rounded-lg hidden items-center justify-center font-bold text-white text-sm shadow-sm"
                style="background-color: {selectedTool.color}"
              >
                {selectedTool.name.slice(0, 2).toUpperCase()}
              </div>
            {:else if selectedTool.icon}
              <span class="material-symbols-outlined text-[28px]" style="color: {selectedTool.color}">
                {selectedTool.icon}
              </span>
            {:else}
              <div
                class="size-9 rounded-lg flex items-center justify-center font-bold text-white text-sm shadow-sm"
                style="background-color: {selectedTool.color}"
              >
                {selectedTool.name.slice(0, 2).toUpperCase()}
              </div>
            {/if}
          </div>
          <div>
            <h3 class="text-base font-bold text-text-main flex items-center gap-2">
              <span>{selectedTool.name}</span>
              <span class="text-[10px] font-semibold px-2 py-0.5 rounded border {status.cls}">
                {status.label}
              </span>
            </h3>
            <p class="text-xs text-text-muted">{selectedTool.description}</p>
          </div>
        </div>

        <button
          type="button"
          onclick={() => (selectedTool = null)}
          class="text-text-muted hover:text-text-main cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <!-- Quick Copy Snippet (if env variables available) -->
      {#if selectedTool.envVars}
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-xs font-bold text-text-main">One-Click Environment Setup</span>
            <button
              type="button"
              onclick={() => {
                const lines = Object.entries(selectedTool?.envVars || {})
                  .map(([k, v]) => `export ${k}="${k.includes('KEY') ? effectiveApiKey : v}"`)
                  .join('\n')
                copyText(lines, 'all-env')
              }}
              class="flex items-center gap-1 text-xs font-semibold text-brand-500 hover:opacity-80 cursor-pointer"
            >
              {#if copiedSnippetId === 'all-env'}
                <Check class="w-3.5 h-3.5 text-success" />
                <span>Copied!</span>
              {:else}
                <Copy class="w-3.5 h-3.5" />
                <span>Copy Export Commands</span>
              {/if}
            </button>
          </div>

          <div class="p-3 rounded-xl bg-bg border border-border font-mono text-xs text-text-main space-y-1.5 select-all">
            {#each Object.entries(selectedTool.envVars) as [key, val]}
              <div class="flex items-center justify-between gap-2">
                <span class="text-text-muted">export {key}="<span class="text-brand-400">{key.includes('KEY') ? effectiveApiKey : val}</span>"</span>
                <button
                  type="button"
                  onclick={() => copyText(`export ${key}="${key.includes('KEY') ? effectiveApiKey : val}"`, key)}
                  class="p-1 text-text-subtle hover:text-text-main cursor-pointer"
                  title="Copy single variable"
                >
                  {#if copiedSnippetId === key}
                    <Check class="w-3 h-3 text-success" />
                  {:else}
                    <Copy class="w-3 h-3" />
                  {/if}
                </button>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Step-by-Step Instructions -->
      {#if selectedTool.instructions}
        <div class="space-y-2">
          <span class="text-xs font-bold text-text-main">Setup Instructions</span>
          <div class="space-y-2 text-xs">
            {#each selectedTool.instructions as step, idx}
              <div class="flex items-start gap-2.5 p-2.5 rounded-lg bg-surface-2 border border-border/60">
                <span class="w-5 h-5 rounded-full bg-brand-500/10 text-brand-500 font-bold flex items-center justify-center text-[10px] shrink-0 mt-0.5">
                  {idx + 1}
                </span>
                <span class="text-text-muted leading-relaxed select-text flex-1">
                  {step}
                </span>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Footer -->
      <div class="flex items-center justify-between pt-3 border-t border-border">
        <div class="flex items-center gap-1.5 text-xs text-text-subtle font-mono">
          <Key class="w-3.5 h-3.5 text-brand-500" />
          <span>Active Token: {effectiveApiKey.slice(0, 10)}••••</span>
        </div>

        <button
          type="button"
          onclick={() => (selectedTool = null)}
          class="py-2 px-5 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer"
        >
          Done
        </button>
      </div>
    </div>
  </div>
{/if}
