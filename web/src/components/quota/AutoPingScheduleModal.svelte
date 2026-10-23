<script lang="ts">
  // Port of 9router src/app/(dashboard)/dashboard/providers/[id]/
  // AutoPingScheduleModal.js (Next.js) to the Go dashboard's Svelte components.
  //
  // One control, three modes, because the settings key has two independent
  // halves: `connections` is the reactive "ping on reset" switch and
  // `schedules` is the anchored time list. Upstream expresses the same thing as
  // a radio group, and the Go handler merges whichever half arrives
  // (internal/handlers/dashboard/settings_autoping.go), so a modal that only
  // wrote `schedules` would leave the switch untouched and vice versa.
  import Modal from '../../lib/ui/Modal.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import {
    PING_SCHEDULE_DEFAULTS,
    coverageEndLabel,
    isScheduleFormValid,
    maxSlotsPerDay,
    scheduleSlotLabels,
    validateScheduleForm
  } from './pingschedule'

  export interface AutoPingSchedule {
    enabled: boolean
    anchor: string
    timezone: string
    windowHours: number
    slotsPerDay: number
  }

  export interface AutoPingSaveResult {
    reactive: boolean
    schedule: { enabled: false } | AutoPingSchedule
  }

  let {
    open = false,
    connectionName = '',
    reactive = false,
    schedule = null,
    timezone,
    onClose,
    onSave
  }: {
    open?: boolean
    connectionName?: string
    reactive?: boolean
    schedule?: Partial<AutoPingSchedule> | null
    /** The operator's own zone, resolved by the caller from the browser. */
    timezone: string
    onClose: () => void
    onSave: (result: AutoPingSaveResult) => void
  } = $props()

  type Mode = 'off' | 'reset' | 'schedule'

  const MODE_OPTIONS: { value: Mode; label: string; icon: string; hint: string }[] = [
    { value: 'off', label: 'Off', icon: 'block', hint: 'No automatic pings.' },
    {
      value: 'reset',
      label: 'On reset',
      icon: 'bolt',
      hint: 'Pings right after the 5h quota window resets, so a new window starts immediately.'
    },
    {
      value: 'schedule',
      label: 'Schedule',
      icon: 'schedule',
      hint: 'Pings at fixed local times so the 5h windows align with your workday.'
    }
  ]

  // Seeded once per mount from the stored values, with the same defaults the Go
  // side applies so an empty schedule opens on a usable form rather than zeros.
  let mode = $state<Mode>(
    schedule?.enabled === true ? 'schedule' : reactive === true ? 'reset' : 'off'
  )
  let anchor = $state(schedule?.anchor || PING_SCHEDULE_DEFAULTS.anchor)
  let windowHours = $state(String(schedule?.windowHours ?? PING_SCHEDULE_DEFAULTS.windowHours))
  let slotsPerDay = $state(String(schedule?.slotsPerDay ?? PING_SCHEDULE_DEFAULTS.slotsPerDay))

  const parsedWindow = $derived(Number(windowHours))
  const parsedSlots = $derived(Number(slotsPerDay))
  const errors = $derived(
    validateScheduleForm({ anchor, windowHours: parsedWindow, slotsPerDay: parsedSlots })
  )
  const formValid = $derived(isScheduleFormValid(errors))
  const slotsMax = $derived(
    Number.isFinite(parsedWindow) && parsedWindow > 0 ? maxSlotsPerDay(parsedWindow) : 24
  )
  const previewSlots = $derived(
    formValid
      ? scheduleSlotLabels({
          anchor,
          windowHours: parsedWindow,
          slotsPerDay: parsedSlots
        })
      : []
  )
  const coverageEnd = $derived(
    formValid
      ? coverageEndLabel({ anchor, windowHours: parsedWindow, slotsPerDay: parsedSlots })
      : ''
  )
  const canSave = $derived(mode !== 'schedule' || formValid)
  const activeHint = $derived(MODE_OPTIONS.find((option) => option.value === mode)?.hint ?? '')

  function handleSave() {
    if (!canSave) return
    onSave({
      reactive: mode === 'reset',
      schedule:
        mode === 'schedule'
          ? {
              enabled: true,
              anchor,
              timezone,
              windowHours: parsedWindow,
              slotsPerDay: parsedSlots
            }
          : { enabled: false }
    })
  }
</script>

<Modal isOpen={open} title="Auto-ping settings" size="lg" onClose={onClose}>
  <div class="flex flex-col gap-4">
    <p class="text-sm text-text-muted">
      Configure automated quota pings{connectionName ? ` for ${connectionName}` : ''} — pick one
      mode.
    </p>

    <div class="grid grid-cols-3 gap-1 rounded-[10px] bg-surface-2 p-1">
      {#each MODE_OPTIONS as option (option.value)}
        {@const isSelected = mode === option.value}
        <button
          type="button"
          onclick={() => (mode = option.value)}
          aria-pressed={isSelected}
          class="flex h-9 items-center justify-center gap-1.5 rounded-[8px] px-3 text-sm font-medium transition-all cursor-pointer {isSelected
            ? 'bg-surface text-text-main shadow-sm'
            : 'text-text-muted hover:text-text-main'}"
        >
          <span class="material-symbols-outlined text-[15px] leading-none">{option.icon}</span>
          <span>{option.label}</span>
        </button>
      {/each}
    </div>

    {#if activeHint}
      <p class="text-xs text-text-muted">{activeHint}</p>
    {/if}

    {#if mode === 'schedule'}
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Input
          label="Anchor time"
          id="auto-ping-anchor"
          type="time"
          bind:value={anchor}
          error={errors.anchor}
          inputClass="ring-1 ring-black/10 dark:ring-white/15"
        />
        <Input
          label="Window (hours)"
          id="auto-ping-window"
          type="number"
          min="0.5"
          max="24"
          bind:value={windowHours}
          error={errors.windowHours}
          inputClass="ring-1 ring-black/10 dark:ring-white/15"
        />
        <Input
          label="Pings per day"
          id="auto-ping-slots"
          type="number"
          min="1"
          max={String(slotsMax)}
          bind:value={slotsPerDay}
          error={errors.slotsPerDay}
          hint={errors.slotsPerDay ? '' : `Max ${slotsMax}`}
          inputClass="ring-1 ring-black/10 dark:ring-white/15"
        />
      </div>

      {#if previewSlots.length > 0}
        <div class="rounded-[10px] bg-surface-2 px-3 py-2.5">
          <p class="text-xs font-medium uppercase tracking-wide text-text-muted">
            Daily slots ({timezone})
          </p>
          <p class="mt-1 text-sm font-medium text-text-main">{previewSlots.join('  →  ')}</p>
          <p class="mt-1 text-xs text-text-muted">
            Covers {previewSlots[0]} – {coverageEnd}, then pauses until {previewSlots[0]} next day.
          </p>
        </div>
      {/if}
    {/if}
  </div>

  {#snippet footer()}
    <div class="flex justify-end gap-2">
      <button
        type="button"
        onclick={onClose}
        class="rounded-lg border border-border-subtle px-3 py-1.5 text-sm font-medium text-text-main transition-colors hover:bg-surface-2 cursor-pointer"
      >
        Cancel
      </button>
      <button
        type="button"
        onclick={handleSave}
        disabled={!canSave}
        class="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
      >
        Save
      </button>
    </div>
  {/snippet}
</Modal>
