<script lang="ts">
  // Free Pi's banner ad.
  //
  // free-pi pays for inference with advertising, and upstream gates completions
  // on ad impression records. This component is the honest half of that bargain:
  // it renders a creative this gateway served, and reports the impression only
  // after the creative is actually painted. Nothing here fires on a timer, and
  // nothing reports an ad that was not displayed — a fabricated impression is a
  // false claim to the advertiser whose spend funds the inference.
  //
  // If the ad service has nothing (204) or fails, it renders nothing and reports
  // nothing. An ad is decoration; it must never make the page look broken.
  import { api } from '../../api/client'

  interface Props {
    connectionId: string
  }

  let { connectionId }: Props = $props()

  interface Ad {
    adId: string
    clickUrl?: string
    source?: string
    creative: { title?: string; body?: string; cta?: string; imageUrl?: string }
  }

  let ad = $state<Ad | null>(null)
  let reported = false
  let disposed = false

  $effect(() => {
    // Re-run when the connection changes, and reset the once-only report flag so
    // a second connection's ad can be reported too.
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
      const res = await api.freePiAd(id)
      if (disposed) return
      ad = res.ad
    } catch {
      // Silently absent: a broken ad service is not a broken dashboard.
      ad = null
    }
  }

  // reportOnPaint fires the impression once, after the creative is on screen.
  //
  // With an image, load/error are the real "it was shown" signal. Without one,
  // two animation frames guarantee the browser has painted the text.
  function reportOnPaint() {
    if (reported || disposed || !ad) return
    reported = true
    void api.freePiImpression(connectionId, ad.adId).catch(() => {
      // A refused or failed report is not the user's problem; the server already
      // refuses reports for creatives it did not serve.
    })
  }

  $effect(() => {
    if (!ad) return
    if (ad.creative.imageUrl) return // the image's own handlers decide
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
    aria-label="Sponsored"
  >
    <div class="flex items-start gap-3">
      {#if ad.creative.imageUrl}
        <img
          src={ad.creative.imageUrl}
          alt=""
          class="h-12 w-12 shrink-0 rounded-lg object-cover"
          loading="eager"
          onload={reportOnPaint}
          onerror={reportOnPaint}
        />
      {/if}
      <div class="min-w-0 flex-1">
        <p class="mb-0.5 flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-wide text-text-subtle">
          <span class="material-symbols-outlined text-[12px]" aria-hidden="true">campaign</span>
          Sponsored
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
        >
          {ad.creative.cta}
        </a>
      {/if}
    </div>
  </div>
{/if}
