<script lang="ts">
  // Provider artwork, with a fallback that never costs a request.
  //
  // A provider can be in the catalog without shipping a PNG under
  // /providers/<id>.png. Every surface that asked for one anyway produced a
  // 404 per tile, and the console filled with them. A catalogue id, an alias
  // and a configured custom-endpoint node were all resolved the same way, so
  // the miss could come from any of them.
  //
  // This is ProviderIcon's job — the initials badge in the provider's brand
  // colour — promoted out of the connections folder and made event-driven, so a
  // tile that appears after mount (a combo edit, a lazy analytics row) swaps the
  // image instead of silently rendering an empty box. An <img> whose src never
  // changes cannot fire onerror for a src set later, which is why the previous
  // call sites hid the image instead.

  import { getIconPath, getProviderGlyph } from '../connections/types'
  interface Props {
    /** Provider id or alias; falls back to the glyph when absent. */
    id?: string | null
    /** apiType picks between the OpenAI chat and Responses glyphs. */
    apiType?: string
    /** Alt text; the provider's display name when omitted. */
    alt?: string
    class?: string
    /** Inline style forwarded to the image, for per-surface sizing. */
    style?: string
  }

  let { id, apiType, alt, class: klass = '', style }: Props = $props()

  const src = $derived(getIconPath(id, apiType))
  const glyph = $derived(getProviderGlyph(id))
  const label = $derived(alt ?? glyph.name)

  // Keyed on the source, so switching providers re-attempts the artwork instead
  // of inheriting the previous provider's failure.
  const failed = $state(new Map<string, boolean>())
  const showImage = $derived(!failed.get(src))

  function handleError() {
    failed.set(src, true)
  }
</script>

{#if showImage}
  <img
    {src}
    alt={label}
    class={klass}
    {style}
    loading="lazy"
    decoding="async"
    onerror={handleError}
  />
{:else}
  <span
    class={klass}
    style={style}
    role="img"
    aria-label={label}
    title={label}
  >{glyph.initials}</span>
{/if}