<script lang="ts">
  // Marks a model the gateway has confirmed is gone upstream.
  //
  // It renders nothing when the model carries no deprecation, so a caller can
  // drop it into any model row unconditionally instead of branching on the
  // state itself.
  import Badge from './Badge.svelte'
  import { deprecationFor } from '../modelDeprecations.svelte'

  interface Props {
    provider: string
    model: string
  }

  let { provider, model }: Props = $props()

  let dep = $derived(deprecationFor(provider, model))

  const label = $derived(dep?.status === 'gone' ? 'Deprecated' : 'Retired')

  // The tooltip carries what the operator needs to act: what upstream said,
  // and the replacement to edit the combo entry to. A badge that only says
  // "Deprecated" leaves the work of finding the successor to guesswork.
  const title = $derived.by(() => {
    if (!dep) return ''
    const parts: string[] = []
    if (dep.message) parts.push(dep.message)
    if (dep.successor) parts.push(`Use ${dep.successor} instead.`)
    if (dep.detectedAt) parts.push(`Detected ${new Date(dep.detectedAt).toLocaleString()}.`)
    return parts.join(' ')
  })
</script>

{#if dep}
  <span class="inline-flex items-center gap-1" title={title}>
    <Badge tone="warning" size="sm">
      <span class="material-symbols-outlined text-[13px] leading-none" aria-hidden="true">error</span>
      {label}
    </Badge>
  </span>
{/if}