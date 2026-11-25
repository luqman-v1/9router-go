<script lang="ts">
  // Freebuff banner ad.
  //
  // Freebuff operates on an ad-funded model where free session allowances and
  // quota are funded by sponsors and advertising partners. This component
  // renders a creative served by the gateway, reporting the impression only
  // after the creative is rendered on screen.
  import { api } from '../../api/client'

  interface Props {
    connectionId: string
  }

  let { connectionId }: Props = $props()

  interface Ad {
    adId: string
    clickUrl?: string
    creative: {
      title?: string
      body?: string
      cta?: string
      imageUrl?: string
      sponsorName?: string
      brandColor?: string
    }
  }

  let ad = $state<Ad | null>(null)
  let reported = false
  let disposed = false

  $effect(() => {
    disposed = false
    reported = false
    void load(connectionId)
    return () => {
      disposed = true
    }
  })

  async function load(id: string) {
    if (!id) return
    try {
      const res = await api.freebuffAd(id)
      if (disposed) return
      ad = res.ad
    } catch {
      ad = null
    }
  }

  function reportOnPaint() {
    if (reported || disposed || !ad) return
    reported = true
    void api.freebuffImpression(connectionId, ad.adId).catch(() => {})
  }

  $effect(() => {
    if (!ad) return
    if (ad.creative.imageUrl) return
    if (typeof requestAnimationFrame !== 'function') {
      reportOnPaint()
      return
    }
    requestAnimationFrame(() => requestAnimationFrame(reportOnPaint))
  })
</script>

{#if ad}
  <div
    class="mb-4 rounded-[14px] border border-border-subtle bg-surface-2 px-3 py-2.5"
    aria-label="Sponsored by Freebuff Partner"
  >
    <div class="flex items-start gap-3">
      {#if ad.creative.imageUrl}
        <img
          src={ad.creative.imageUrl}
          alt=""
          class="h-10 w-10 shrink-0 rounded-lg object-contain bg-surface p-1"
          loading="eager"
          onload={reportOnPaint}
          onerror={reportOnPaint}
        />
      {/if}
      <div class="min-w-0 flex-1">
        <p class="mb-0.5 flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-wide text-text-subtle">
          <span class="material-symbols-outlined text-[12px]" aria-hidden="true">campaign</span>
          Sponsored {#if ad.creative.sponsorName}· {ad.creative.sponsorName}{/if}
        </p>
        {#if ad.creative.title}
          <p class="truncate text-xs font-semibold text-text-main">{ad.creative.title}</p>
        {/if}
        {#if ad.creative.body}
          <p class="line-clamp-2 text-[11px] text-text-muted">{ad.creative.body}</p>
        {/if}
      </div>
      {#if ad.creative.cta && ad.clickUrl}
        <a
          href={ad.clickUrl}
          target="_blank"
          rel="noreferrer sponsored"
          class="shrink-0 self-center rounded-lg border border-border px-2.5 py-1 text-[11px] font-medium text-text-main hover:bg-surface-3"
          style={ad.creative.brandColor ? `border-color: ${ad.creative.brandColor}` : undefined}
        >
          {ad.creative.cta}
        </a>
      {/if}
    </div>
  </div>
{/if}
