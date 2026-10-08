<script lang="ts">
  import { onMount } from 'svelte'
  import type { ActiveRequestItem } from './types'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'

  interface ProviderNodeItem {
    id: string
    name: string
    color?: string
    type?: string
    alias?: string
  }

  interface Props {
    providers?: ProviderNodeItem[]
    activeRequests?: ActiveRequestItem[]
    pulseProvider?: string
    lastProvider?: string
    errorProvider?: string
    onRefresh?: () => void
  }

  let {
    providers = [],
    activeRequests = [],
    pulseProvider = '',
    lastProvider = '',
    errorProvider = '',
    onRefresh
  }: Props = $props()

  // Upstream parity (#4615): an empty list renders an empty state, it does not
  // fall back to a hardcoded free-provider roster. Those nodes have no
  // connection behind them, and upstream #4615 exists precisely because
  // drawing them unconditionally made an idle map look busy — so a catalog
  // fallback here would re-introduce the very bug that PR removed, on the one
  // screen where nothing is actually in use.
  let displayProviders = $derived(providers)

  let activeProviderIds = $derived.by(() => {
    const set = new Set<string>()
    for (const r of activeRequests) {
      const p = r.provider?.toLowerCase().trim()
      if (p) set.add(p)
    }
    return set
  })

  let hasPulse = $derived(Boolean(pulseProvider))
  let activeCount = $derived(
    Math.max(activeRequests.reduce((sum, r) => sum + (r.count || 1), 0), hasPulse ? 1 : 0)
  )
  // Container sizing and fitView state
  let containerEl = $state<HTMLDivElement | null>(null)
  let containerWidth = $state(800)
  let containerHeight = $state(480)

  // Zoom & Pan transforms
  let zoom = $state(0.85)
  let panX = $state(0)
  let panY = $state(0)
  let isDragging = $state(false)
  let dragStart = { x: 0, y: 0 }

  // Track image load errors to fallback to stylish initials
  let imageErrors = $state<Record<string, boolean>>({})

  // Upstream exact node radius formula:
  // s = providers.length
  // a = Math.max(320, 204 * s / (2 * Math.PI)) // rx
  // i = Math.max(200, 0.55 * a)                 // ry
  let geometry = $derived.by(() => {
    const s = displayProviders.length
    const rx = Math.max(320, (204 * s) / (2 * Math.PI))
    const ry = Math.max(200, 0.55 * rx)

    const nodes = displayProviders.map((p, idx) => {
      const angle = -Math.PI / 2 + (2 * Math.PI * idx) / s
      const x = rx * Math.cos(angle)
      const y = ry * Math.sin(angle)

      const pid = p.id.toLowerCase().trim()
      const pname = (p.name || '').toLowerCase().trim()
      const palias = (p.alias || '').toLowerCase().trim()
      const isProviderMatch = (target: string) => {
        if (!target) return false
        const t = target.toLowerCase().trim()
        return t === pid || t === pname || (Boolean(palias) && t === palias)
      }

      const isActive =
        activeProviderIds.has(pid) ||
        activeProviderIds.has(pname) ||
        (Boolean(palias) && activeProviderIds.has(palias)) ||
        (hasPulse && isProviderMatch(pulseProvider))
      const isLast = !isActive && Boolean(lastProvider) && isProviderMatch(lastProvider)
      const isError = !isActive && Boolean(errorProvider) && isProviderMatch(errorProvider)
      // Edge handle coordinates based on angle
      let sourceX = 0
      let sourceY = 0
      let targetX = x
      let targetY = y
      let isVertical = false

      if (
        Math.abs(angle + Math.PI / 2) < Math.PI / 4 ||
        Math.abs(angle - (3 * Math.PI) / 2) < Math.PI / 4
      ) {
        // Top quadrant
        sourceX = 0
        sourceY = -22
        targetX = x
        targetY = y + 26
        isVertical = true
      } else if (Math.abs(angle - Math.PI / 2) < Math.PI / 4) {
        // Bottom quadrant
        sourceX = 0
        sourceY = 22
        targetX = x
        targetY = y - 26
        isVertical = true
      } else if (angle > -Math.PI / 2 && angle < Math.PI / 2) {
        // Right quadrant
        sourceX = 65
        sourceY = 0
        targetX = x - 75
        targetY = y
        isVertical = false
      } else {
        // Left quadrant
        sourceX = -65
        sourceY = 0
        targetX = x + 75
        targetY = y
        isVertical = false
      }

      const path = isVertical
        ? `M ${sourceX} ${sourceY} C ${sourceX} ${(sourceY + targetY) / 2}, ${targetX} ${(sourceY + targetY) / 2}, ${targetX} ${targetY}`
        : `M ${sourceX} ${sourceY} C ${(sourceX + targetX) / 2} ${sourceY}, ${(sourceX + targetX) / 2} ${targetY}, ${targetX} ${targetY}`

      const returnPath = isVertical
        ? `M ${targetX} ${targetY} C ${targetX} ${(sourceY + targetY) / 2}, ${sourceX} ${(sourceY + targetY) / 2}, ${sourceX} ${sourceY}`
        : `M ${targetX} ${targetY} C ${(sourceX + targetX) / 2} ${targetY}, ${(sourceX + targetX) / 2} ${sourceY}, ${sourceX} ${sourceY}`

      const textIcon = (p.name || p.id || '?').slice(0, 2).toUpperCase()

      return {
        ...p,
        x,
        y,
        isActive,
        isLast,
        isError,
        path,
        returnPath,
        textIcon,
        color: p.color || '#6b7280'
      }
    })

    return { rx, ry, nodes }
  })

  function fitView() {
    if (!containerWidth || !containerHeight) return
    const requiredWidth = 2 * geometry.rx + 220
    const requiredHeight = 2 * geometry.ry + 120
    const scaleX = (containerWidth - 32) / requiredWidth
    const scaleY = (containerHeight - 32) / requiredHeight
    zoom = Math.min(1.05, Math.max(0.15, Math.min(scaleX, scaleY)))
    panX = 0
    panY = 0
  }

  let prevNodeCount = 0
  $effect(() => {
    const count = geometry.nodes.length
    if (count !== prevNodeCount) {
      prevNodeCount = count
      fitView()
    }
  })
  function handlePointerDown(e: PointerEvent) {
    if ((e.target as HTMLElement).closest('button')) return
    isDragging = true
    dragStart = { x: e.clientX - panX, y: e.clientY - panY }
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  }

  function handlePointerMove(e: PointerEvent) {
    if (!isDragging) return
    panX = e.clientX - dragStart.x
    panY = e.clientY - dragStart.y
  }

  function handlePointerUp(e: PointerEvent) {
    if (isDragging) {
      isDragging = false
      try {
        ;(e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId)
      } catch {}
    }
  }

  function handleWheel(e: WheelEvent) {
    e.preventDefault()
    const zoomFactor = e.deltaY < 0 ? 1.08 : 0.92
    zoom = Math.min(2.2, Math.max(0.25, zoom * zoomFactor))
  }

  onMount(() => {
    if (!containerEl) return
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        containerWidth = entry.contentRect.width || 800
        containerHeight = entry.contentRect.height || 480
        fitView()
      }
    })
    observer.observe(containerEl)
    fitView()
    return () => observer.disconnect()
  })
</script>

<div
  bind:this={containerEl}
  role="region"
  aria-label="Provider topology map"
  class="h-[320px] w-full min-w-0 rounded-lg border border-border bg-bg-subtle/30 sm:h-[480px] relative overflow-hidden select-none cursor-grab active:cursor-grabbing"
  onpointerdown={handlePointerDown}
  onpointermove={handlePointerMove}
  onpointerup={handlePointerUp}
  onpointercancel={handlePointerUp}
  onwheel={handleWheel}
>
  <!-- Interactive Viewport Layer (centered at 0, 0) -->
  <div
    class="absolute inset-0 pointer-events-none"
    style="transform: translate({containerWidth / 2 + panX}px, {containerHeight / 2 + panY}px) scale({zoom}); transform-origin: 0 0;"
  >
    <!-- SVG Layer for Bezier Edges -->
    <svg class="absolute inset-0 overflow-visible pointer-events-none" style="transform: translate(0, 0);">
      <defs>
        <filter id="topo-electric" filterUnits="userSpaceOnUse" x="-5000" y="-5000" width="10000" height="10000">
          <feTurbulence type="fractalNoise" baseFrequency="0.9" numOctaves="1" seed="2" result="noise">
            <animate attributeName="baseFrequency" values="0.8;1.3;0.8" dur="0.25s" repeatCount="indefinite" />
          </feTurbulence>
          <feDisplacementMap in="SourceGraphic" in2="noise" scale="3" xChannelSelector="R" yChannelSelector="G" />
        </filter>
      </defs>

      <!-- Edges connecting router to provider nodes -->
      {#each geometry.nodes as node (node.id)}
        {#if node.isActive}
          <!-- Expanding Shockwave Ripples from Provider Target -->
          <g class="topology-shockwave-group">
            <circle
              cx={node.x}
              cy={node.y}
              r="35"
              fill="none"
              stroke={node.color || '#22d3ee'}
              opacity="0"
              style="filter: drop-shadow(0 0 10px {node.color || '#22d3ee'});"
            >
              <animate attributeName="r" values="32;105" dur="1.1s" repeatCount="indefinite" />
              <animate attributeName="opacity" values="0.85;0" dur="1.1s" repeatCount="indefinite" />
              <animate attributeName="stroke-width" values="3.5;0.5" dur="1.1s" repeatCount="indefinite" />
            </circle>
            <circle
              cx={node.x}
              cy={node.y}
              r="35"
              fill="none"
              stroke="#38bdf8"
              opacity="0"
              style="filter: drop-shadow(0 0 8px #38bdf8);"
            >
              <animate attributeName="r" values="32;105" dur="1.1s" begin="0.55s" repeatCount="indefinite" />
              <animate attributeName="opacity" values="0.85;0" dur="1.1s" begin="0.55s" repeatCount="indefinite" />
              <animate attributeName="stroke-width" values="3;0.5" dur="1.1s" begin="0.55s" repeatCount="indefinite" />
            </circle>
          </g>

          <!-- Bidirectional Neural Stream (Router <-> Provider) -->
          <g class="topology-edge-electric">
            <!-- 1. Outgoing Prompt Stream: Outer electric halo (Router -> Provider) -->
            <path
              d={node.path}
              fill="none"
              stroke="#22d3ee"
              stroke-width="10"
              stroke-opacity="0.32"
              stroke-linecap="round"
              filter="url(#topo-electric)"
              class="topology-edge-halo"
            />
            <!-- 2. Mid plasma forward stream -->
            <path
              d={node.path}
              fill="none"
              stroke="#06b6d4"
              stroke-width="4.5"
              stroke-opacity="0.8"
              stroke-linecap="round"
              filter="url(#topo-electric)"
              class="topology-edge-plasma"
            />
            <!-- 3. Hot core laser forward beam -->
            <path
              d={node.path}
              fill="none"
              stroke="#f8fafc"
              stroke-width="2"
              class="topology-edge-kame"
            />

            <!-- 4. Incoming Token Stream: Reverse emerald/gold plasma (Provider -> Router) -->
            <path
              d={node.returnPath}
              fill="none"
              stroke="#10b981"
              stroke-width="3"
              stroke-opacity="0.9"
              class="topology-edge-return"
              style="filter: drop-shadow(0 0 6px #34d399);"
            />
            <path
              d={node.returnPath}
              fill="none"
              stroke="#fef08a"
              stroke-width="1.4"
              class="topology-edge-return"
            />

            <!-- Forward Prompt Tokens: Router -> Provider -->
            {#each [0, 1, 2] as i}
              <circle
                r={i === 0 ? 4.5 : 3.2}
                fill={i === 0 ? '#38bdf8' : i === 1 ? '#e0f2fe' : '#67e8f9'}
                opacity="0.95"
                style="filter: drop-shadow(0 0 6px #0ea5e9);"
              >
                <animateMotion
                  dur="{0.42 + i * 0.09}s"
                  repeatCount="indefinite"
                  path={node.path}
                  begin="{i * 0.12}s"
                />
              </circle>
            {/each}

            <!-- Reverse Output Tokens: Provider -> Router (Streamed Responses) -->
            {#each [0, 1, 2, 3] as i}
              <circle
                r={i % 2 === 0 ? 4.2 : 2.8}
                fill={i === 0 ? '#34d399' : i === 1 ? '#facc15' : i === 2 ? '#6ee7b7' : '#fde047'}
                opacity="0.95"
                style="filter: drop-shadow(0 0 6px #10b981);"
              >
                <animateMotion
                  dur="{0.48 + i * 0.1}s"
                  repeatCount="indefinite"
                  path={node.returnPath}
                  begin="{i * 0.11}s"
                />
              </circle>
            {/each}

            <!-- Electric Sparks -->
            {#each [0, 1, 2, 3] as i}
              <circle
                r="1.8"
                fill="#e0f2fe"
                opacity="0"
              >
                <animate
                  attributeName="opacity"
                  values="0;1;0;0;1;0"
                  dur="{0.35 + (i % 3) * 0.1}s"
                  begin="{i * 0.07}s"
                  repeatCount="indefinite"
                />
                <animateMotion
                  dur="{0.28 + i * 0.05}s"
                  repeatCount="indefinite"
                  path={node.path}
                  begin="{i * 0.11}s"
                />
              </circle>
            {/each}
          </g>
        {:else if node.isLast}
          <!-- Last Provider Active Edge (Amber) -->
          <path
            d={node.path}
            fill="none"
            stroke="#f59e0b"
            stroke-width="2"
            opacity="0.7"
          />
        {:else if node.isError}
          <!-- Error Edge (Red) -->
          <path
            d={node.path}
            fill="none"
            stroke="#ef4444"
            stroke-width="2.5"
            opacity="0.9"
          />
        {:else}
          <!-- Inactive Subtle Edge -->
          <path
            d={node.path}
            fill="none"
            stroke="var(--color-border)"
            stroke-width="1"
            opacity="0.3"
          />
        {/if}
      {/each}
      <!-- Center Router Absorption Rings when activeCount > 0 -->
      {#if activeCount > 0}
        <g class="topology-router-absorption">
          <circle
            cx="0"
            cy="0"
            r="30"
            fill="none"
            stroke="#22d3ee"
            stroke-dasharray="5 5"
            opacity="0"
            style="filter: drop-shadow(0 0 6px #22d3ee);"
          >
            <animate attributeName="r" values="55;20" dur="0.9s" repeatCount="indefinite" />
            <animate attributeName="opacity" values="0;0.8;0" dur="0.9s" repeatCount="indefinite" />
            <animate attributeName="stroke-width" values="1;2.5;0.5" dur="0.9s" repeatCount="indefinite" />
          </circle>
          <circle
            cx="0"
            cy="0"
            r="30"
            fill="none"
            stroke="#facc15"
            stroke-dasharray="4 6"
            opacity="0"
            style="filter: drop-shadow(0 0 6px #facc15);"
          >
            <animate attributeName="r" values="65;24" dur="1.1s" begin="0.45s" repeatCount="indefinite" />
            <animate attributeName="opacity" values="0;0.7;0" dur="1.1s" begin="0.45s" repeatCount="indefinite" />
            <animate attributeName="stroke-width" values="0.8;2;0.5" dur="1.1s" begin="0.45s" repeatCount="indefinite" />
          </circle>
        </g>
      {/if}
    </svg>

    <!-- HTML Router Node (Center 0, 0) -->
    <div
      class="absolute z-10 flex items-center justify-center px-5 py-3 rounded-xl border-2 min-w-[130px] {activeCount > 0 ? 'topology-router-core border-yellow-300 bg-gradient-to-br from-primary/30 via-yellow-400/20 to-cyan-400/25' : 'border-primary bg-primary/5 shadow-md'} pointer-events-auto"
      style="left: 0px; top: 0px; transform: translate(-50%, -50%);"
    >
      <img
        src="/favicon.svg"
        alt="9router-go"
        class="w-6 h-6 mr-2 object-contain {activeCount > 0 ? 'topology-router-icon' : ''}"
        loading="lazy"
        decoding="async"
      />
      <span class="text-sm font-bold {activeCount > 0 ? 'topology-router-label text-yellow-300' : 'text-primary'}">
        9router-go
      </span>
      {#if activeCount > 0}
        <span class="ml-2 px-1.5 py-0.5 rounded-full bg-yellow-400 text-black text-xs font-bold topology-router-badge">
          {activeCount}
        </span>
      {/if}
    </div>
    <!-- HTML Provider Nodes -->
    {#each geometry.nodes as node (node.id)}
      <div
        class="absolute flex items-center gap-2.5 px-4 py-2.5 rounded-lg border-2 transition-all duration-300 bg-bg shadow-sm pointer-events-auto {node.isActive ? 'topology-node-active-bounce' : ''}"
        style="left: {node.x}px; top: {node.y}px; transform: translate(-50%, -50%); border-color: {node.isActive ? node.color : node.isLast ? '#f59e0b' : 'var(--color-border)'}; box-shadow: {node.isActive ? `0 0 22px ${node.color}60, 0 0 10px rgba(34, 211, 238, 0.4)` : node.isLast ? '0 0 8px rgba(245, 158, 11, 0.25)' : 'none'}; min-width: 150px;"
      >
        <div
          class="w-8 h-8 rounded-md flex items-center justify-center shrink-0"
          style="background-color: {node.color}15;"
        >
          {#if !imageErrors[node.id]}
            <ProviderArtwork id={node.id} class="w-6 h-6 rounded-sm object-contain" />
          {:else}
            <span class="text-sm font-bold" style="color: {node.color};">
              {node.textIcon}
            </span>
          {/if}
        </div>
        <span
          class="text-base font-medium truncate max-w-[200px]"
          style="color: {node.isActive ? node.color : 'var(--color-text)'}"
          title={node.name}
        >
          {node.name}
        </span>

        <!-- Active indicator -->
        {#if node.isActive}
          <div class="flex items-center gap-1.5 ml-auto shrink-0">
            <span class="text-[10px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded bg-emerald-500/15 text-emerald-400 border border-emerald-500/30">
              Live
            </span>
            <span class="relative flex h-2 w-2">
              <span
                class="animate-ping absolute inline-flex h-full w-full rounded-full opacity-75"
                style="background-color: {node.color};"
              ></span>
              <span
                class="relative inline-flex rounded-full h-2 w-2"
                style="background-color: {node.color};"
              ></span>
            </span>
          </div>
        {/if}
      </div>
    {/each}
  </div>
  <!-- Upstream empty state: nothing has been used in the period, so there is
       nothing honest to draw. The router core and the zoom controls stay put so
       the card keeps its shape instead of collapsing. -->
  {#if displayProviders.length === 0}
    <div class="pointer-events-none absolute inset-0 z-10 flex items-center justify-center px-4 text-center text-sm text-text-muted">
      No providers connected
    </div>
  {/if}

  <!-- Bottom-left React Flow style controls -->
  <div class="absolute bottom-4 left-4 z-20 flex flex-col rounded-md border border-border bg-surface/90 shadow-md overflow-hidden backdrop-blur">
    <button
      type="button"
      onclick={() => (zoom = Math.min(2.5, zoom * 1.2))}
      class="p-1.5 text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors border-b border-border cursor-pointer flex items-center justify-center"
      title="Zoom In"
      aria-label="Zoom In"
    >
      <span class="material-symbols-outlined text-[16px]">add</span>
    </button>
    <button
      type="button"
      onclick={() => (zoom = Math.max(0.2, zoom / 1.2))}
      class="p-1.5 text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors border-b border-border cursor-pointer flex items-center justify-center"
      title="Zoom Out"
      aria-label="Zoom Out"
    >
      <span class="material-symbols-outlined text-[16px]">remove</span>
    </button>
    <button
      type="button"
      onclick={fitView}
      class="p-1.5 text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer flex items-center justify-center"
      title="Fit View"
      aria-label="Fit View"
    >
      <span class="material-symbols-outlined text-[16px]">crop_free</span>
    </button>
  </div>
</div>
