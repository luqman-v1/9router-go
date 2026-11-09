<script lang="ts">
  // Free-pi sign-in.
  //
  // This is deliberately not wired into the generic device-code flow. free-pi's
  // consent screen discloses that ad content receives excerpts of the recent
  // conversation AND that prompts, code, files and responses are saved as traces,
  // used to train models, and may be shared or sold with no opt-out. So the user
  // reads that exact text and ticks a box before any request is sent — and
  // declining leaves no trace upstream, because the backend refuses to fetch even
  // a device code until consent is asserted.
  //
  // The text is fetched from the API rather than duplicated here, so the wording
  // the user reads cannot drift from the version that was recorded.
  import { api, ApiError } from '../../api/client'

  interface Props {
    isOpen: boolean
    onClose: () => void
    onConnected: () => void
  }

  let { isOpen, onClose, onConnected }: Props = $props()

  type Phase = 'consent' | 'code' | 'done'

  let phase = $state<Phase>('consent')
  let consentText = $state('')
  let consentVersion = $state('')
  let accepted = $state(false)
  let loadingConsent = $state(false)
  let busy = $state(false)
  let error = $state('')

  let userCode = $state('')
  let verificationUri = $state('')
  // verification_uri_complete has the code pre-filled, so the user just clicks
  // Authorize instead of having to copy the code from one window to another.
  let verificationUriComplete = $state('')
  let sessionId = $state('')
  let pollTimer = $state<ReturnType<typeof setInterval> | null>(null)
  // Consecutive failed polls. Errors are surfaced, never swallowed: the previous
  // version ignored them, so a 410 (this sign-in attempt expired) or a 502 showed
  // up as an indefinite "Waiting for authorisation…" with no way out.
  let pollFailures = $state(0)

  $effect(() => {
    if (!isOpen) {
      stopPolling()
      return
    }
    // Open (or reopen) always starts at the gate: an accepted consent must be a
    // deliberate act in this session, not a remembered one.
    phase = 'consent'
    accepted = false
    error = ''
    userCode = ''
    verificationUri = ''
    sessionId = ''
    void loadConsent()
  })

  async function loadConsent() {
    loadingConsent = true
    error = ''
    try {
      const res = await api.freePiConsent()
      consentText = res.text
      consentVersion = res.version
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      loadingConsent = false
    }
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  function handleKeyDown(event: KeyboardEvent) {
    if (event.key === 'Escape') onClose()
  }

  async function start() {
    if (!accepted || busy) return
    busy = true
    error = ''
    try {
      const res = await api.freePiInitiate(consentVersion)
      sessionId = res.sessionId
      userCode = res.userCode
      verificationUri = res.verificationUri
      verificationUriComplete = res.verificationUriComplete ?? ''
      phase = 'code'
      // The complete URI has the code pre-filled, so the user just clicks
      // Authorize. Without it the popup opens a bare github.com/login/device
      // and the user has to copy the code — which is where the previous hang
      // lived: they re-used an old tab and authorized a dead code.
      const openUrl = verificationUriComplete || verificationUri
      if (typeof window !== 'undefined' && openUrl) {
        window.open(openUrl, '_blank')
      }
      stopPolling()
      const interval = res.interval > 0 ? res.interval : 5
      pollTimer = setInterval(() => void pollOnce(), interval * 1000)
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      busy = false
    }
  }

  async function pollOnce() {
    if (!sessionId) return
    try {
      const res = await api.freePiPoll(sessionId)
      pollFailures = 0
      if (res.status === 'connected') {
        stopPolling()
        phase = 'done'
        onConnected()
        return
      }
      // consent_required is terminal: polling again cannot fix a login the
      // server never recorded consent for.
      if (res.status === 'consent_required') {
        stopPolling()
        error = res.message || 'free-pi did not record consent for this login. Start again.'
        phase = 'consent'
        accepted = false
      }
      // 'pending' needs no action; the next tick retries.
    } catch (err) {
      pollFailures += 1
      const status = err instanceof ApiError ? err.status : 0
      const detail = err instanceof Error ? err.message : String(err)

      // 410 means this attempt is gone (expired, or the gateway restarted). No
      // amount of retrying brings it back, so say so and offer the only real fix.
      if (status === 410) {
        stopPolling()
        error = 'This sign-in attempt expired before it completed. Start again — the code is still valid only until it expires.'
        phase = 'consent'
        accepted = false
        return
      }

      // Anything else is retried a few times, but the message is shown straight
      // away so a persistent failure is visible instead of silent.
      error = detail
      if (pollFailures >= 3) {
        stopPolling()
      }
    }
  }

  function cancel() {
    stopPolling()
    onClose()
  }
</script>

{#if isOpen}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
    role="presentation"
    onclick={(e) => e.target === e.currentTarget && cancel()}
    onkeydown={handleKeyDown}
  >
    <div
      class="flex max-h-[85vh] w-full max-w-2xl flex-col rounded-2xl border border-border bg-surface p-5 shadow-xl"
      role="dialog"
      aria-modal="true"
      aria-labelledby="freepi-connect-title"
    >
      <div class="mb-4 flex items-start justify-between gap-4">
        <div>
          <h3 id="freepi-connect-title" class="text-sm font-semibold text-text-main">
            Connect Free Pi
          </h3>
          <p class="mt-0.5 text-xs text-text-muted">
            Sign in with GitHub. Free Pi pays for inference with ads, so it asks you to accept a
            few terms first.
          </p>
        </div>
        <button
          type="button"
          onclick={cancel}
          aria-label="Close"
          class="shrink-0 rounded-md p-1 text-text-muted hover:bg-surface-2 hover:text-text-main"
        >
          <span class="material-symbols-outlined text-[20px]" aria-hidden="true">close</span>
        </button>
      </div>

      {#if error}
        <div class="mb-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          {error}
        </div>
      {/if}

      {#if phase === 'consent'}
        {#if loadingConsent}
          <p class="py-6 text-center text-sm text-text-muted">Loading terms…</p>
        {:else}
          <!-- Rendered verbatim: a paraphrase would let the user agree to
               something narrower than what upstream records. -->
          <div class="mb-3 max-h-64 overflow-y-auto rounded-lg border border-border-subtle bg-surface-2 px-3 py-2">
            <pre class="whitespace-pre-wrap break-words font-sans text-[11px] leading-relaxed text-text-main">{consentText}</pre>
          </div>

          <label class="mb-4 flex items-start gap-2 text-xs text-text-main">
            <input
              type="checkbox"
              bind:checked={accepted}
              class="mt-0.5 h-4 w-4 shrink-0 rounded border-border"
            />
            <span>
              I have read and accept these terms (version {consentVersion}), including that my
              prompts, code and responses may be used to train models.
            </span>
          </label>

          <div class="flex items-center justify-end gap-2">
            <button
              type="button"
              onclick={cancel}
              class="rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-text-muted hover:bg-surface-2"
            >
              Decline
            </button>
            <button
              type="button"
              onclick={start}
              disabled={!accepted || busy}
              class="rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-white hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50"
            >
              {busy ? 'Starting…' : 'Accept and sign in'}
            </button>
          </div>
        {/if}
      {:else if phase === 'code'}
        <div class="space-y-3">
          <p class="text-xs text-text-muted">
            Enter this code on GitHub. This window updates by itself once you approve.
          </p>
          <div class="flex flex-wrap items-center gap-3 rounded-lg border border-border-subtle bg-surface-2 px-3 py-3">
            <code class="font-mono text-lg tracking-widest text-text-main">{userCode}</code>
            {#if verificationUri}
              <a
                href={verificationUri}
                target="_blank"
                rel="noreferrer"
                class="text-xs font-medium text-primary hover:underline"
              >
                {verificationUri}
              </a>
            {/if}
          </div>
          <p class="flex items-center gap-2 text-xs text-text-muted">
            <span class="material-symbols-outlined animate-spin text-[16px]" aria-hidden="true">progress_activity</span>
            Waiting for authorisation…
          </p>
        </div>
      {:else}
        <div class="space-y-3">
          <p class="flex items-center gap-2 text-sm font-medium text-text-main">
            <span class="material-symbols-outlined text-[18px] text-emerald-500" aria-hidden="true">check_circle</span>
            Connected
          </p>
          <p class="text-xs text-text-muted">
            The connection appears below. Free Pi allows a few concurrent sessions per account; the
            gateway uses exactly one, so adding a second connection for the same account is not
            needed.
          </p>
          <div class="flex justify-end">
            <button
              type="button"
              onclick={cancel}
              class="rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-white hover:bg-primary-hover"
            >
              Done
            </button>
          </div>
        </div>
      {/if}
    </div>
  </div>
{/if}
