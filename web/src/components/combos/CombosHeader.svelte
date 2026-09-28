<script lang="ts">
  import Button from '../../lib/ui/Button.svelte'

  interface Props {
    onCreateClick: () => void
    onAutoFreeClick?: () => void
    isBuildingAutoFree?: boolean
  }

  let {
    onCreateClick,
    onAutoFreeClick,
    isBuildingAutoFree = false,
  }: Props = $props()
</script>

<div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
  <div class="min-w-0">
    <p class="text-sm text-text-muted mt-1">
      Group models under one name, then pick a strategy per combo:
    </p>
    <ul class="text-sm text-text-muted mt-2 flex flex-col gap-1">
      <li>
        <span class="font-medium text-text-main">Fallback</span> — tries models in order (next on failure)
      </li>
      <li>
        <span class="font-medium text-text-main">Round Robin</span> — rotates models across requests to spread load
      </li>
      <li>
        <span class="font-medium text-text-main">Fusion</span> — queries all models in parallel, then a judge
        synthesizes one answer. Best quality, but costs the most: every request bills all panel models + the judge
        (N+1 calls)
      </li>
    </ul>
  </div>
  <div class="flex w-full flex-col gap-2 sm:w-auto sm:items-stretch">
    <Button icon="add" onclick={onCreateClick} class="w-full sm:w-auto whitespace-nowrap">
      Create Combo
    </Button>
    {#if onAutoFreeClick}
      <Button
        icon="auto_awesome"
        onclick={onAutoFreeClick}
        disabled={isBuildingAutoFree}
        variant="secondary"
        class="w-full sm:w-auto whitespace-nowrap"
      >
        {isBuildingAutoFree ? 'Building...' : 'Auto Free Tier'}
      </Button>
    {/if}
  </div>
</div>
