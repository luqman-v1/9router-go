<script lang="ts">
  import { onMount, tick } from 'svelte'
  import { api } from '../api/client'

  let {
    onSuccess,
  }: {
    onSuccess?: () => void
  } = $props()

  let password = $state('')
  let newPassword = $state('')
  let showPassword = $state(false)
  let isLoading = $state(false)
  let errorMessage = $state('')
  let resetHint = $state('')
  let retryAfter = $state(0)
  let hasPassword = $state<boolean | null>(null)
  let authMode = $state('password')
  let ssoType = $state('oidc')
  let oidcConfigured = $state(false)
  let oidcLoginLabel = $state('Sign in with OIDC')
  let samlConfigured = $state(false)
  let samlLoginLabel = $state('Sign in with SAML SSO')
  let mustChange = $state(false)

  // Countdown for rate-limit lockout (upstream retryAfter).
  $effect(() => {
    if (retryAfter <= 0) return
    const id = setInterval(() => {
      retryAfter = retryAfter > 0 ? retryAfter - 1 : 0
    }, 1000)
    return () => clearInterval(id)
  })

  onMount(() => {
    // Surface SSO callback failures (?error=...) like upstream /login.
    const queryError = new URLSearchParams(window.location.search).get('error')
    if (queryError) errorMessage = queryError
    const controller = new AbortController()
    const timeoutId = setTimeout(() => controller.abort(), 5000)
    fetch('/api/auth/status', { signal: controller.signal })
      .then(async (res) => {
        clearTimeout(timeoutId)

        if (!res.ok) {
          // Safe fallback to avoid an infinite loading state.
          hasPassword = true
          tick().then(focusPasswordField)
          return
        }
        const data = await res.json()
        if (data.authenticated === true || data.requireLogin === false) {
          if (onSuccess) {
            onSuccess()
          } else {
            window.location.assign('/dashboard')
          }
          return
        }
        hasPassword = !!data.hasPassword
        authMode = data.authMode || 'password'
        ssoType = data.ssoType || 'oidc'
        oidcConfigured = data.oidcConfigured === true
        oidcLoginLabel = data.oidcLoginLabel || 'Sign in with OIDC'
        samlConfigured = data.samlConfigured === true
        samlLoginLabel = data.samlLoginLabel || 'Sign in with SAML SSO'
        // The form only exists once hasPassword has left null, so focus it as
        // soon as the state that reveals it has been applied.
        tick().then(focusPasswordField)
      })
      .catch(() => {
        clearTimeout(timeoutId)
        hasPassword = true
        tick().then(focusPasswordField)
      })
  })

  // Focus the field the user has to type into, once the form is on screen.
  //
  // The `autofocus` attribute was the obvious way to do this and it warned in
  // the console: "Autofocus processing was blocked because a document already
  // has a focused element." Reached from /dashboard without a session the app
  // mounts this view beside the page it was already showing, and the browser
  // resolves autofocus per document — the body had already taken focus, so the
  // attribute was refused. Asking for focus after mount gets the same result
  // without asking the browser to arbitrate.
  function focusPasswordField() {
    document.querySelector<HTMLElement>('#login-password')?.focus()
  }

  type LoginFailure = Error & {
    retryAfter?: number
    resetHint?: string
    mustChangePassword?: boolean
  }

  async function handleSubmit(e: SubmitEvent) {
    e.preventDefault()
    if (!password.trim() || isLoading || retryAfter > 0) return

    isLoading = true
    errorMessage = ''
    resetHint = ''

    try {
      const res = await api.login(password)
      if (res.success) {
        if (onSuccess) {
          onSuccess()
        } else {
          window.location.assign('/dashboard')
        }
      } else {
        errorMessage = res.error || 'Invalid password'
      }
    } catch (err: unknown) {
      const failure = err as LoginFailure
      if (failure?.mustChangePassword) {
        // Remote fresh install on the well-known default: rotate first. The
        // login attempt never issues a session, so set the new password from
        // the local host (or set INITIAL_PASSWORD) before retrying.
        mustChange = true
        errorMessage = err instanceof Error ? err.message : 'Default password must be changed before remote access.'
      } else {
        errorMessage = err instanceof Error ? err.message : 'Invalid password'
        if (typeof failure?.retryAfter === 'number') retryAfter = failure.retryAfter
        if (typeof failure?.resetHint === 'string') resetHint = failure.resetHint
      }
    } finally {
      isLoading = false
    }
  }

  // Force a new password before entering the dashboard (default + remote).
  async function handleSetNewPassword(e: SubmitEvent) {
    e.preventDefault()
    if (!newPassword || isLoading) return

    isLoading = true
    errorMessage = ''

    try {
      const res = await api.login(password, newPassword)
      if (res.success) {
        if (onSuccess) {
          onSuccess()
        } else {
          window.location.assign('/dashboard')
        }
      } else {
        errorMessage = res.error || 'Failed to set password'
      }
    } catch (err: unknown) {
      errorMessage = err instanceof Error ? err.message : 'Failed to set password'
    } finally {
      isLoading = false
    }
  }

  function handleOidcLogin() {
    window.location.href = '/api/auth/oidc/start'
  }

  function handleSamlLogin() {
    window.location.href = '/api/auth/saml/start'
  }

  const isSsoEnabled = $derived(['sso', 'oidc', 'saml', 'both'].includes(authMode))
  const activeSsoType = $derived(ssoType || (authMode === 'saml' ? 'saml' : 'oidc'))
  const samlAvailable = $derived(isSsoEnabled && activeSsoType === 'saml' && samlConfigured)
  const oidcAvailable = $derived(isSsoEnabled && activeSsoType === 'oidc' && oidcConfigured)
  const ssoAvailable = $derived(samlAvailable || oidcAvailable)
  const passwordAvailable = $derived(authMode === 'password' || authMode === 'both' || !ssoAvailable)
</script>

<div class="min-h-screen flex items-center justify-center bg-bg p-4 relative overflow-hidden">
  <div class="landing-grid absolute inset-0 pointer-events-none" aria-hidden="true"></div>

  <div class="relative z-10 w-full max-w-md">
    {#if hasPassword === null}
      <div class="text-center">
        <div class="inline-block animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
        <p class="text-text-muted mt-4">Loading...</p>
      </div>
    {:else}
      <div class="text-center mb-8 flex flex-col items-center">
        <div class="size-14 rounded-2xl bg-surface border border-border-subtle shadow-[var(--shadow-warm)] flex items-center justify-center p-2.5 mb-4">
          <img src="/favicon.svg" alt="9router-go" class="w-full h-full object-contain" />
        </div>
        <h1 class="text-3xl font-bold text-primary mb-2">9router-go</h1>
        <p class="text-text-muted">
          {#if samlAvailable}
            Sign in with SAML 2.0 Single Sign-On
          {:else if oidcAvailable}
            Sign in with your OIDC provider to access the dashboard
          {:else}
            Enter your password to access the dashboard
          {/if}
        </p>
      </div>

      <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
        {#if mustChange}
          <form onsubmit={handleSetNewPassword} class="flex flex-col gap-4">
            <p class="text-sm text-amber-600 dark:text-amber-400 text-center">
              Set a new password before accessing the dashboard remotely.
            </p>
            <div class="flex flex-col gap-2">
              <label for="login-new-password" class="text-sm font-medium text-text-main">New password</label>
              <input
                id="login-new-password"
                type="password"
                placeholder="Enter new password"
                bind:value={newPassword}
                required
                data-login-focus
                class="w-full py-2.5 px-3 text-sm text-text-main bg-surface-2 rounded-[10px] border border-transparent placeholder-text-muted/70 focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500 transition-colors"
              />
              {#if errorMessage}
                <p class="text-xs text-red-500">{errorMessage}</p>
              {/if}
            </div>
            <button
              type="submit"
              disabled={isLoading || !newPassword}
              class="w-full h-9 px-4 text-sm rounded-[10px] font-medium bg-brand-500 hover:bg-brand-600 text-white shadow-sm disabled:bg-surface-3 disabled:text-text-muted disabled:cursor-not-allowed transition-colors flex items-center justify-center gap-2 cursor-pointer"
            >
              {#if isLoading}
                <span class="inline-block animate-spin rounded-full h-4 w-4 border-2 border-white/20 border-t-white"></span>
                <span>Setting password...</span>
              {:else}
                <span>Set password</span>
              {/if}
            </button>
          </form>
        {:else}
          <div class="flex flex-col gap-4">
            {#if samlAvailable}
              <button
                type="button"
                onclick={handleSamlLogin}
                class="w-full h-9 px-4 text-sm rounded-[10px] font-medium bg-brand-500 hover:bg-brand-600 text-white shadow-sm transition-colors cursor-pointer"
              >
                {samlLoginLabel}
              </button>
            {/if}

            {#if oidcAvailable}
              <button
                type="button"
                onclick={handleOidcLogin}
                class="w-full h-9 px-4 text-sm rounded-[10px] font-medium bg-brand-500 hover:bg-brand-600 text-white shadow-sm transition-colors cursor-pointer"
              >
                {oidcLoginLabel}
              </button>
            {/if}

            {#if ssoAvailable && passwordAvailable}
              <div class="h-px bg-border-subtle"></div>
            {/if}

            {#if passwordAvailable}
              <form onsubmit={handleSubmit} class="flex flex-col gap-4">
                {#if isSsoEnabled && !ssoAvailable}
                  <p class="text-xs text-amber-600 dark:text-amber-400 text-center">
                    {activeSsoType === 'saml' ? 'SAML SSO' : 'OIDC'} login is enabled, but configuration is incomplete. Password login is still available for recovery.
                  </p>
                {/if}

                {#if authMode === 'both' && ssoAvailable}
                  <p class="text-xs text-text-muted text-center">
                    Password and {activeSsoType === 'saml' ? 'SAML SSO' : 'OIDC'} login are both enabled.
                  </p>
                {/if}

                <div class="flex flex-col gap-2">
                  <label for="login-password" class="text-sm font-medium text-text-main">Password</label>
                  <div class="relative">
                    <input
                      id="login-password"
                      type={showPassword ? 'text' : 'password'}
                      placeholder="Enter password"
                      bind:value={password}
                      required
                      data-login-focus
                      class="w-full py-2.5 px-3 pr-10 text-sm text-text-main bg-surface-2 rounded-[10px] border border-transparent placeholder-text-muted/70 focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500 transition-colors"
                    />
                    <button
                      type="button"
                      onclick={() => (showPassword = !showPassword)}
                      class="absolute inset-y-0 right-0 flex items-center pr-3 text-text-muted hover:text-text-main transition-colors cursor-pointer"
                      aria-label={showPassword ? 'Hide password' : 'Show password'}
                    >
                      <span class="material-symbols-outlined text-[20px]">
                        {showPassword ? 'visibility_off' : 'visibility'}
                      </span>
                    </button>
                  </div>
                  {#if errorMessage}
                    <p class="text-xs text-red-500">{errorMessage}</p>
                  {/if}
                  {#if retryAfter > 0}
                    <p class="text-xs text-amber-600 dark:text-amber-400">
                      Locked. Retry in <span class="font-mono">{retryAfter}s</span>.
                    </p>
                  {/if}
                  {#if resetHint}
                    <p class="text-xs text-text-muted">{resetHint}</p>
                  {/if}
                </div>

                <button
                  type="submit"
                  disabled={isLoading || !password || retryAfter > 0}
                  class="w-full h-9 px-4 text-sm rounded-[10px] font-medium bg-brand-500 hover:bg-brand-600 text-white shadow-sm disabled:bg-surface-3 disabled:text-text-muted disabled:cursor-not-allowed transition-colors flex items-center justify-center gap-2 cursor-pointer"
                >
                  {#if isLoading}
                    <span class="inline-block animate-spin rounded-full h-4 w-4 border-2 border-white/20 border-t-white"></span>
                    <span>Logging in...</span>
                  {:else if retryAfter > 0}
                    <span>Wait {retryAfter}s</span>
                  {:else}
                    <span>Login</span>
                  {/if}
                </button>

                <p class="text-xs text-center text-text-muted mt-2">
                  Default password is <code class="bg-surface-2 px-1.5 py-0.5 rounded text-text-main font-mono">123456</code>
                </p>
                {#if hasPassword === false}
                  <p class="text-xs text-center text-amber-600 dark:text-amber-400">
                    Security risk: no password set. You will be asked to set one when logging in remotely.
                  </p>
                {/if}
              </form>
            {:else if errorMessage}
              <p class="text-xs text-red-500">{errorMessage}</p>
            {/if}
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>
