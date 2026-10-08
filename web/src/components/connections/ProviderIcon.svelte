<script lang="ts">
  // Provider icon with a graceful fallback.
  //
  // The artwork itself is ProviderArtwork's job, so every surface shares one
  // fallback: a provider added to the catalog without shipping a PNG under
  // /providers/<id>.png degrades to an initials badge in the provider's brand
  // colour instead of a 404 plus an empty box.
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import { getProviderGlyph } from './types'

  interface Props {
    id?: string | null
    apiType?: string
    size?: 'sm' | 'md' | 'lg'
    class?: string
  }

  let { id, apiType, size = 'sm', class: klass = '' }: Props = $props()

  const glyph = $derived(getProviderGlyph(id))

  const box = $derived(
    size === 'lg' ? 'w-10 h-10' : size === 'md' ? 'w-8 h-8' : 'w-6 h-6'
  )
  const art = $derived(size === 'lg' ? 'w-6 h-6' : size === 'md' ? 'w-5 h-5' : 'w-4 h-4')
  const text = $derived(size === 'lg' ? 'text-sm' : size === 'md' ? 'text-[11px]' : 'text-[9px]')
</script>

<div
  class="{box} shrink-0 rounded-lg flex items-center justify-center bg-black/5 dark:bg-white/5 border border-border overflow-hidden {klass}"
  role="img"
  aria-label={glyph.name}
  title={glyph.name}
>
  <ProviderArtwork {id} {apiType} class="{art} {text} object-contain leading-none font-semibold" />
</div>