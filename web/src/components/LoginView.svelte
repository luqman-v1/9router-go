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
  // the dashboard password from the browser instead of typing the well-known
  // default first. hasPassword === false on a loopback peer is the wizard.
  let firstRun = $state(false)
  let confirmPassword = $state('')
  // The wizard's second step: is this gateway for one person, or for keys
  // someone else spends? Personal writes the sidebar hide list; Advanced leaves
  // the sidebar as it always was. Neither touches the routes.
  let setupStep = $state<'password' | 'mode'>('password')
  let advancedChoice = $state(true)
  // Show/hide toggles for the two wizard password fields, mirroring the login
  // form's eye button: a long generated password is easier to confirm when it
  // can be read back.
  let showSetupPassword = $state(false)
  let showConfirmPassword = $state(false)
  let skipLoading = $state(false)
  // White-label branding on the login screen. appName/appLogoUrl are the two
  // the sidebar already reads from the same settings row; loginLogo and
  // appTagline are the login-specific pair.
  let appName = $state('')
  let loginLogo = $state('')
  let appTagline = $state('')
  const displayBrandName = $derived(appName || '9router-go')
  const displayBrandLogo = $derived(loginLogo || '/favicon.svg')

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
        // The wizard applies only on a fresh install opened from the machine
        // itself: no stored hash, no INITIAL_PASSWORD, loopback peer. A remote
        // browser on a fresh install sees the normal login (the server refuses
        // its setup POST anyway), so hiding the form would lock the operator
        // out entirely.
        firstRun =
          !data.hasPassword &&
          data.initialPasswordSet !== true &&
          data.requestIsLocal === true &&
          // A gateway whose operator turned the login requirement off is
          // configured, not unfinished: showing the wizard would re-enable it.
          data.requireLogin !== false
        authMode = data.authMode || 'password'
        ssoType = data.ssoType || 'oidc'
        oidcConfigured = data.oidcConfigured === true
        oidcLoginLabel = data.oidcLoginLabel || 'Sign in with OIDC'
        samlConfigured = data.samlConfigured === true
        samlLoginLabel = data.samlLoginLabel || 'Sign in with SAML SSO'
        // White-label branding: appName and appLogoUrl are the same settings the
        // sidebar reads, so an operator's name and login logo appear on this
        // screen too. Empty strings fall back to the project defaults below.
        appName = data.appName || ''
        loginLogo = data.loginLogo || ''
        appTagline = data.appTagline || ''
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

  // password before the first login. The server signs the session in, so a
  // successful setup lands in the dashboard without a second form.
  async function handleSetupSubmit(e: SubmitEvent) {
    e.preventDefault()
    if (!newPassword || isLoading) return
    if (newPassword !== confirmPassword) {
      errorMessage = 'Passwords do not match'
      return
    }

    isLoading = true
    errorMessage = ''

    // The password is kept in memory until the mode is picked: one POST
    // carries both, so a half-finished wizard never stores a password without
    // the mode it was chosen for.
    setupStep = 'mode'
    isLoading = false
  }

  // Skip the password step: keep the default password, finish with the default
  // mode, and land in the dashboard. The operator can set a password later in
  // Settings > Security.
  async function handleSkipPassword() {
    if (isLoading || skipLoading) return
    skipLoading = true
    errorMessage = ''
    try {
      const res = await api.setupSkip()
      if (res.success) {
        firstRun = false
        if (onSuccess) {
          onSuccess()
        } else {
          window.location.assign('/dashboard')
        }
      } else {
        errorMessage = res.error || 'Setup failed'
      }
    } catch (err: unknown) {
      errorMessage = err instanceof Error ? err.message : 'Setup failed'
    } finally {
      skipLoading = false
    }
  }

  async function handleModeSubmit(e: SubmitEvent) {
    e.preventDefault()
    if (isLoading) return
    isLoading = true
    errorMessage = ''
    try {
      const res = await api.setup(newPassword, advancedChoice)
      if (res.success) {
        firstRun = false
        if (onSuccess) {
          onSuccess()
        } else {
          window.location.assign('/dashboard')
        }
      } else {
        errorMessage = res.error || 'Setup failed'
      }
    } catch (err: unknown) {
      errorMessage = err instanceof Error ? err.message : 'Setup failed'
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
          <img src={displayBrandLogo} alt={displayBrandName} class="w-full h-full object-contain" />
        </div>
        <h1 class="text-3xl font-bold text-primary mb-2">{displayBrandName}</h1>
        {#if appTagline}
          <p class="text-sm text-text-muted mb-1">{appTagline}</p>
        {/if}
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
        {#if firstRun}
          {#if setupStep === 'password'}
            <form onsubmit={handleSetupSubmit} class="flex flex-col gap-4">
              <p class="text-sm text-text-muted text-center">
                Welcome to 9router-go. Set a dashboard password to finish setup.
              </p>
              <div class="flex flex-col gap-2">
                <label for="setup-password" class="text-sm font-medium text-text-main">New password</label>
                <div class="relative">
                  <input
                    id="setup-password"
                    type={showSetupPassword ? 'text' : 'password'}
                    placeholder="At least 8 characters"
                    bind:value={newPassword}
                    required
                    minlength="8"
                    data-login-focus
                    class="w-full py-2.5 px-3 pr-10 text-sm text-text-main bg-surface-2 rounded-[10px] border border-transparent placeholder-text-muted/70 focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500 transition-colors"
                  />
                  <button
                    type="button"
                    onclick={() => (showSetupPassword = !showSetupPassword)}
                    class="absolute inset-y-0 right-0 flex items-center pr-3 text-text-muted hover:text-text-main transition-colors cursor-pointer"
                    aria-label={showSetupPassword ? 'Hide password' : 'Show password'}
                  >
                    <span class="material-symbols-outlined text-[20px]">
                      {showSetupPassword ? 'visibility_off' : 'visibility'}
                    </span>
                  </button>
                </div>
              </div>
              <div class="flex flex-col gap-2">
                <label for="setup-password-confirm" class="text-sm font-medium text-text-main">Confirm password</label>
                <div class="relative">
                  <input
                    id="setup-password-confirm"
                    type={showConfirmPassword ? 'text' : 'password'}
                    placeholder="Repeat the password"
                    bind:value={confirmPassword}
                    required
                    class="w-full py-2.5 px-3 pr-10 text-sm text-text-main bg-surface-2 rounded-[10px] border border-transparent placeholder-text-muted/70 focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-500 transition-colors"
                  />
                  <button
                    type="button"
                    onclick={() => (showConfirmPassword = !showConfirmPassword)}
                    class="absolute inset-y-0 right-0 flex items-center pr-3 text-text-muted hover:text-text-main transition-colors cursor-pointer"
                    aria-label={showConfirmPassword ? 'Hide password' : 'Show password'}
                  >
                    <span class="material-symbols-outlined text-[20px]">
                      {showConfirmPassword ? 'visibility_off' : 'visibility'}
                    </span>
                  </button>
                </div>
                {#if errorMessage}
                  <p class="text-xs text-red-500">{errorMessage}</p>
                {/if}
              </div>
              <button
                type="submit"
                disabled={isLoading || !newPassword || newPassword.length < 8 || newPassword !== confirmPassword}
                class="w-full h-9 px-4 text-sm rounded-[10px] font-medium bg-brand-500 hover:bg-brand-600 text-white shadow-sm disabled:bg-surface-3 disabled:text-text-muted disabled:cursor-not-allowed transition-colors flex items-center justify-center gap-2 cursor-pointer"
              >
                <span>Continue</span>
              </button>
              <button
                type="button"
                onclick={handleSkipPassword}
                disabled={isLoading || skipLoading}
                class="text-xs text-text-muted hover:text-text-main transition-colors cursor-pointer"
              >
                {skipLoading ? 'Skipping...' : 'Skip — keep the default password (123456)'}
              </button>
              <p class="text-xs text-center text-text-muted">
                Step 1 of 2. This is accepted from the local machine only, while no
                password is stored.
              </p>
            </form>
          {:else}
            <form onsubmit={handleModeSubmit} class="flex flex-col gap-4">
              <p class="text-sm text-text-muted text-center">
                How will this gateway be used?
              </p>
              <fieldset class="flex flex-col gap-2">
                <legend class="sr-only">Usage mode</legend>
                <label
                  class="flex cursor-pointer gap-3 rounded-[10px] border p-3 transition-colors {advancedChoice
                    ? 'border-border-subtle bg-surface'
                    : 'border-brand-500 bg-brand-500/5'}"
                >
                  <input
                    type="radio"
                    name="setup-mode"
                    class="mt-0.5"
                    checked={!advancedChoice}
                    onchange={() => (advancedChoice = false)}
                  />
                  <span class="flex flex-col gap-0.5">
                    <span class="text-sm font-medium text-text-main">Personal</span>
                    <span class="text-xs text-text-muted">
                      Just me. Plans, budgets and prompt presets are hidden from the
                      sidebar — fewer surfaces to think about. Nothing is removed.
                    </span>
                  </span>
                </label>
                <label
                  class="flex cursor-pointer gap-3 rounded-[10px] border p-3 transition-colors {advancedChoice
                    ? 'border-brand-500 bg-brand-500/5'
                    : 'border-border-subtle bg-surface'}"
                >
                  <input
                    type="radio"
                    name="setup-mode"
                    class="mt-0.5"
                    checked={advancedChoice}
                    onchange={() => (advancedChoice = true)}
                  />
                  <span class="flex flex-col gap-0.5">
                    <span class="text-sm font-medium text-text-main">Advanced / sharing keys</span>
                    <span class="text-xs text-text-muted">
                      Keys for other people. Plans, per-key budgets, usage export and
                      prompt presets are on.
                    </span>
                  </span>
                </label>
              </fieldset>
              {#if errorMessage}
                <p class="text-xs text-red-500">{errorMessage}</p>
              {/if}
              <button
                type="submit"
                disabled={isLoading}
                class="w-full h-9 px-4 text-sm rounded-[10px] font-medium bg-brand-500 hover:bg-brand-600 text-white shadow-sm disabled:bg-surface-3 disabled:text-text-muted disabled:cursor-not-allowed transition-colors flex items-center justify-center gap-2 cursor-pointer"
              >
                {#if isLoading}
                  <span class="inline-block animate-spin rounded-full h-4 w-4 border-2 border-white/20 border-t-white"></span>
                  <span>Setting up...</span>
                {:else}
                  <span>Create password</span>
                {/if}
              </button>
              <button
                type="button"
                onclick={() => {
                  errorMessage = ''
                  setupStep = 'password'
                }}
                class="text-xs text-text-muted hover:text-text-main transition-colors cursor-pointer"
              >
                Back
              </button>
              <p class="text-xs text-center text-text-muted">
                Step 2 of 2. You can change this later in Settings &rarr; Sidebar.
              </p>
            </form>
          {/if}
        {:else if mustChange}
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
