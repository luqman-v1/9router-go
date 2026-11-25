<script lang="ts">
  import { api, type SystemVersionInfo } from '../api/client'
  import { marked } from 'marked'
  import { copyToClipboard } from '../lib/clipboard'
  import { pushEscapeHandler } from '../lib/ui/escapeStack'

  let {
    isOpen = $bindable(false),
    updateInfo = null,
    currentVersion = '',
  }: {
    isOpen?: boolean
    updateInfo?: SystemVersionInfo | null
    currentVersion?: string
  } = $props()

  let isUpdating = $state(false)
  let updateStatus = $state<'idle' | 'updating' | 'success' | 'error'>('idle')
  let updateMsg = $state('')
  let copied = $state(false)
  let shutdownCountdown = $state(0)
  let shutdownPending = $state(false)
  let isDisconnected = $state(false)
  let releaseNotesHtml = $state('')
  let isLoadingChangelog = $state(false)
  let notesForKey = $state<string | null>(null)
  let shutdownTimer: ReturnType<typeof setInterval> | null = null
  // Bumped on every dismissal so a late continuation can tell its
  // operation was cancelled and must not act.
  let shutdownToken = 0

  const INSTALL_CMD = '9router-go update'

  marked.setOptions({ gfm: true, breaks: true })

  // marked.parse is synchronous unless `async` is set, so it returns a string
  // and must not be chained with .then().
  function toHtml(md: string): string {
    return marked.parse(md, { async: false }) as string
  }

  $effect(() => {
    const key = updateInfo?.latestVersion ?? ''
    if (!isOpen || notesForKey === key) return
    notesForKey = key
    releaseNotesHtml = ''
    isLoadingChangelog = false
    if (updateInfo?.releaseNotes) {
      releaseNotesHtml = toHtml(updateInfo.releaseNotes)
      return
    }
    isLoadingChangelog = true
    api
      .getChangelog()
      .then((md) => {
        // Ignore a response that arrived after the version moved on.
        if (md && notesForKey === key) releaseNotesHtml = toHtml(md)
      })
      .catch(() => {})
      .finally(() => {
        // Only the request that still owns the key may clear the spinner.
        if (notesForKey === key) isLoadingChangelog = false
      })
  })

  function clearShutdownTimer() {
    if (shutdownTimer !== null) {
      clearInterval(shutdownTimer)
      shutdownTimer = null
    }
    shutdownCountdown = 0
  }

  // Every dismiss path goes through here, so an in-flight update is never
  // abandoned and a pending shutdown countdown never outlives the modal.
  function dismiss() {
    if (isUpdating) return
    // Invalidate anything still awaiting, so a late clipboard result cannot
    // start a countdown behind a closed modal.
    shutdownToken++
    shutdownPending = false
    clearShutdownTimer()
    updateStatus = 'idle'
    updateMsg = ''
    isOpen = false
  }

  // Close on Escape key & lock scroll. The stack keeps Escape on the topmost
  // dialog, so this one does not close over a dialog opened above it.
  $effect(() => {
    if (!isOpen) return
    const releaseEscape = pushEscapeHandler(dismiss)
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      releaseEscape()
      document.body.style.overflow = prevOverflow
      clearShutdownTimer()
    }
  })

  async function copyInstallCmd(): Promise<boolean> {
    const ok = await copyToClipboard(INSTALL_CMD)
    if (!ok) {
      return false
    }
    copied = true
    setTimeout(() => {
      copied = false
    }, 2000)
    return true
  }

  async function shutdownServer() {
    try {
      await api.shutdownServer()
      isDisconnected = true
    } catch (err: any) {
      updateStatus = 'error'
      updateMsg = err?.message || 'Could not stop the server. Restart it manually.'
    }
  }

  async function handleAutoUpdate() {
    // An auto update and a manual shutdown must never both be in flight:
    // cancel the countdown and invalidate a clipboard write still in the air.
    clearShutdownTimer()
    shutdownToken++
    shutdownPending = false
    isUpdating = true
    updateStatus = 'updating'
    updateMsg = 'Downloading and applying binary update...'
    try {
      const res = await api.triggerUpdate()
      updateStatus = 'success'
      updateMsg = res?.message || 'Update installed successfully. Process is restarting...'
      setTimeout(() => {
        globalThis.location.reload()
      }, 3500)
    } catch (err: any) {
      updateStatus = 'error'
      updateMsg = err?.message || 'Auto update failed. Please run update command manually.'
      isUpdating = false
    }
  }

  async function handleCopyAndShutdown() {
    if (shutdownPending || shutdownTimer !== null) return
    const token = ++shutdownToken
    shutdownPending = true
    const didCopy = await copyInstallCmd()
    // Dismissed (or superseded) while the clipboard call was in flight.
    if (token !== shutdownToken) return
    shutdownPending = false
    if (!didCopy) {
      updateStatus = 'error'
      updateMsg = `Could not copy "${INSTALL_CMD}". Copy it manually before stopping the server.`
      return
    }
    let remaining = 5
    shutdownCountdown = remaining
    shutdownTimer = setInterval(() => {
      remaining -= 1
      shutdownCountdown = remaining
      if (remaining <= 0) {
        clearShutdownTimer()
        shutdownServer()
      }
    }, 1000)
  }
</script>

{#if isOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-sm"
      onclick={dismiss}
      onkeydown={(e) => e.key === 'Escape' && dismiss()}
      role="button"
      tabindex="-1"
      aria-label="Close background"
    ></div>

    <div
      class="relative w-full max-w-2xl bg-surface border border-border-subtle rounded-2xl shadow-2xl p-6 flex flex-col gap-4 z-10 animate-in fade-in zoom-in-95 max-h-[90vh] overflow-y-auto custom-scrollbar"
    >
      <div class="flex items-center justify-between pb-3 border-b border-border-subtle">
        <div class="flex items-center gap-2.5">
          <div class="size-9 rounded-full flex items-center justify-center bg-amber-500/10 text-amber-500">
            <span class="material-symbols-outlined text-[20px]">upgrade</span>
          </div>
          <div>
            <h2 class="text-base font-semibold text-text-main">
              Update 9router-go{updateInfo?.latestVersion ? ` to v${updateInfo.latestVersion}` : ''}
            </h2>
            <p class="text-xs text-text-muted">
              Current version: v{currentVersion || '1.9.1'}
            </p>
          </div>
        </div>
        <button
          type="button"
          onclick={dismiss}
          class="p-1 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
          aria-label="Close"
        >
          <span class="material-symbols-outlined text-[20px]">close</span>
        </button>
      </div>

      {#if releaseNotesHtml || updateInfo?.releaseNotes || isLoadingChangelog}
        <div class="flex flex-col gap-1.5">
          <span class="text-xs font-medium text-text-muted">Changes & Release Notes</span>
          <div class="p-4 rounded-xl bg-surface-2 border border-border-subtle text-xs text-text-main leading-relaxed max-h-60 overflow-y-auto custom-scrollbar changelog-body">
            {#if isLoadingChangelog}
              <div class="flex items-center justify-center py-6 text-text-muted gap-2">
                <span class="material-symbols-outlined animate-spin text-[20px] text-primary">progress_activity</span>
                <span>Loading changes...</span>
              </div>
            {:else if releaseNotesHtml}
              <!-- eslint-disable-next-line svelte/no-at-html-tags -->
              {@html releaseNotesHtml}
            {:else if updateInfo?.releaseNotes}
              <p class="whitespace-pre-line">{updateInfo.releaseNotes}</p>
            {/if}
          </div>
        </div>
      {/if}

      <!-- Terminal Command Box -->
      <div class="flex flex-col gap-1.5">
        <span class="text-xs font-medium text-text-muted">Terminal Command</span>
        <div class="flex items-center gap-2 p-2.5 rounded-xl bg-surface-2 border border-border-subtle">
          <code class="text-xs font-mono text-amber-600 dark:text-amber-400 flex-1 truncate select-all">
            {INSTALL_CMD}
          </code>
          <button
            type="button"
            onclick={copyInstallCmd}
            class="px-2.5 py-1 text-xs rounded-lg bg-surface hover:bg-surface-3 border border-border-subtle text-text-main transition-colors cursor-pointer shrink-0 flex items-center gap-1"
          >
            <span class="material-symbols-outlined text-[14px]">content_copy</span>
            <span>{copied ? 'Copied!' : 'Copy'}</span>
          </button>
        </div>
      </div>

      {#if updateStatus === 'updating'}
        <div class="p-3 rounded-xl bg-blue-500/10 border border-blue-500/20 text-blue-600 dark:text-blue-400 flex items-center gap-2 text-xs">
          <span class="material-symbols-outlined animate-spin text-[18px]">progress_activity</span>
          <span>{updateMsg}</span>
        </div>
      {:else if updateStatus === 'success'}
        <div class="p-3 rounded-xl bg-green-500/10 border border-green-500/20 text-green-600 dark:text-green-400 flex items-center gap-2 text-xs">
          <span class="material-symbols-outlined text-[18px]">check_circle</span>
          <span>{updateMsg}</span>
        </div>
      {:else if updateStatus === 'error'}
        <div class="p-3 rounded-xl bg-red-500/10 border border-red-500/20 text-red-600 dark:text-red-400 flex items-center gap-2 text-xs">
          <span class="material-symbols-outlined text-[18px]">error</span>
          <span>{updateMsg}</span>
        </div>
      {/if}

      <!-- Action buttons -->
      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border-subtle">
        <button
          type="button"
          onclick={dismiss}
          disabled={isUpdating}
          class="px-3.5 py-2 text-xs font-medium rounded-lg text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={handleCopyAndShutdown}
          disabled={isUpdating || shutdownCountdown > 0}
          class="px-3.5 py-2 text-xs font-medium rounded-lg border border-border-subtle text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
        >
          {shutdownCountdown > 0 ? `Stopping in ${shutdownCountdown}s...` : 'Copy & Shutdown'}
        </button>
        <button
          type="button"
          onclick={handleAutoUpdate}
          disabled={isUpdating}
          class="px-4 py-2 text-xs font-semibold rounded-lg bg-primary hover:bg-primary-hover text-white transition-colors cursor-pointer flex items-center gap-1.5 shadow-sm"
        >
          <span class="material-symbols-outlined text-[16px]">autorenew</span>
          <span>{isUpdating ? 'Updating...' : 'Auto Update'}</span>
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Disconnected Overlay -->
{#if isDisconnected}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm p-6">
    <div class="text-center p-8 bg-surface border border-border-subtle rounded-2xl shadow-2xl max-w-sm w-full animate-in fade-in">
      <div class="flex items-center justify-center size-14 rounded-full bg-red-500/20 text-red-500 mx-auto mb-4">
        <span class="material-symbols-outlined text-[28px]">power_off</span>
      </div>
      <h2 class="text-lg font-semibold text-text-main mb-1">Server Stopped</h2>
      <p class="text-xs text-text-muted mb-4">
        Now run <code class="px-1.5 py-0.5 rounded bg-surface-2 font-mono text-amber-500">9router-go update</code> in your terminal.
      </p>
      <button
        type="button"
        onclick={() => globalThis.location.reload()}
        class="w-full py-2 px-4 rounded-lg bg-primary hover:bg-primary-hover text-white text-xs font-semibold transition-colors cursor-pointer"
      >
        Reload Dashboard
      </button>
    </div>
  </div>
{/if}
