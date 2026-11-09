<script lang="ts">
  // Port of decolua/9router src/shared/components/Toggle.js (switch)
  let {
    checked = false,
    disabled = false,
    label = 'Toggle',
    title = '',
    size = 'md',
    onChange,
    onchange
  }: {
    checked?: boolean
    disabled?: boolean
    label?: string
    title?: string
    size?: 'sm' | 'md'
    onChange?: (value: boolean) => void
    onchange?: (value: boolean) => void
  } = $props()
</script>

<button
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  {title}
  {disabled}
  onclick={() => {
    const fn = onChange || onchange
    fn?.(!checked)
  }}
  class="relative inline-flex shrink-0 {size === 'sm' ? 'h-4 w-7' : 'h-5 w-9'} items-center rounded-full transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed {checked
    ? 'bg-brand-500'
    : 'bg-surface-3'}"
>
  <span
    class="inline-block {size === 'sm' ? 'h-3 w-3' : 'h-3.5 w-3.5'} transform rounded-full bg-white shadow transition-transform {checked
      ? (size === 'sm' ? 'translate-x-[13px]' : 'translate-x-[18px]')
      : 'translate-x-[2px]'}"
  ></span>
</button>
