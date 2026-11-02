<script lang="ts">
  // Import entry point. One modal for every share form: a 9router:// token, a
  // .9router.json file, or the original /add#… URL. The codec decides which is
  // which — the operator pastes whatever they were given.
  //
  // Nothing is written here: the parsed links are handed to
  // AddProviderFromLinkModal, which is where the confirm step lives. An import
  // is untrusted input, so the review-and-confirm surface must not be skipped.
  import Modal from '../../lib/ui/Modal.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import { parseShareInput, SHARE_SCHEME, type ProviderShareLink } from '../../lib/providerShare'

  interface Props {
    isOpen: boolean
    onClose: () => void
    /** Receives the parsed links so the caller can open the receiver modal. */
    onImport: (links: ProviderShareLink[]) => void
  }

  let { isOpen, onClose, onImport }: Props = $props()
  let pasted = $state('')
  let parseError = $state('')
  let fileError = $state('')
  let dragging = $state(false)
  let fileInput = $state<HTMLInputElement | null>(null)

  // Reset each time the modal opens, so a failed paste is not still sitting
  // there the next time the operator comes back to it.
  let seededFor = $state<string | null>(null)
  $effect(() => {
    if (!isOpen) return
    if (seededFor === 'open') return
    seededFor = 'open'
    pasted = ''
    parseError = ''
    fileError = ''
    dragging = false
  })

  function parse() {
    parseError = ''
    if (!pasted.trim()) {
      parseError = 'Paste a share link, a 9router:// token, or the contents of a .9router.json file.'
      return
    }
    let links: ProviderShareLink[] | null
    try {
      links = parseShareInput(pasted)
    } catch (err) {
      // A malformed share link is a loud failure, not an empty result: the
      // operator needs to know the text they hold is broken.
      parseError = err instanceof Error ? err.message : String(err)
      return
    }
    if (!links || links.length === 0) {
      parseError = `Not a share link. Expected ${SHARE_SCHEME}…, a /add#… URL, or a .9router.json body.`
      return
    }
    onImport(links)
  }

  async function readFile(file: File) {
    fileError = ''
    parseError = ''
    try {
      const text = await file.text()
      let links: ProviderShareLink[] | null
      try {
        links = parseShareInput(text)
      } catch (err) {
        fileError = err instanceof Error ? err.message : String(err)
        return
      }
      if (!links || links.length === 0) {
        fileError = `${file.name} is not a share file. Expected ${SHARE_SCHEME}…, a /add#… URL, or a .9router.json body.`
        return
      }
      onImport(links)
    } catch {
      fileError = `Could not read ${file.name}.`
    }
  }

  function onDrop(event: DragEvent) {
    event.preventDefault()
    dragging = false
    const file = event.dataTransfer?.files?.[0]
    if (file) void readFile(file)
  }

  const canParse = $derived(pasted.trim().length > 0)
</script>

<Modal {isOpen} {onClose} title="Import Provider" icon="download" size="lg">
  {#snippet children()}
    <div class="flex flex-col gap-4">
      <p class="text-sm text-text-muted">
        Paste a share link, or drop a <span class="font-mono">.9router.json</span> file. You will review every
        provider before anything is written.
      </p>

      <label class="flex flex-col gap-1.5">
        <span class="text-sm font-medium text-text-main">Share link or file contents</span>
        <textarea
          bind:value={pasted}
          rows={5}
          placeholder={'9router://…\n— or —\nhttps://gateway/add#prov=…'}
          class="w-full rounded-[10px] border border-border-subtle bg-surface-2 px-3 py-2 font-mono text-xs text-text-main placeholder:text-text-muted/70 focus:border-brand-500/40 focus:outline-none"
        ></textarea>
      </label>

      {#if parseError}
        <p class="rounded-lg border border-error/40 bg-error/10 p-2 text-xs text-text-main">{parseError}</p>
      {/if}
      {#if fileError}
        <p class="rounded-lg border border-error/40 bg-error/10 p-2 text-xs text-text-main">{fileError}</p>
      {/if}

      <div
        role="region"
        aria-label="Drop a share file"
        ondragover={(e) => { e.preventDefault(); dragging = true }}
        ondragleave={() => (dragging = false)}
        ondrop={onDrop}
        class="rounded-[10px] border-2 border-dashed p-4 text-center transition-colors {dragging ? 'border-brand-500 bg-brand-500/5' : 'border-border-subtle bg-surface-2/40'}"
      >
        <p class="text-xs text-text-muted">
          Drop a <span class="font-mono">.9router.json</span> file here, or
          <button type="button" onclick={() => fileInput?.click()} class="cursor-pointer text-brand-500 underline">
            browse
          </button>
        </p>
        <input
          bind:this={fileInput}
          type="file"
          accept=".json,.9router,application/json,text/plain"
          class="hidden"
          onchange={(e) => {
            const file = e.currentTarget.files?.[0]
            if (file) void readFile(file)
            e.currentTarget.value = ''
          }}
        />
      </div>

      <div class="rounded-lg border border-border-subtle bg-surface-2/40 p-3 text-xs text-text-muted">
        <p class="mb-1 font-medium text-text-main">Accepted forms</p>
        <ul class="flex list-disc flex-col gap-0.5 pl-4">
          <li><span class="font-mono">9router://&lt;base64url&gt;</span> — one token, best for chat</li>
          <li><span class="font-mono">/add#prov=…</span> — the original URL form</li>
          <li><span class="font-mono">.9router.json</span> — for many providers or a block of keys</li>
        </ul>
      </div>
    </div>
  {/snippet}

  {#snippet footer()}
    <div class="flex items-center justify-end gap-2">
      <Button variant="secondary" onclick={onClose}>Cancel</Button>
      <Button variant="primary" disabled={!canParse} onclick={parse}>Review providers</Button>
    </div>
  {/snippet}
</Modal>
