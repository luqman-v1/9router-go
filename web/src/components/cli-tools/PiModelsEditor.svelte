<script lang="ts">
  interface Props {
    installed?: boolean
  }

  let { installed = false }: Props = $props()
</script>

<div class="text-xs text-text-muted">
  Pi models editor (installed: {installed ? 'yes' : 'no'})
</div>
