<script lang="ts">
  // The single Edit Connection modal, shared by the Providers tab and the
  // Quota Tracker (issue #158). Both used to keep their own copy of the same
  // nine `edit*` variables and the same markup, so any rule added here had to
  // be written twice. This component owns the state, the probe and the save.
  //
  // Credentials and the blocked-key rule are shared with credential.ts
  // (issue #154).
  import { api, type ProviderConnection } from '../../api/client'
  import {
    credentialPlaceholder,
    credentialUpdate,
    probeReplacementKey,
    type CredentialCheck
  } from './credential'
  import {
    emailPrivacy,
    formatEmailLabel,
    submittedConnectionName
  } from '../../lib/privacy'

  export interface ConnectionUpdate {
    name?: string
    priority?: number
    apiKey?: string
    testStatus?: string
  }

  type TestStatus = 'ok' | 'error' | null

  interface Props {
    connection: ProviderConnection
    /** Dismiss without saving. Also what every dismiss control calls. */
    onClose: () => void
    /** Persist the accumulated payload, then let the caller refresh its list. */
    onSave: (payload: ConnectionUpdate) => Promise<void> | void
    /**
     * How a failed save reaches the user. Required on purpose: this prop was
     * optional, and the Quota Tracker omitted it, so a failed save silently
     * swallowed the error and left the user clicking Save on a modal that
     * refused to close. Making it required turns that into a type error at the
     * call site instead.
     */
    onSaveError: (message: string) => void
    /** Label for the Test Connection action. */
    testLabel?: string
    /** True to show a spinner glyph beside Test while it runs. */
    testSpinner?: boolean
  }

  let {
    connection,
    onClose,
    onSave,
    onSaveError,
    testLabel = 'Test Connection',
    testSpinner = true
  }: Props = $props()

  // Upstream Modal parity: Escape dismisses, backdrop click dismisses.
  $effect(() => {
    if (typeof window === 'undefined') return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  // Seeded masked when privacy masking is on, so screen sharing does not
  // reveal the account email in the form. `originalName` keeps the untouched
  // value from the row: a masked label is a rendering, not a rename, and must
  // never be written back as the connection's name. `seededName` is what the
  // field actually opened with — save compares against that rather than
  // re-deriving the mask from live store state, which would go stale the
  // moment masking is toggled elsewhere while this modal is open.
  let originalName = $state(connection.name || '')
  let seededName = $state(formatEmailLabel(connection.name || '', $emailPrivacy))
  let name = $state(seededName)
  let priority = $state<number>(connection.priority ?? 1)
  // The value the priority field was seeded with. Saving sends priority only
  // when the field actually changed: a NULL-priority row has no number of its
  // own, so seeding the input with 1 and always sending it turned a plain
  // rename into an assignment of rank 1, colliding with whichever row already
  // held it.
  let seededPriority = $state<number>(connection.priority ?? 1)
  let testStatus = $state<TestStatus>(null)
  let testError = $state<string | null>(null)
  let isTesting = $state(false)
  let isSaving = $state(false)
  // A typed replacement key plus the verdict of the last probe against it.
  // Both are per-open and never persisted.
  let apiKey = $state('')
  let keyCheck = $state<CredentialCheck>(null)
  let keyError = $state<string | null>(null)
  let isCheckingKey = $state(false)

  const isOAuth = $derived(connection.authType === 'oauth')

  /** On-demand probe of the typed key, for the Check button beside the field. */
  async function checkReplacementKey() {
    if (!apiKey.trim()) return
    isCheckingKey = true
    keyError = null
    try {
      const { check, error } = await probeReplacementKey(connection, apiKey)
      keyCheck = check
      keyError = error
    } finally {
      isCheckingKey = false
    }
  }

  // Typing again invalidates the previous verdict: the last check answered a
  // different key than the one on screen now.
  function resetKeyCheck() {
    keyCheck = null
    keyError = null
  }

  async function testConnection() {
    isTesting = true
    testStatus = null
    testError = null
    try {
      const res = await api.testConnection(connection.id)
      if (res?.valid) {
        testStatus = 'ok'
      } else {
        testStatus = 'error'
        testError = res?.error || 'Test failed'
      }
    } catch (err) {
      testStatus = 'error'
      testError = err instanceof Error ? err.message : 'Test failed'
    } finally {
      isTesting = false
    }
  }

  async function save() {
    isSaving = true
    try {
      const submitted = submittedConnectionName(name, originalName, seededName)
      const payload: ConnectionUpdate = {
        name: submitted
      }
      // Omit an untouched priority: a NULL-priority row has no number of its
      // own, so always sending the seeded 1 would rewrite a plain rename into
      // a rank-1 assignment, tying with whoever already holds it and
      // recreating the un-reorderable pair the reorder endpoint repairs.
      if (priority !== seededPriority) {
        payload.priority = priority
      }
      const rotation = credentialUpdate(apiKey)
      if (rotation) {
        // A key the provider rejects is never written: the stored one is the
        // only thing keeping this account in rotation.
        const { check, error } = await probeReplacementKey(connection, rotation.apiKey)
        keyCheck = check
        keyError = error
        // Returning leaves the modal open with the reason on screen; the
        // stored key is still the one keeping this account in rotation.
        if (error) return
        payload.apiKey = rotation.apiKey
        // Only a provider that actually answered may mark the row active
        // again; an unsupported probe proves nothing about the new key.
        if (check === 'valid') payload.testStatus = 'active'
      }
      await onSave(payload)
    } catch (err) {
      onSaveError(`Failed to save connection: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSaving = false
    }
  }
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
  <div
    class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in"
    onclick={onClose}
    role="presentation"
  ></div>
  <div
    class="relative w-full bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in max-w-md p-6"
  >
    <div class="flex items-center justify-between pb-3 border-b border-border-subtle mb-4">
      <h2 class="text-lg font-semibold text-text-main">Edit Connection</h2>
      <button
        type="button"
        onclick={onClose}
        class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
      >
        <span class="material-symbols-outlined text-lg">close</span>
      </button>
    </div>

    <div class="space-y-4">
      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="edit-conn-name">Name</label>
        <input
          id="edit-conn-name"
          bind:value={name}
          class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background text-text-main focus:outline-none focus:border-primary"
        />
      </div>

      {#if connection.email}
        <div>
          <span class="block text-xs font-medium text-text-muted mb-1">Email</span>
          <p class="text-xs text-text-main font-medium">{formatEmailLabel(connection.email, $emailPrivacy)}</p>
        </div>
      {/if}

      {#if !isOAuth}
        <div>
          <label class="block text-xs font-medium text-text-muted mb-1" for="edit-conn-key">
            API (leave blank to keep the key on file)
          </label>
          <div class="flex gap-2">
            <input
              id="edit-conn-key"
              type="password"
              autocomplete="off"
              spellcheck="false"
              placeholder={credentialPlaceholder(connection)}
              bind:value={apiKey}
              oninput={resetKeyCheck}
              class="min-w-0 flex-1 px-2.5 py-1.5 text-xs border border-border rounded-md bg-background text-text-main focus:outline-none focus:border-primary"
            />
            <button
              type="button"
              onclick={checkReplacementKey}
              disabled={!apiKey.trim() || isCheckingKey || isSaving}
              class="shrink-0 px-2.5 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border disabled:opacity-50 cursor-pointer"
            >
              {isCheckingKey ? 'Checking' : 'Check'}
            </button>
          </div>
          {#if keyCheck === 'valid'}
            <p class="mt-1.5 text-xs text-emerald-600 dark:text-emerald-400">The provider accepted this key.</p>
          {:else if keyCheck === 'unsupported'}
            <p class="mt-1.5 text-xs text-text-subtle">
              This provider has no key check, so the new key is saved unverified.
            </p>
          {:else if keyError}
            <p class="mt-1.5 text-xs text-red-500">{keyError}</p>
          {/if}
        </div>
      {/if}

      <div>
        <label class="block text-xs font-medium text-text-muted mb-1" for="edit-conn-priority">Priority</label>
        <input
          id="edit-conn-priority"
          type="number"
          min="1"
          bind:value={priority}
          class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background text-text-main focus:outline-none focus:border-primary"
        />
      </div>

      {#if testStatus}
        <div class="text-xs {testStatus === 'ok' ? 'text-green-500' : 'text-red-500'}">
          {testStatus === 'ok' ? 'Connection valid!' : testError || 'Test failed'}
        </div>
      {/if}

      <div class="flex items-center justify-between pt-2">
        <button
          type="button"
          onclick={testConnection}
          disabled={isTesting}
          class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border flex items-center gap-1.5 cursor-pointer disabled:opacity-50"
        >
          {#if testSpinner}
            <span class="material-symbols-outlined text-sm {isTesting ? 'animate-spin' : ''}">
              {isTesting ? 'progress_activity' : 'science'}
            </span>
          {/if}
          {isTesting ? 'Testing...' : testLabel}
        </button>

        <div class="flex gap-2">
          <button
            type="button"
            onclick={onClose}
            class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
          >
            Cancel
          </button>
          <button
            type="button"
            onclick={save}
            disabled={isSaving}
            class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-brand-500 hover:bg-brand-600 text-white shadow-sm disabled:opacity-50 cursor-pointer"
          >
            {isSaving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  </div>
</div>