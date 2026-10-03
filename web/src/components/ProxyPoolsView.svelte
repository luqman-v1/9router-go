<script lang="ts">
  // Port of upstream src/app/(dashboard)/dashboard/proxy-pools/page.js
  import { onMount } from 'svelte'
  import Badge from '../lib/ui/Badge.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import Input from '../lib/ui/Input.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import { api, getAuthHeaders, type ProxyPool } from '../api/client'
  import { notifications } from '../lib/notifications'
  import { parseProxyLine } from '../lib/proxy-import'
  import { getStatusVariant, getLatencyBadge } from './proxypools/helpers'

  function formatDateTime(value: string | null | undefined): string {
    if (!value) return 'Never'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return 'Never'
    return date.toLocaleString()
  }

  interface PoolForm {
    name: string
    proxyUrl: string
    noProxy: string
    isActive: boolean
    strictProxy: boolean
  }

  function normalizeFormData(data: Partial<ProxyPool> = {}): PoolForm {
    return {
      name: data.name || '',
      proxyUrl: data.proxyUrl || '',
      noProxy: data.noProxy || '',
      isActive: data.isActive !== false,
      strictProxy: data.strictProxy === true,
    }
  }

  let proxyPools = $state<ProxyPool[]>([])
  let loading = $state(true)
  let showFormModal = $state(false)
  let showBatchImportModal = $state(false)
  let showVercelModal = $state(false)
  let showCloudflareModal = $state(false)
  let showDenoModal = $state(false)
  let showRelayMenu = $state(false)
  let editingPool = $state<ProxyPool | null>(null)
  let formData = $state<PoolForm>(normalizeFormData())
  let batchImportText = $state('')
  let vercelForm = $state({ vercelToken: '', projectName: 'vercel-relay' })
  let cloudflareForm = $state({ accountId: '', apiToken: '', projectName: 'cloudflare-relay' })
  let denoForm = $state({ denoToken: '', orgDomain: '', projectName: '' })
  let saving = $state(false)
  let importing = $state(false)
  let deploying = $state(false)
  let testingId = $state<string | null>(null)
  let selectedIds = $state<string[]>([])
  let healthChecking = $state(false)
  let healthProgress = $state({ current: 0, total: 0 })
  let bulkBusy = $state(false)
  let confirmState = $state<{
    title: string
    message: string
    confirmText?: string
    onConfirm: () => Promise<void> | void
  } | null>(null)
  let relayMenuRef = $state<HTMLDivElement | null>(null)

  // Tested pools float to the top (most recently tested first);
  // never-tested pools keep backend order below.
  // Array.prototype.sort is stable, so ties preserve fetch order.
  function sortPools(pools: ProxyPool[]): ProxyPool[] {
    return [...pools].sort((a, b) => {
      const at = a.lastTestedAt ? Date.parse(a.lastTestedAt) || 0 : 0
      const bt = b.lastTestedAt ? Date.parse(b.lastTestedAt) || 0 : 0
      if (at !== 0 || bt !== 0) return bt - at
      return 0
    })
  }

  async function fetchProxyPools() {
    try {
      proxyPools = sortPools(await api.getProxyPools(true))
    } catch (err) {
      console.log('Error fetching proxy pools:', err)
    } finally {
      loading = false
    }
  }

  onMount(() => {
    fetchProxyPools()
  })

  // Close the Deploy Relay menu on outside click (upstream parity).
  $effect(() => {
    if (!showRelayMenu || typeof document === 'undefined') return
    const onDown = (e: MouseEvent) => {
      if (relayMenuRef && !relayMenuRef.contains(e.target as Node)) showRelayMenu = false
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  })

  // Drop selections for pools that no longer exist.
  // Guard the write: assigning a fresh array unconditionally would retrigger
  // this effect forever (new identity each run) and freeze the page on loading.
  $effect(() => {
    const ids = new Set(proxyPools.map((p) => p.id))
    if (selectedIds.some((id) => !ids.has(id))) {
      selectedIds = selectedIds.filter((id) => ids.has(id))
    }
  })

  let statusFilter = $state<'all' | 'active' | 'passed' | 'failed'>('all')
  let sortOption = $state<'default' | 'fastest' | 'recently_tested' | 'name'>('default')

  let activeCount = $derived(proxyPools.filter((p) => p.isActive === true).length)
  let passedCount = $derived(proxyPools.filter((p) => p.testStatus === 'passed').length)
  let failedCount = $derived(proxyPools.filter((p) => p.testStatus === 'failed').length)

  let filteredPools = $derived(
    proxyPools.filter((p) => {
      if (statusFilter === 'active') return p.isActive === true
      if (statusFilter === 'passed') return p.testStatus === 'passed'
      if (statusFilter === 'failed') return p.testStatus === 'failed'
      return true
    })
  )

  let displayedPools = $derived.by(() => {
    if (sortOption === 'fastest') {
      return [...filteredPools].sort((a, b) => {
        const aLat = typeof a.latency === 'number' && a.latency > 0 ? a.latency : Infinity
        const bLat = typeof b.latency === 'number' && b.latency > 0 ? b.latency : Infinity
        if (aLat !== bLat) return aLat - bLat
        return (a.name || '').localeCompare(b.name || '')
      })
    }
    if (sortOption === 'recently_tested') {
      return [...filteredPools].sort((a, b) => {
        const at = a.lastTestedAt ? Date.parse(a.lastTestedAt) || 0 : 0
        const bt = b.lastTestedAt ? Date.parse(b.lastTestedAt) || 0 : 0
        if (at !== bt) return bt - at
        return (a.name || '').localeCompare(b.name || '')
      })
    }
    if (sortOption === 'name') {
      return [...filteredPools].sort((a, b) => (a.name || '').localeCompare(b.name || ''))
    }
    return filteredPools
  })

  let allSelected = $derived(
    displayedPools.length > 0 && displayedPools.every((p) => selectedIds.includes(p.id))
  )

  function toggleSelect(id: string) {
    selectedIds = selectedIds.includes(id)
      ? selectedIds.filter((x) => x !== id)
      : [...selectedIds, id]
  }

  function toggleSelectAll() {
    if (allSelected) {
      const displayedIdSet = new Set(displayedPools.map((p) => p.id))
      selectedIds = selectedIds.filter((id) => !displayedIdSet.has(id))
    } else {
      const set = new Set([...selectedIds, ...displayedPools.map((p) => p.id)])
      selectedIds = Array.from(set)
    }
  }

  function clearSelection() {
    selectedIds = []
  }

  function resetForm() {
    editingPool = null
    formData = normalizeFormData()
  }

  function openCreateModal() {
    resetForm()
    showFormModal = true
  }

  function openEditModal(pool: ProxyPool) {
    editingPool = pool
    formData = normalizeFormData(pool)
    showFormModal = true
  }

  function closeFormModal() {
    showFormModal = false
    resetForm()
  }

  function openBatchImportModal() {
    batchImportText = ''
    showBatchImportModal = true
  }

  function closeBatchImportModal() {
    if (importing) return
    showBatchImportModal = false
  }

  function openVercelModal() {
    vercelForm = { vercelToken: '', projectName: 'vercel-relay' }
    showVercelModal = true
  }

  function closeVercelModal() {
    if (deploying) return
    showVercelModal = false
  }

  function openCloudflareModal() {
    cloudflareForm = { accountId: '', apiToken: '', projectName: 'cloudflare-relay' }
    showCloudflareModal = true
  }

  function closeCloudflareModal() {
    if (deploying) return
    showCloudflareModal = false
  }

  function openDenoModal() {
    denoForm = { denoToken: '', orgDomain: '', projectName: '' }
    showDenoModal = true
  }

  function closeDenoModal() {
    if (deploying) return
    showDenoModal = false
  }

  async function handleSave() {
    const payload = {
      name: formData.name.trim(),
      proxyUrl: formData.proxyUrl.trim(),
      noProxy: formData.noProxy.trim(),
      isActive: formData.isActive === true,
      strictProxy: formData.strictProxy === true,
    }
    if (!payload.name || !payload.proxyUrl) return
    saving = true
    try {
      if (editingPool) {
        await api.updateProxyPool(editingPool.id, payload)
      } else {
        await api.createProxyPool(payload)
      }
      await fetchProxyPools()
      closeFormModal()
      notifications.success(editingPool ? 'Proxy pool updated' : 'Proxy pool created')
    } catch (err) {
      console.log('Error saving proxy pool:', err)
      notifications.error(err instanceof Error ? err.message : 'Failed to save proxy pool')
    } finally {
      saving = false
    }
  }

  function handleDelete(pool: ProxyPool) {
    confirmState = {
      title: 'Delete Proxy Pool',
      message: `Delete proxy pool "${pool.name}"?`,
      onConfirm: async () => {
        confirmState = null
        try {
          const res = await fetch(`/api/proxy-pools/${encodeURIComponent(pool.id)}`, {
            method: 'DELETE',
            headers: getAuthHeaders(),
          })
          if (res.ok) {
            proxyPools = proxyPools.filter((item) => item.id !== pool.id)
            notifications.success('Proxy pool deleted')
            return
          }
          const data = await res.json().catch(() => ({}))
          if (res.status === 409) {
            notifications.warning(
              `Cannot delete: ${data.boundConnectionCount || 0} connection(s) are still using this pool.`
            )
          } else {
            notifications.error(data.error || data?.error?.message || 'Failed to delete proxy pool')
          }
        } catch (err) {
          console.log('Error deleting proxy pool:', err)
          notifications.error('Failed to delete proxy pool')
        }
      },
    }
  }

  async function handleTest(poolId: string) {
    testingId = poolId
    try {
      const data = await api.testProxyPool(poolId)
      proxyPools = proxyPools.map((p) =>
        p.id === poolId
          ? {
              ...p,
              testStatus: data.status || (data.success ? 'passed' : 'failed'),
              latency: typeof data.latency === 'number' ? data.latency : p.latency,
              lastTestedAt: new Date().toISOString(),
            }
          : p
      )
      await fetchProxyPools()
      if (data.success) {
        notifications.success('Proxy test passed')
      } else {
        notifications.error(data.error || 'Proxy test failed')
      }
    } catch (err) {
      console.log('Error testing proxy pool:', err)
      notifications.error('Failed to test proxy')
    } finally {
      testingId = null
    }
  }

  async function handleToggleActive(pool: ProxyPool) {
    const next = !pool.isActive
    proxyPools = proxyPools.map((p) => (p.id === pool.id ? { ...p, isActive: next } : p))
    try {
      await api.updateProxyPool(pool.id, { isActive: next })
    } catch (err) {
      console.log('Error toggling active:', err)
      proxyPools = proxyPools.map((p) => (p.id === pool.id ? { ...p, isActive: pool.isActive } : p))
      notifications.error('Failed to update active state')
    }
  }

  async function bulkSetActive(isActive: boolean) {
    const targets = selectedIds.length > 0 ? selectedIds : proxyPools.map((p) => p.id)
    if (targets.length === 0) return
    bulkBusy = true
    try {
      let ok = 0
      let failed = 0
      for (const id of targets) {
        try {
          await api.updateProxyPool(id, { isActive })
          ok += 1
        } catch {
          failed += 1
        }
      }
      await fetchProxyPools()
      notifications.success(
        `${isActive ? 'Activated' : 'Deactivated'} ${ok}${failed ? `, failed ${failed}` : ''}`
      )
    } finally {
      bulkBusy = false
    }
  }

  function bulkDelete() {
    if (selectedIds.length === 0) return
    const count = selectedIds.length
    confirmState = {
      title: 'Delete Proxy Pools',
      message: `Delete ${count} proxy pool(s)?`,
      onConfirm: async () => {
        confirmState = null
        bulkBusy = true
        try {
          let ok = 0
          let blocked = 0
          let failed = 0
          for (const id of selectedIds) {
            try {
              const res = await fetch(`/api/proxy-pools/${encodeURIComponent(id)}`, {
                method: 'DELETE',
                headers: getAuthHeaders(),
              })
              if (res.ok) ok += 1
              else if (res.status === 409) blocked += 1
              else failed += 1
            } catch {
              failed += 1
            }
          }
          await fetchProxyPools()
          clearSelection()
          notifications.success(
            `Deleted ${ok}${blocked ? `, ${blocked} bound` : ''}${failed ? `, ${failed} failed` : ''}`
          )
        } finally {
          bulkBusy = false
        }
      },
    }
  }

  async function handleHealthCheck(forceAll = false) {
    const targets =
      !forceAll && selectedIds.length > 0
        ? proxyPools.filter((p) => selectedIds.includes(p.id))
        : proxyPools
    if (targets.length === 0) return
    healthChecking = true
    healthProgress = { current: 0, total: targets.length }
    let alive = 0
    const deadIds: string[] = []
    let done = 0
    const CONCURRENCY = 10
    const queue = [...targets]

    const worker = async () => {
      while (queue.length > 0) {
        const pool = queue.shift()
        if (!pool) break
        try {
          const data = await api.testProxyPool(pool.id)
          if (data.success) {
            alive += 1
          } else {
            deadIds.push(pool.id)
          }
          proxyPools = proxyPools.map((p) =>
            p.id === pool.id
              ? {
                  ...p,
                  testStatus: data.status || (data.success ? 'passed' : 'failed'),
                  latency: typeof data.latency === 'number' ? data.latency : p.latency,
                  lastTestedAt: new Date().toISOString(),
                }
              : p
          )
        } catch {
          deadIds.push(pool.id)
          proxyPools = proxyPools.map((p) =>
            p.id === pool.id
              ? {
                  ...p,
                  testStatus: 'failed',
                  lastTestedAt: new Date().toISOString(),
                }
              : p
          )
        } finally {
          done += 1
          healthProgress = { current: done, total: targets.length }
        }
      }
    }

    await Promise.all(
      Array.from({ length: Math.min(CONCURRENCY, targets.length) }, () => worker())
    )
    await fetchProxyPools()
    healthChecking = false
    healthProgress = { current: 0, total: 0 }

    if (deadIds.length > 0) {
      const dead = deadIds.length
      confirmState = {
        title: 'Disable Dead Proxies',
        message: `Alive: ${alive}, Dead: ${dead}.\n\nDisable ${dead} dead proxies?`,
        confirmText: 'Disable Dead',
        onConfirm: async () => {
          confirmState = null
          bulkBusy = true
          try {
            for (const id of deadIds) {
              try {
                await api.updateProxyPool(id, { isActive: false })
              } catch {}
            }
            await fetchProxyPools()
            notifications.success(`Disabled ${dead} dead proxies`)
          } finally {
            bulkBusy = false
          }
        },
      }
    } else {
      notifications.success(`Health check done. Alive: ${alive}, Dead: ${deadIds.length}`)
    }
  }

  async function handleDisableFailed() {
    const failed = proxyPools.filter((p) => p.testStatus === 'failed' && p.isActive !== false)
    if (failed.length === 0) {
      notifications.info('No active failed proxies found')
      return
    }
    bulkBusy = true
    try {
      for (const p of failed) {
        try {
          await api.updateProxyPool(p.id, { isActive: false })
        } catch (err) {
          console.error('Failed to disable proxy:', err)
        }
      }
      await fetchProxyPools()
      notifications.success(`Disabled ${failed.length} failed proxies`)
    } finally {
      bulkBusy = false
    }
  }

  function handleDeleteFailed() {
    const failed = proxyPools.filter((p) => p.testStatus === 'failed')
    if (failed.length === 0) {
      notifications.info('No failed proxies to delete')
      return
    }
    confirmState = {
      title: 'Delete Failed Proxies',
      message: `Are you sure you want to delete ${failed.length} failed proxy pool${failed.length === 1 ? '' : 's'}? This action cannot be undone.`,
      confirmText: 'Delete Failed',
      onConfirm: async () => {
        confirmState = null
        bulkBusy = true
        let deleted = 0
        let blocked = 0
        try {
          for (const p of failed) {
            try {
              const res = await fetch(`/api/proxy-pools/${encodeURIComponent(p.id)}`, {
                method: 'DELETE',
                headers: getAuthHeaders(),
              })
              if (res.ok) {
                deleted += 1
              } else if (res.status === 409) {
                blocked += 1
              }
            } catch {
              // ignore
            }
          }
          await fetchProxyPools()
          if (blocked > 0) {
            notifications.warning(`Deleted ${deleted} proxies. ${blocked} could not be deleted (bound to active connections).`)
          } else {
            notifications.success(`Deleted ${deleted} failed proxies`)
          }
        } finally {
          bulkBusy = false
        }
      },
    }
  }

  async function handleBatchImport() {
    const lines = batchImportText
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean)

    if (lines.length === 0) {
      notifications.warning('Please paste at least one proxy line.')
      return
    }

    const parsedEntries: { proxyUrl: string; name: string }[] = []
    const invalidLines: string[] = []

    lines.forEach((line, index) => {
      try {
        const parsed = parseProxyLine(line)
        if (parsed) parsedEntries.push(parsed)
      } catch (err) {
        invalidLines.push(`Line ${index + 1}: ${err instanceof Error ? err.message : 'Invalid'}`)
      }
    })

    if (invalidLines.length > 0) {
      notifications.error(`Invalid proxy format:\n${invalidLines.join('\n')}`)
      return
    }

    importing = true
    try {
      const existingKeys = new Set(
        proxyPools.map((pool) => `${(pool.proxyUrl || '').trim()}|||${(pool.noProxy || '').trim()}`)
      )

      let created = 0
      let skipped = 0
      let failed = 0

      for (const entry of parsedEntries) {
        const dedupeKey = `${entry.proxyUrl}|||`
        if (existingKeys.has(dedupeKey)) {
          skipped += 1
          continue
        }
        try {
          await api.createProxyPool({
            name: entry.name,
            proxyUrl: entry.proxyUrl,
            noProxy: '',
            isActive: true,
          })
          created += 1
          existingKeys.add(dedupeKey)
        } catch {
          failed += 1
        }
      }

      await fetchProxyPools()
      showBatchImportModal = false
      notifications.success(
        `Batch import completed: Created ${created}, Skipped ${skipped}, Failed ${failed}`
      )
    } catch (err) {
      console.log('Error batch importing proxies:', err)
      notifications.error('Batch import failed')
    } finally {
      importing = false
    }
  }

  async function handleVercelDeploy() {
    if (!vercelForm.vercelToken.trim()) return
    deploying = true
    try {
      const data = await api.deployVercelRelay({
        vercelToken: vercelForm.vercelToken.trim(),
        projectName: vercelForm.projectName.trim() || undefined,
      })
      await fetchProxyPools()
      closeVercelModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Vercel relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleCloudflareDeploy() {
    if (!cloudflareForm.accountId.trim() || !cloudflareForm.apiToken.trim()) return
    deploying = true
    try {
      const data = await api.deployCloudflareRelay({
        accountId: cloudflareForm.accountId.trim(),
        apiToken: cloudflareForm.apiToken.trim(),
        projectName: cloudflareForm.projectName.trim() || undefined,
      })
      await fetchProxyPools()
      closeCloudflareModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Cloudflare relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleDenoDeploy() {
    if (!denoForm.denoToken.trim() || !denoForm.orgDomain.trim()) return
    deploying = true
    try {
      const data = await api.deployDenoRelay({
        denoToken: denoForm.denoToken.trim(),
        orgDomain: denoForm.orgDomain.trim(),
        projectName: denoForm.projectName.trim() || undefined,
      })
      await fetchProxyPools()
      closeDenoModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Deno relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }
</script>

{#if loading}
  <div class="mx-auto flex w-full max-w-5xl flex-col gap-4 px-1 sm:gap-6 sm:px-0">
    <div class="h-20 rounded-[14px] bg-surface border border-border-subtle animate-pulse"></div>
    <div class="h-64 rounded-[14px] bg-surface border border-border-subtle animate-pulse"></div>
  </div>
{:else}
  <div class="mx-auto flex w-full max-w-5xl flex-col gap-4 px-1 sm:gap-6 sm:px-0">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div class="min-w-0">
        <h1 class="text-xl font-semibold sm:text-2xl">Proxy Pools</h1>
      </div>

      <div class="grid grid-cols-1 gap-2 sm:flex sm:items-center">
        <Button
          size="sm"
          variant="secondary"
          onclick={() => handleHealthCheck(true)}
          disabled={healthChecking || proxyPools.length === 0}
        >
          <span
            class="material-symbols-outlined text-[18px]"
            style={healthChecking ? 'animation: spin 1s linear infinite' : undefined}
          >
            {healthChecking ? 'progress_activity' : 'speed'}
          </span>
          {healthChecking
            ? `Testing ${healthProgress.current}/${healthProgress.total}`
            : 'Test All'}
        </Button>

        <div class="relative" bind:this={relayMenuRef}>
          <Button
            size="sm"
            variant="secondary"
            onclick={() => (showRelayMenu = !showRelayMenu)}
          >
            <span class="material-symbols-outlined text-[18px]">rocket_launch</span>
            Deploy Relay
            <span class="material-symbols-outlined ml-1 text-[18px]">
              {showRelayMenu ? 'expand_less' : 'expand_more'}
            </span>
          </Button>

          {#if showRelayMenu}
            <div
              class="absolute left-0 top-full z-50 mt-1 w-48 rounded-xl border border-black/10 bg-white p-1 shadow-xl dark:border-white/10 dark:bg-zinc-900 sm:left-auto sm:right-0"
            >
              <button
                type="button"
                onclick={() => {
                  openCloudflareModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-orange-500">cloud</span>
                Cloudflare Relay
              </button>
              <button
                type="button"
                onclick={() => {
                  openVercelModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-blue-500">cloud_upload</span>
                Vercel Relay
              </button>
              <button
                type="button"
                onclick={() => {
                  openDenoModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-green-500">terminal</span>
                Deno Relay
              </button>
            </div>
          {/if}
        </div>

        <Button size="sm" variant="secondary" onclick={openBatchImportModal}>
          <span class="material-symbols-outlined text-[18px]">upload</span>
          Batch Import
        </Button>
        <Button size="sm" onclick={openCreateModal}>
          <span class="material-symbols-outlined text-[18px]">add</span>
          Add Proxy Pool
        </Button>
      </div>
    </div>

    <Card>
      <!-- Filter & Sort Controls & Quick Cleanup -->
      <div class="mb-4 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <!-- Filter pills -->
        <div class="flex items-center gap-1.5 flex-wrap">
          <button
            type="button"
            class="px-2.5 py-1 text-xs rounded-lg border transition-colors cursor-pointer {statusFilter === 'all' ? 'border-primary bg-primary/10 text-primary font-medium' : 'border-border text-text-muted hover:border-border-hover hover:text-text-main'}"
            onclick={() => (statusFilter = 'all')}
          >
            All <span class="ml-1 opacity-75">({proxyPools.length})</span>
          </button>
          <button
            type="button"
            class="px-2.5 py-1 text-xs rounded-lg border transition-colors cursor-pointer {statusFilter === 'active' ? 'border-primary bg-primary/10 text-primary font-medium' : 'border-border text-text-muted hover:border-border-hover hover:text-text-main'}"
            onclick={() => (statusFilter = 'active')}
          >
            Active <span class="ml-1 opacity-75">({activeCount})</span>
          </button>
          <button
            type="button"
            class="px-2.5 py-1 text-xs rounded-lg border transition-colors cursor-pointer {statusFilter === 'passed' ? 'border-primary bg-primary/10 text-primary font-medium' : 'border-border text-text-muted hover:border-border-hover hover:text-text-main'}"
            onclick={() => (statusFilter = 'passed')}
          >
            Passed <span class="ml-1 opacity-75">({passedCount})</span>
          </button>
          <button
            type="button"
            class="px-2.5 py-1 text-xs rounded-lg border transition-colors cursor-pointer {statusFilter === 'failed' ? 'border-primary bg-primary/10 text-primary font-medium' : 'border-border text-text-muted hover:border-border-hover hover:text-text-main'}"
            onclick={() => (statusFilter = 'failed')}
          >
            Failed <span class="ml-1 opacity-75">({failedCount})</span>
          </button>
        </div>

        <!-- Sort & Quick Cleanup -->
        <div class="flex items-center gap-2 flex-wrap">
          <div class="flex items-center gap-1.5">
            <span class="text-xs text-text-muted shrink-0">Sort:</span>
            <select
              bind:value={sortOption}
              class="h-8 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors hover:bg-surface-3 cursor-pointer"
              aria-label="Sort proxy pools"
            >
              <option value="default">Default</option>
              <option value="fastest">Fastest (Latency asc)</option>
              <option value="recently_tested">Recently Tested</option>
              <option value="name">Name (A-Z)</option>
            </select>
          </div>

          <Button
            size="sm"
            variant="secondary"
            onclick={handleDisableFailed}
            disabled={failedCount === 0 || bulkBusy || healthChecking}
          >
            <span class="material-symbols-outlined text-[16px]">pause_circle</span>
            Disable Failed
          </Button>

          <Button
            size="sm"
            variant="danger"
            onclick={handleDeleteFailed}
            disabled={failedCount === 0 || bulkBusy || healthChecking}
          >
            <span class="material-symbols-outlined text-[16px]">delete_sweep</span>
            Delete Failed
          </Button>
        </div>
      </div>

      <div class="mb-4 flex flex-wrap items-center justify-between gap-2 border-b border-black/[0.06] pb-3 dark:border-white/[0.06]">
        {#if displayedPools.length > 0}
          <label class="flex items-center gap-1.5 text-xs text-text-muted cursor-pointer">
            <input
              type="checkbox"
              checked={allSelected}
              onchange={toggleSelectAll}
              class="size-4 rounded border-black/20 dark:border-white/20 cursor-pointer"
            />
            {allSelected ? 'Unselect all' : 'Select all'}
          </label>
        {:else}
          <div></div>
        {/if}
        <div class="flex items-center gap-2">
          <Badge>Total: {proxyPools.length}</Badge>
          <Badge variant="success">Active: {activeCount}</Badge>
          {#if failedCount > 0}
            <Badge variant="error">Failed: {failedCount}</Badge>
          {/if}
        </div>
      </div>

      {#if selectedIds.length > 0 || healthChecking}
        <div
          class="mb-4 flex flex-wrap items-center gap-2 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2"
        >
          <span class="material-symbols-outlined text-[18px] text-primary">checklist</span>
          <span class="text-xs font-medium text-primary">
            {selectedIds.length > 0 ? `${selectedIds.length} selected` : 'All pools'}
          </span>
          <div class="ml-auto flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              onclick={() => handleHealthCheck(false)}
              disabled={healthChecking || bulkBusy || proxyPools.length === 0}
            >
              <span
                class="material-symbols-outlined text-[18px]"
                style={healthChecking ? 'animation: spin 1s linear infinite' : undefined}
              >
                {healthChecking ? 'progress_activity' : 'health_and_safety'}
              </span>
              {healthChecking
                ? `Checking ${healthProgress.current}/${healthProgress.total}`
                : 'Health Check'}
            </Button>
            {#if selectedIds.length > 0}
              <Button
                size="sm"
                variant="secondary"
                onclick={() => bulkSetActive(true)}
                disabled={bulkBusy || healthChecking}
              >
                <span class="material-symbols-outlined text-[18px]">toggle_on</span>
                Activate
              </Button>
              <Button
                size="sm"
                variant="secondary"
                onclick={() => bulkSetActive(false)}
                disabled={bulkBusy || healthChecking}
              >
                <span class="material-symbols-outlined text-[18px]">toggle_off</span>
                Deactivate
              </Button>
              <Button
                size="sm"
                variant="danger"
                onclick={bulkDelete}
                disabled={bulkBusy || healthChecking}
              >
                <span class="material-symbols-outlined text-[18px]">delete</span>
                Delete
              </Button>
              <Button size="sm" variant="ghost" onclick={clearSelection} disabled={bulkBusy || healthChecking}>
                Clear
              </Button>
            {/if}
          </div>
        </div>
      {/if}

      {#if proxyPools.length === 0}
        <div class="text-center py-10">
          <p class="text-text-main font-medium mb-1">No proxy pool entries yet</p>
          <p class="text-sm text-text-muted mb-4">
            Create a proxy pool entry, then assign it to connections.
          </p>
          <Button onclick={openCreateModal}>
            <span class="material-symbols-outlined text-[18px]">add</span>
            Add Proxy Pool
          </Button>
        </div>
      {:else if displayedPools.length === 0}
        <div class="text-center py-10">
          <p class="text-text-main font-medium mb-1">No proxy pools match current filter</p>
          <p class="text-sm text-text-muted mb-4">
            Try selecting a different filter option or reset filters.
          </p>
          <Button variant="secondary" onclick={() => (statusFilter = 'all')}>
            Reset Filter
          </Button>
        </div>
      {:else}
        <div class="flex flex-col divide-y divide-black/[0.04] dark:divide-white/[0.05]">
          {#each displayedPools as pool (pool.id)}
            <div class="flex flex-col gap-3 py-3 sm:flex-row sm:items-center sm:justify-between">
              <div class="flex items-start gap-3 min-w-0 flex-1">
                <input
                  type="checkbox"
                  checked={selectedIds.includes(pool.id)}
                  onchange={() => toggleSelect(pool.id)}
                  class="mt-1 size-4 shrink-0 rounded border-black/20 dark:border-white/20 cursor-pointer"
                />
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2 flex-wrap">
                    <p class="min-w-0 max-w-full truncate text-sm font-medium sm:max-w-[18rem]">
                      {pool.name}
                    </p>
                    <Badge variant={getStatusVariant(pool.testStatus)} size="sm" dot>
                      {pool.testStatus || 'unknown'}
                    </Badge>
                    {#if getLatencyBadge(pool.latency)}
                      {@const latencyBadge = getLatencyBadge(pool.latency)}
                      <Badge variant={latencyBadge?.variant} size="sm">
                        {latencyBadge?.text}
                      </Badge>
                    {/if}
                    <Badge variant={pool.isActive ? 'success' : 'default'} size="sm">
                      {pool.isActive ? 'active' : 'inactive'}
                    </Badge>
                    {#if pool.type === 'vercel'}
                      <Badge size="sm">vercel relay</Badge>
                    {/if}
                    {#if pool.type === 'cloudflare'}
                      <Badge size="sm">cloudflare relay</Badge>
                    {/if}
                    <Badge size="sm">
                      {pool.boundConnectionCount || 0} bound
                    </Badge>
                  </div>
                  <p class="text-xs text-text-muted truncate mt-1">{pool.proxyUrl}</p>
                  {#if pool.noProxy}
                    <p class="text-xs text-text-muted truncate">No proxy: {pool.noProxy}</p>
                  {/if}
                  <p class="text-[11px] text-text-muted mt-1">
                    Last tested: {formatDateTime(pool.lastTestedAt)}
                    {pool.lastError ? ` · ${pool.lastError}` : ''}
                  </p>
                </div>
              </div>

              <div class="flex items-center justify-end gap-1">
                <Toggle
                  size="sm"
                  checked={pool.isActive === true}
                  onChange={() => handleToggleActive(pool)}
                  title={pool.isActive ? 'Disable' : 'Enable'}
                />
                <button
                  type="button"
                  onclick={() => handleTest(pool.id)}
                  class="p-2 rounded hover:bg-black/5 dark:hover:bg-white/5 text-text-muted hover:text-primary cursor-pointer disabled:opacity-50"
                  title="Test proxy"
                  disabled={testingId === pool.id}
                >
                  <span
                    class="material-symbols-outlined text-[18px]"
                    style={testingId === pool.id ? 'animation: spin 1s linear infinite' : undefined}
                  >
                    {testingId === pool.id ? 'progress_activity' : 'science'}
                  </span>
                </button>
                <button
                  type="button"
                  onclick={() => openEditModal(pool)}
                  class="p-2 rounded hover:bg-black/5 dark:hover:bg-white/5 text-text-muted hover:text-primary cursor-pointer"
                  title="Edit"
                >
                  <span class="material-symbols-outlined text-[18px]">edit</span>
                </button>
                <button
                  type="button"
                  onclick={() => handleDelete(pool)}
                  class="p-2 rounded hover:bg-red-500/10 text-red-500 cursor-pointer"
                  title="Delete"
                >
                  <span class="material-symbols-outlined text-[18px]">delete</span>
                </button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </Card>

    <Modal
      isOpen={showBatchImportModal}
      title="Batch Import Proxies"
      onClose={closeBatchImportModal}
    >
      <div class="flex flex-col gap-4">
        <div>
          <label class="text-sm font-medium text-text-main mb-1 block">
            Paste Proxy List (One per line)
          </label>
          <textarea
            bind:value={batchImportText}
            placeholder={'http://user:pass@127.0.0.1:7897\n127.0.0.1:7897:user:pass'}
            class="w-full min-h-[180px] py-2 px-3 text-sm text-text-main bg-surface-2 border border-transparent rounded-[10px] focus:ring-1 focus:ring-primary/30 focus:border-primary/50 focus:outline-none transition-all font-mono"
          ></textarea>
          <p class="text-xs text-text-muted mt-1">
            Supported formats: protocol://user:pass@host:port, host:port:user:pass
          </p>
        </div>

        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button fullWidth onclick={handleBatchImport} disabled={!batchImportText.trim() || importing}>
            {importing ? 'Importing...' : 'Import'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeBatchImportModal} disabled={importing}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal isOpen={showVercelModal} title="Deploy Vercel Relay" onClose={closeVercelModal}>
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-blue-500/5 border border-blue-500/10 p-3 flex flex-col gap-1.5">
          <p class="text-sm text-text-main font-medium">What is Vercel Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys an edge relay function to Vercel. All AI provider requests will be forwarded
            through Vercel's edge network, masking your real IP from providers.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>
              Your IP is replaced by Vercel's dynamic edge IPs (hundreds of IPs across 20+ global
              regions)
            </li>
            <li>
              Vercel serves millions of apps — providers can't block Vercel IPs without affecting
              legitimate traffic
            </li>
            <li>Free tier: 100GB bandwidth/month, 500K edge invocations</li>
            <li>Deploy multiple relays on different accounts for more IP diversity</li>
          </ul>
        </div>
        <div>
          <Input
            label="Vercel API Token"
            type="password"
            bind:value={vercelForm.vercelToken}
            placeholder="your-vercel-api-token"
          />
          <p class="text-xs text-text-muted mt-1">
            Token is used once for deployment and not stored.
            <a
              href="https://vercel.com/account/tokens"
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary hover:underline"
            >
              Get token →
            </a>
          </p>
        </div>
        <Input
          label="Project Name"
          bind:value={vercelForm.projectName}
          placeholder="my-relay"
          hint="Unique name for your Vercel project. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleVercelDeploy}
            disabled={!vercelForm.vercelToken.trim() || deploying}
          >
            {deploying ? 'Deploying... (may take ~1 min)' : 'Deploy'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeVercelModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showCloudflareModal}
      title="Deploy Cloudflare Relay"
      onClose={closeCloudflareModal}
    >
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-orange-500/5 border border-orange-500/10 p-3 flex flex-col gap-1.5">
          <p class="text-sm text-text-main font-medium">What is Cloudflare Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a Cloudflare Worker as a proxy relay. All AI provider requests will be forwarded
            through Cloudflare's global edge network.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>High performance global routing and IP masking via Cloudflare Workers</li>
            <li>Free tier: 100,000 requests per day</li>
            <li>Requires Cloudflare Account ID and a Workers API Token (Edit Workers permission)</li>
          </ul>
          <div class="mt-2 pt-2 border-t border-orange-500/10 text-xs text-text-muted">
            <p class="font-medium text-text-main mb-1">How to generate your API Token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>My Profile</b> → <b>API Tokens</b> → <b>Create Token</b></li>
              <li>Scroll down to <b>Custom Token</b> and click <b>Get started</b></li>
              <li>Under <b>Permissions</b>: Account | Workers Scripts | Edit</li>
              <li>
                Under <b>Account Resources</b>: Include | Account | <i>Your Account Name</i>
              </li>
              <li>Click <b>Continue to summary</b> → <b>Create Token</b></li>
            </ol>
          </div>
        </div>
        <Input
          label="Account ID"
          bind:value={cloudflareForm.accountId}
          placeholder="your-cloudflare-account-id"
          hint="Found on the right side of the Cloudflare dashboard overview page."
        />
        <div>
          <Input
            label="API Token"
            type="password"
            bind:value={cloudflareForm.apiToken}
            placeholder="your-cloudflare-api-token"
          />
          <p class="text-xs text-text-muted mt-1">
            Requires "Workers Scripts: Edit" permission.
            <a
              href="https://dash.cloudflare.com/profile/api-tokens"
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary hover:underline"
            >
              Get token →
            </a>
          </p>
        </div>
        <Input
          label="Worker Name"
          bind:value={cloudflareForm.projectName}
          placeholder="my-relay"
          hint="Unique name for your Cloudflare Worker. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleCloudflareDeploy}
            disabled={!cloudflareForm.accountId.trim() ||
              !cloudflareForm.apiToken.trim() ||
              deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Worker'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeCloudflareModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal isOpen={showDenoModal} title="Deploy Deno Relay" onClose={closeDenoModal}>
      <div class="flex flex-col gap-4">
        <div
          class="rounded-lg bg-black/5 dark:bg-white/5 border border-black/10 dark:border-white/10 p-3 flex flex-col gap-1.5"
        >
          <p class="text-sm text-text-main font-medium">What is Deno Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a relay worker to Deno Deploy's global edge network. All AI provider requests
            are forwarded through Deno's edge, masking your real IP.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>Deno Deploy v2 runs on a high-performance global edge network</li>
            <li>Free tier: 1M requests & 100GiB outbound traffic per month</li>
            <li>No per-request CPU time limits (unlike Vercel/Cloudflare)</li>
            <li>Support up to 20 active apps & 50 custom domains</li>
            <li>Deploy multiple relays for maximum IP diversity</li>
          </ul>
          <div
            class="mt-2 pt-2 border-t border-black/10 dark:border-white/10 text-xs text-text-muted"
          >
            <p class="font-medium text-text-main mb-1">How to generate API token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>console.deno.com</b></li>
              <li>Select your <b>Organization</b> → <b>Settings</b> → <b>Organization Tokens</b></li>
              <li>Create a <b>Organization Token</b> (prefix <b>ddo_</b>)</li>
            </ol>
          </div>
        </div>
        <div>
          <Input
            label="Deno Deploy API Token"
            type="password"
            bind:value={denoForm.denoToken}
            placeholder="ddo_xxxxxxxxxxxxxxxx"
          />
          <p class="text-xs text-text-muted mt-1">
            Token is used once for deployment, not stored. Found in Organization Settings.
          </p>
        </div>
        <Input
          label="Organization Domain"
          bind:value={denoForm.orgDomain}
          placeholder="your-org.deno.net"
          hint="Organization's default domain. Your relay URL will be in the format: https://my-relay.your-org.deno.net"
        />
        <Input
          label="App Name"
          bind:value={denoForm.projectName}
          placeholder="deno-relay"
          hint="Unique app name. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleDenoDeploy}
            disabled={!denoForm.denoToken.trim() || !denoForm.orgDomain.trim() || deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Relay'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeDenoModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showFormModal}
      title={editingPool ? 'Edit Proxy Pool' : 'Add Proxy Pool'}
      onClose={closeFormModal}
    >
      <div class="flex flex-col gap-4">
        <Input label="Name" bind:value={formData.name} placeholder="Office Proxy" />
        <Input label="Proxy URL" bind:value={formData.proxyUrl} placeholder="http://127.0.0.1:7897" />
        <Input
          label="No Proxy"
          bind:value={formData.noProxy}
          placeholder="localhost,127.0.0.1,.internal"
          hint="Comma-separated hosts/domains to bypass proxy"
        />

        <div
          class="flex flex-col gap-3 rounded-lg border border-border/50 p-3 sm:flex-row sm:items-center sm:justify-between"
        >
          <div>
            <p class="font-medium text-sm">Active</p>
            <p class="text-xs text-text-muted">Inactive pools are ignored by runtime resolution.</p>
          </div>
          <Toggle
            checked={formData.isActive === true}
            onChange={() => (formData.isActive = !formData.isActive)}
            disabled={saving}
          />
        </div>

        <div
          class="flex flex-col gap-3 rounded-lg border border-border/50 p-3 sm:flex-row sm:items-center sm:justify-between"
        >
          <div>
            <p class="font-medium text-sm">Strict Proxy</p>
            <p class="text-xs text-text-muted">
              Fail request if proxy is unreachable instead of falling back to direct.
            </p>
          </div>
          <Toggle
            checked={formData.strictProxy === true}
            onChange={() => (formData.strictProxy = !formData.strictProxy)}
            disabled={saving}
          />
        </div>

        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleSave}
            disabled={!formData.name.trim() || !formData.proxyUrl.trim() || saving}
          >
            {saving ? 'Saving...' : 'Save'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeFormModal} disabled={saving}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={!!confirmState}
      title={confirmState?.title || 'Confirm'}
      size="sm"
      onClose={() => (confirmState = null)}
    >
      {#snippet footer()}
        <Button variant="ghost" onclick={() => (confirmState = null)}>Cancel</Button>
        <Button variant="danger" onclick={() => confirmState?.onConfirm()}>
          {confirmState?.confirmText || 'Confirm'}
        </Button>
      {/snippet}
      <p class="text-text-muted whitespace-pre-wrap">{confirmState?.message}</p>
    </Modal>
  </div>
{/if}
