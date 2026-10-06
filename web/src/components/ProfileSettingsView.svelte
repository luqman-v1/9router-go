<script lang="ts">
  import { onMount } from 'svelte'
  import {
    AlertCircle,
    Check,
    Database,
    Download,
    Eye,
    EyeOff,
    Globe,
    Key,
    Laptop,
    Loader2,
    Lock,
    RefreshCw,
    Save,
    Shield,
    Sliders,
    Upload,
    User,
    Zap
  } from 'lucide-svelte'
  import Card from '../lib/ui/Card.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import { api, getAuthHeaders, responseErrorMessage, type Settings } from '../api/client'

  interface Props {
    settings?: Settings
    onRefresh?: () => void
  }

  let {
    settings = {},
    onRefresh
  }: Props = $props()

  // General Settings State
  let requireLogin = $state(false)
  let sessionTimeout = $state('24h')
  let selectedLanguage = $state('en')
  let enableObservability = $state(false)

  // Routing Strategy State
  let fallbackStrategy = $state('failover')
  let stickyRoundRobinLimit = $state(3)
  let comboStrategy = $state('first-model')
  let forceFallback = $state(false)

  // Password Management State
  let currentPassword = $state('')
  let newPassword = $state('')
  let confirmNewPassword = $state('')
  let isUpdatingPassword = $state(false)
  let passwordSuccessMessage = $state<string | null>(null)
  let passwordErrorMessage = $state<string | null>(null)
  let showPasswordFields = $state(false)

  // SSO State
  let authMode = $state<'password' | 'oidc' | 'saml'>('password')
  let oidcIssuerUrl = $state('')
  let oidcClientId = $state('')
  let oidcScopes = $state('openid profile email')
  let oidcLoginLabel = $state('Sign in with OIDC')

  let samlEntryPoint = $state('')
  let samlIssuer = $state('')
  let samlCert = $state('')
  let samlLoginLabel = $state('Sign in with SAML SSO')

  // Save states
  let isSavingSettings = $state(false)
  let saveSuccess = $state(false)

  // Database Backup / Import
  let isDownloadingBackup = $state(false)
  let isImportingBackup = $state(false)
  let fileInput: HTMLInputElement | null = $state(null)
  let dbPassword = $state('')
  let dbAuthOpen = $state(false)
  let pendingImportFile: File | null = $state(null)

  $effect(() => {
    if (settings) {
      requireLogin = !!settings.requireLogin
      enableObservability = !!settings.enableObservability
      if (typeof settings.sessionTimeout === 'string') sessionTimeout = settings.sessionTimeout
      if (typeof settings.language === 'string') selectedLanguage = settings.language
      if (typeof settings.fallbackStrategy === 'string') fallbackStrategy = settings.fallbackStrategy
      if (typeof settings.stickyRoundRobinLimit === 'number') stickyRoundRobinLimit = settings.stickyRoundRobinLimit
      if (typeof settings.comboStrategy === 'string') comboStrategy = settings.comboStrategy
      if (typeof settings.forceFallback === 'boolean') forceFallback = settings.forceFallback

      // SSO
      if (settings.authMode === 'oidc' || settings.authMode === 'saml') authMode = settings.authMode
      if (typeof settings.oidcIssuerUrl === 'string') oidcIssuerUrl = settings.oidcIssuerUrl
      if (typeof settings.oidcClientId === 'string') oidcClientId = settings.oidcClientId
      if (typeof settings.oidcScopes === 'string') oidcScopes = settings.oidcScopes
      if (typeof settings.oidcLoginLabel === 'string') oidcLoginLabel = settings.oidcLoginLabel

      if (typeof settings.samlEntryPoint === 'string') samlEntryPoint = settings.samlEntryPoint
      if (typeof settings.samlIssuer === 'string') samlIssuer = settings.samlIssuer
      if (typeof settings.samlCert === 'string') samlCert = settings.samlCert
      if (typeof settings.samlLoginLabel === 'string') samlLoginLabel = settings.samlLoginLabel
    }
  })

  async function loadSettings() {
    try {
      const s = await api.getSettings()
      if (s) {
        requireLogin = !!s.requireLogin
        enableObservability = !!s.enableObservability
        if (s.sessionTimeout) sessionTimeout = String(s.sessionTimeout)
        if (s.language) selectedLanguage = String(s.language)
        if (s.fallbackStrategy) fallbackStrategy = String(s.fallbackStrategy)
        if (typeof s.stickyRoundRobinLimit === 'number') stickyRoundRobinLimit = s.stickyRoundRobinLimit
        if (s.comboStrategy) comboStrategy = String(s.comboStrategy)
        if (typeof s.forceFallback === 'boolean') forceFallback = s.forceFallback

        if (s.authMode === 'oidc' || s.authMode === 'saml') authMode = s.authMode
        if (s.oidcIssuerUrl) oidcIssuerUrl = String(s.oidcIssuerUrl)
        if (s.oidcClientId) oidcClientId = String(s.oidcClientId)
        if (s.oidcScopes) oidcScopes = String(s.oidcScopes)
        if (s.oidcLoginLabel) oidcLoginLabel = String(s.oidcLoginLabel)

        if (s.samlEntryPoint) samlEntryPoint = String(s.samlEntryPoint)
        if (s.samlIssuer) samlIssuer = String(s.samlIssuer)
        if (s.samlCert) samlCert = String(s.samlCert)
        if (s.samlLoginLabel) samlLoginLabel = String(s.samlLoginLabel)
      }
    } catch {
      // silent
    }
  }

  onMount(() => {
    loadSettings()
  })

  async function handleSaveAll() {
    isSavingSettings = true
    saveSuccess = false
    try {
      await api.updateSettings({
        requireLogin,
        sessionTimeout,
        language: selectedLanguage,
        enableObservability,
        fallbackStrategy,
        stickyRoundRobinLimit,
        comboStrategy,
        forceFallback,
        authMode,
        oidcIssuerUrl,
        oidcClientId,
        oidcScopes,
        oidcLoginLabel,
        samlEntryPoint,
        samlIssuer,
        samlCert,
        samlLoginLabel,
      })
      saveSuccess = true
      onRefresh?.()
      setTimeout(() => (saveSuccess = false), 3000)
    } catch (err) {
      alert(`Failed to save settings: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSavingSettings = false
    }
  }

  async function handleUpdatePassword(e: SubmitEvent) {
    e.preventDefault()
    passwordErrorMessage = null
    passwordSuccessMessage = null

    if (newPassword !== confirmNewPassword) {
      passwordErrorMessage = 'Passwords do not match'
      return
    }
    if (newPassword.length < 6) {
      passwordErrorMessage = 'Password must be at least 6 characters'
      return
    }

    isUpdatingPassword = true
    try {
      await api.updateSettings({
        currentPassword,
        newPassword,
      })
      passwordSuccessMessage = 'Password updated successfully!'
      currentPassword = ''
      newPassword = ''
      confirmNewPassword = ''
      showPasswordFields = false
      onRefresh?.()
    } catch (err) {
      passwordErrorMessage = err instanceof Error ? err.message : 'Failed to update password'
    } finally {
      isUpdatingPassword = false
    }
  }

  // closeDbAuth is the single dismissal path for the password modal. Modal
  // wires Escape, the overlay and both ✕ buttons to onClose, and an import
  // already in flight is a destructive operation the user must not be able to
  // abandon by pressing a key — dismissing the dialog would leave the POST
  // running and then wipe the database on completion.
  function closeDbAuth() {
    if (isImportingBackup || isDownloadingBackup) return
    dbAuthOpen = false
    dbPassword = ''
    pendingImportFile = null
  }

  function handleDownloadBackup() {
    if (isImportingBackup || isDownloadingBackup) return
    dbPassword = ''
    pendingImportFile = null
    dbAuthOpen = true
  }

  async function runDownloadBackup() {
    isDownloadingBackup = true
    const password = dbPassword
    try {
      const res = await fetch('/api/settings/database', {
        headers: { ...getAuthHeaders(), 'x-9r-password': password },
      })
      if (!res.ok) {
        throw new Error(await responseErrorMessage(res, 'Failed to export database'))
      }
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      const stamp = new Date().toISOString().replace(/[.:]/g, '-')
      a.href = url
      a.download = `9router-backup-${stamp}.json`
      document.body.appendChild(a)
      a.click()
      setTimeout(() => {
        document.body.removeChild(a)
        URL.revokeObjectURL(url)
      }, 1000)
    } catch (err) {
      alert(`Failed to download backup: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isDownloadingBackup = false
      dbAuthOpen = false
      dbPassword = ''
    }
  }

  function handleFileSelected(e: Event) {
    if (isImportingBackup || isDownloadingBackup) return
    const target = e.target as HTMLInputElement
    const file = target.files?.[0]
    if (!file) return

    target.value = ''
    pendingImportFile = file
    dbPassword = ''
    dbAuthOpen = true
  }

  async function runImportBackup() {
    const file = pendingImportFile
    if (!file) return
    // Snapshot the password before the first await: file.text() and
    // JSON.parse() can both yield, and reading live state afterwards would let a
    // concurrent dismissal blank the credential the request is authorized with.
    const password = dbPassword
    isImportingBackup = true
    try {
      let res: Response
      const isZip = file.name.endsWith('.zip') || file.type === 'application/zip'
      if (isZip) {
        const buffer = await file.arrayBuffer()
        res = await fetch('/api/settings/database', {
          method: 'POST',
          headers: { ...getAuthHeaders(), 'x-9r-password': password, 'Content-Type': 'application/zip' },
          body: buffer,
        })
      } else {
        const raw = await file.text()
        const payload = JSON.parse(raw)
        res = await fetch('/api/settings/database', {
          method: 'POST',
          headers: { ...getAuthHeaders(), 'x-9r-password': password, 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        })
      }
      if (!res.ok) {
        throw new Error(await responseErrorMessage(res, 'Failed to import database'))
      }
      alert('Database backup imported successfully! Reloading page...')
      window.location.reload()
    } catch (err) {
      alert(`Failed to import database: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isImportingBackup = false
      pendingImportFile = null
      dbAuthOpen = false
      dbPassword = ''
    }
  }
</script>

<div class="flex flex-col gap-6">
  <!-- PAGE HEADER & SAVE BUTTON -->
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
    <div class="space-y-1">
      <div class="flex items-center gap-2">
        <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
          <User class="w-5 h-5" />
        </div>
        <div>
          <h1 class="font-headline text-2xl sm:text-3xl font-bold text-text-main tracking-tight flex items-center gap-2">
            Settings & Profile
          </h1>
          <p class="font-body text-xs sm:text-sm text-text-muted">
            System configuration, authentication credentials, and local data storage
          </p>
        </div>
      </div>
    </div>

    <button
      type="button"
      onclick={handleSaveAll}
      disabled={isSavingSettings}
      class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 disabled:opacity-50 text-white font-semibold text-xs transition cursor-pointer shadow-md shadow-brand-500/20"
    >
      {#if isSavingSettings}
        <Loader2 class="w-3.5 h-3.5 animate-spin" />
        <span>Saving...</span>
      {:else if saveSuccess}
        <Check class="w-3.5 h-3.5" />
        <span>Settings Saved!</span>
      {:else}
        <Save class="w-3.5 h-3.5" />
        <span>Save Changes</span>
      {/if}
    </button>
  </div>

  <div class="grid grid-cols-1 lg:grid-cols-2 gap-5">
    <!-- SECTION 1: Runtime Storage -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Laptop class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Runtime Storage</h2>
        </div>
        <span class="text-[10px] font-mono font-bold px-2 py-0.5 rounded bg-success/10 text-success border border-success/20">
          Server managed
        </span>
      </div>

      <div class="space-y-2 text-xs">
        <div class="p-3 rounded-xl bg-bg border border-border space-y-1">
          <div class="text-[10px] text-text-subtle uppercase font-mono tracking-wider font-semibold">
            Default Database Location
          </div>
          <div class="font-mono text-xs text-text-main font-semibold">
            ~/.9router/db/data.sqlite
          </div>
          <div class="text-[11px] text-text-muted pt-1">
            The Go server opens the configured SQLite database in WAL mode. DB_PATH or DATA_DIR may select a different location.
          </div>
          <div class="text-[11px] text-text-subtle pt-1">
            The native server does not run upstream's versioned schema migration. A new empty database must be initialized with a compatible upstream schema before use.
          </div>
          <div class="text-[11px] text-text-subtle pt-1">
            Backups are dashboard-data payloads, not a byte-for-byte copy of the live SQLite file.
          </div>
        </div>

        <div class="flex items-center gap-2 pt-1">
          <button
            type="button"
            onclick={handleDownloadBackup}
            disabled={isDownloadingBackup || isImportingBackup}
            class="flex-1 py-2 px-3 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs font-semibold text-text-main transition cursor-pointer flex items-center justify-center gap-1.5"
          >
            <Download class="w-3.5 h-3.5 text-brand-500" />
            <span>Download Backup</span>
          </button>

          <input
            type="file"
            accept=".zip,.json,application/zip,application/json"
            bind:this={fileInput}
            onchange={handleFileSelected}
            class="hidden"
          />

          <button
            type="button"
            onclick={() => fileInput?.click()}
            disabled={isImportingBackup || isDownloadingBackup}
            class="flex-1 py-2 px-3 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs font-semibold text-text-main transition cursor-pointer flex items-center justify-center gap-1.5"
          >
            <Upload class="w-3.5 h-3.5 text-text-muted" />
            <span>Import Backup</span>
          </button>
        </div>
      </div>
    </Card>

    <!-- SECTION 2: Language & Region -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center gap-2 pb-2 border-b border-border">
        <Globe class="w-4 h-4 text-brand-500" />
        <h2 class="text-sm font-bold text-text-main">Language & Display</h2>
      </div>

      <div class="space-y-3 text-xs">
        <div class="space-y-1">
          <label for="lang-select" class="block font-semibold text-text-main">Dashboard Language</label>
          <select
            id="lang-select"
            bind:value={selectedLanguage}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="en">English (US)</option>
            <option value="zh-CN">简体中文 (Simplified Chinese)</option>
            <option value="zh-TW">繁體中文 (Traditional Chinese)</option>
            <option value="ja">日本語 (Japanese)</option>
            <option value="ko">한국어 (Korean)</option>
            <option value="es">Español</option>
            <option value="de">Deutsch</option>
          </select>
          <p class="text-[11px] text-text-subtle">
            Select the primary interface language for the 9router-go web dashboard.
          </p>
        </div>
      </div>
    </Card>

    <!-- SECTION 3: Security & Master Password -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Shield class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Security & Password</h2>
        </div>
        <Toggle
          checked={requireLogin}
          size="sm"
          label="Require login"
          onChange={(val) => (requireLogin = val)}
        />
      </div>

      <div class="space-y-3 text-xs">
        <div class="flex items-center justify-between">
          <div>
            <p class="font-semibold text-text-main">Require Password on Localhost</p>
            <p class="text-[11px] text-text-subtle">Default password is <code class="font-mono text-brand-500">123456</code></p>
          </div>
        </div>

        <div class="space-y-1 pt-1">
          <label for="session-timeout" class="block font-semibold text-text-main">Session Timeout</label>
          <select
            id="session-timeout"
            bind:value={sessionTimeout}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="15m">15 Minutes</option>
            <option value="1h">1 Hour</option>
            <option value="24h">24 Hours</option>
            <option value="7d">7 Days</option>
            <option value="never">Never (Stay signed in)</option>
          </select>
        </div>

        <!-- Update Password Toggle & Form -->
        <div class="pt-2 border-t border-border/60">
          <button
            type="button"
            onclick={() => (showPasswordFields = !showPasswordFields)}
            class="text-xs font-semibold text-brand-500 hover:opacity-80 cursor-pointer"
          >
            {showPasswordFields ? 'Hide Change Password' : 'Change Master Password'}
          </button>

          {#if showPasswordFields}
            <form onsubmit={handleUpdatePassword} class="space-y-2.5 pt-3">
              {#if passwordErrorMessage}
                <div class="p-2.5 rounded-lg bg-danger/10 border border-danger/20 text-red-600 dark:text-red-400 text-[11px]">
                  {passwordErrorMessage}
                </div>
              {/if}
              {#if passwordSuccessMessage}
                <div class="p-2.5 rounded-lg bg-success/10 border border-success/20 text-success text-[11px]">
                  {passwordSuccessMessage}
                </div>
              {/if}

              <div class="space-y-1">
                <label for="curr-pwd" class="block text-[11px] font-semibold text-text-muted">Current Password</label>
                <input
                  id="curr-pwd"
                  type="password"
                  bind:value={currentPassword}
                  placeholder="123456"
                  class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
                  required
                />
              </div>

              <div class="space-y-1">
                <label for="new-pwd" class="block text-[11px] font-semibold text-text-muted">New Password</label>
                <input
                  id="new-pwd"
                  type="password"
                  bind:value={newPassword}
                  placeholder="At least 6 characters"
                  class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
                  required
                />
              </div>

              <div class="space-y-1">
                <label for="confirm-pwd" class="block text-[11px] font-semibold text-text-muted">Confirm New Password</label>
                <input
                  id="confirm-pwd"
                  type="password"
                  bind:value={confirmNewPassword}
                  placeholder="Re-enter new password"
                  class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
                  required
                />
              </div>

              <button
                type="submit"
                disabled={isUpdatingPassword}
                class="w-full py-2 px-3 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer"
              >
                {isUpdatingPassword ? 'Updating...' : 'Update Password'}
              </button>
            </form>
          {/if}
        </div>
      </div>
    </Card>

    <!-- SECTION 4: Single Sign-On (SSO) -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Key class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Single Sign-On (SSO)</h2>
        </div>
      </div>

      <div class="space-y-3 text-xs">
        <div class="space-y-1">
          <label for="auth-mode-select" class="block font-semibold text-text-main">Authentication Mode</label>
          <select
            id="auth-mode-select"
            bind:value={authMode}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="password">Password Only</option>
            <option value="oidc">OpenID Connect (OIDC)</option>
            <option value="saml">SAML 2.0 SSO</option>
          </select>
        </div>

        {#if authMode === 'oidc'}
          <div class="space-y-2 pt-2 border-t border-border/60">
            <div class="space-y-1">
              <label for="oidc-issuer" class="block font-semibold text-text-muted">Issuer URL</label>
              <input
                id="oidc-issuer"
                type="text"
                bind:value={oidcIssuerUrl}
                placeholder="https://accounts.google.com or Okta URL"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="oidc-client-id" class="block font-semibold text-text-muted">Client ID</label>
              <input
                id="oidc-client-id"
                type="text"
                bind:value={oidcClientId}
                placeholder="client-id"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="oidc-scopes" class="block font-semibold text-text-muted">Scopes</label>
              <input
                id="oidc-scopes"
                type="text"
                bind:value={oidcScopes}
                placeholder="openid profile email"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
          </div>
        {:else if authMode === 'saml'}
          <div class="space-y-2 pt-2 border-t border-border/60">
            <div class="space-y-1">
              <label for="saml-entrypoint" class="block font-semibold text-text-muted">SAML EntryPoint (SSO URL)</label>
              <input
                id="saml-entrypoint"
                type="text"
                bind:value={samlEntryPoint}
                placeholder="https://idp.example.com/sso"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="saml-issuer" class="block font-semibold text-text-muted">SP Entity ID / Issuer</label>
              <input
                id="saml-issuer"
                type="text"
                bind:value={samlIssuer}
                placeholder="https://9router.local"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="saml-cert" class="block font-semibold text-text-muted">X.509 Certificate (PEM)</label>
              <textarea
                id="saml-cert"
                bind:value={samlCert}
                rows={3}
                placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"
                class="w-full p-2.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              ></textarea>
            </div>
          </div>
        {/if}
      </div>
    </Card>

    <!-- SECTION 5: Routing Strategy & Limits -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center gap-2 pb-2 border-b border-border">
        <Sliders class="w-4 h-4 text-brand-500" />
        <h2 class="text-sm font-bold text-text-main">Default Routing Strategy</h2>
      </div>

      <div class="space-y-3 text-xs">
        <div class="space-y-1">
          <label for="fallback-strat" class="block font-semibold text-text-main">Connection Fallback Strategy</label>
          <select
            id="fallback-strat"
            bind:value={fallbackStrategy}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="failover">Failover (Try next on error/rate-limit)</option>
            <option value="round-robin">Round Robin (Distribute requests across all connections)</option>
            <option value="sticky-round-robin">Sticky Round-Robin (Keep active connection up to limit)</option>
          </select>
        </div>

        {#if fallbackStrategy === 'sticky-round-robin'}
          <div class="space-y-1">
            <label for="sticky-limit" class="block font-semibold text-text-main">Sticky Request Limit</label>
            <input
              id="sticky-limit"
              type="number"
              bind:value={stickyRoundRobinLimit}
              min="1"
              max="100"
              class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
            />
            <p class="text-[11px] text-text-subtle">
              Number of consecutive requests routed to the same credential before rotating.
            </p>
          </div>
        {/if}

        <div class="space-y-1">
          <label for="combo-strat" class="block font-semibold text-text-main">Combo Routing Mode</label>
          <select
            id="combo-strat"
            bind:value={comboStrategy}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="first-model">First Model (Always try primary model first)</option>
            <option value="round-robin">Round Robin (Rotate starting model)</option>
          </select>
        </div>

        <Toggle
          checked={forceFallback}
          size="sm"
          label="Force Fallback (all accounts cooling down)"
          onChange={(v: boolean) => (forceFallback = v)}
        />
        <p class="text-[11px] text-text-subtle">
          When every account is in cooldown, send the request through the account that
          frees up first instead of failing. Requests can still be rejected upstream.
        </p>

      </div>
    </Card>

    <!-- SECTION 6: Observability & Tracing -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Zap class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Observability</h2>
        </div>
        <Toggle
          checked={enableObservability}
          size="sm"
          label="Enable Observability"
          onChange={(val) => (enableObservability = val)}
        />
      </div>

      <div class="space-y-2 text-xs">
        <p class="text-text-muted leading-relaxed">
          Collect real-time telemetry, TTFT (Time to First Token), round-trip latency, and token throughput for all routed LLM requests.
        </p>
        <div class="p-3 rounded-xl bg-bg border border-border text-[11px] text-text-subtle space-y-1">
          <div>Telemetry destination: In-memory ring buffer & SQLite <code class="font-mono text-brand-500">requestDetails</code></div>
          <div>Real-time stream: <code class="font-mono text-info">/api/usage/stream</code> (SSE)</div>
        </div>
      </div>
    </Card>

    <Modal
      isOpen={dbAuthOpen}
      onClose={closeDbAuth}
      title={pendingImportFile ? 'Confirm Import' : 'Confirm Download'}
      size="sm"
    >
      <div class="space-y-3">
        <p class="text-text-muted">
          {pendingImportFile
            ? `Import "${pendingImportFile.name}"? This will overwrite existing server data.`
            : 'Download a full backup of the dashboard database (.json file)?'}
        </p>
        <Input
          type="password"
          label="Dashboard password"
          placeholder="Enter password to authorize"
          bind:value={dbPassword}
        />
      </div>
      {#snippet footer()}
        <Button variant="ghost" onclick={closeDbAuth} disabled={isImportingBackup || isDownloadingBackup}>
          Cancel
        </Button>
        <Button
          variant="primary"
          onclick={() => (pendingImportFile ? runImportBackup() : runDownloadBackup())}
          loading={isImportingBackup || isDownloadingBackup}
        >
          {pendingImportFile ? 'Import' : 'Download'}
        </Button>
      {/snippet}
    </Modal>
  </div>
</div>
