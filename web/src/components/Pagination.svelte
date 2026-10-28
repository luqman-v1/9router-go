<script lang="ts">
  // Pagination for a paged list: the page label on the left, the prev/next
  // pair on the right. Both lists on the jailbreak page used to carry this
  // markup twice, so a change to the button treatment had to be made twice.
  interface Props {
    page: number
    pageCount: number
    total: number
    noun?: string
    onPage: (page: number) => void
  }
  let { page, pageCount, total, noun = 'preset', onPage }: Props = $props()
</script>

<div class="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-border-subtle">
  <span class="text-[11px] text-text-subtle">
    Page {page} of {pageCount} · {total} {noun}{total === 1 ? '' : 's'}
  </span>
  <div class="flex items-center gap-2">
    <button
      type="button"
      onclick={() => onPage(Math.max(1, page - 1))}
      disabled={page <= 1}
      aria-label="Previous page"
      class="px-3 py-1.5 rounded-[8px] bg-surface-3 border border-border-subtle text-text-main text-sm font-medium disabled:opacity-40 hover:bg-brand-500 hover:text-white transition-colors"
    >
      ‹
    </button>
    <button
      type="button"
      onclick={() => onPage(Math.min(pageCount, page + 1))}
      disabled={page >= pageCount}
      aria-label="Next page"
      class="px-3 py-1.5 rounded-[8px] bg-surface-3 border border-border-subtle text-text-main text-sm font-medium disabled:opacity-40 hover:bg-brand-500 hover:text-white transition-colors"
    >
      ›
    </button>
  </div>
</div>
