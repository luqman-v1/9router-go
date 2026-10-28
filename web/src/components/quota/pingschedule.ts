// Port of src/shared/services/quotaPingSchedule.js for the native Go dashboard.
//
// Pure helpers only: the schedule editor renders these and the numbers it sends
// back, and the Go side re-validates every one of them on the way in
// (internal/schedule/pingschedule.go). The dashboard is not the trust boundary —
// this file exists so the modal can preview the slots and refuse an obviously
// bad value before the round trip, not so the server can skip checking.

export const PING_SCHEDULE_DEFAULTS = {
  anchor: '09:30',
  windowHours: 5,
  slotsPerDay: 3
} as const

export const MIN_WINDOW_HOURS = 0.5
export const MAX_WINDOW_HOURS = 24

export interface ParsedAnchor {
  hour: number
  minute: number
}

/** Parses "HH:MM" (leading zero optional). null when it is not a clock time. */
export function parseAnchor(anchor: unknown): ParsedAnchor | null {
  const match = /^(\d{1,2}):(\d{2})$/.exec(String(anchor ?? '').trim())
  if (!match) return null
  const hour = Number(match[1])
  const minute = Number(match[2])
  if (hour > 23 || minute > 59) return null
  return { hour, minute }
}

/**
 * How many slots one day can hold for a window. Mirrors MaxSlotsPerDay on the Go
 * side: a 5h window fits 4, and the floor keeps a 24h window at one rather than
 * zero.
 */
export function maxSlotsPerDay(windowHours: number): number {
  if (!Number.isFinite(windowHours) || windowHours <= 0) return 1
  return Math.max(1, Math.floor(24 / windowHours))
}

/** "HH:MM" for a count of minutes from midnight, wrapping past 24h. */
function clockLabel(totalMinutes: number): string {
  const wrapped = ((Math.round(totalMinutes) % 1440) + 1440) % 1440
  const hour = Math.floor(wrapped / 60)
  const minute = wrapped % 60
  return `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`
}

/**
 * The "09:30 → 14:30 → 19:30" preview.
 *
 * Rounds before the modulo, as upstream does (Math.round(startMinutes + index *
 * windowHours * 60) % 1440): a 5.1h window puts the third slot at
 * 917.9999999999998 minutes, which truncates to 917 and rounds to 918 — a minute
 * of drift the preview must not show differently from the server.
 */
export function scheduleSlotLabels(schedule: {
  anchor: string
  windowHours: number
  slotsPerDay: number
}): string[] {
  const anchor = parseAnchor(schedule.anchor) ?? parseAnchor(PING_SCHEDULE_DEFAULTS.anchor)
  if (!anchor) return []
  const start = anchor.hour * 60 + anchor.minute
  const max = maxSlotsPerDay(schedule.windowHours)
  const count = Math.max(1, Math.min(schedule.slotsPerDay, max))
  const labels: string[] = []
  for (let index = 0; index < count; index += 1) {
    labels.push(clockLabel(start + index * schedule.windowHours * 60))
  }
  return labels
}

/**
 * When the last slot's window closes — the moment coverage stops until the next
 * anchor. Empty when there is no usable slot to measure from.
 */
export function coverageEndLabel(schedule: {
  anchor: string
  windowHours: number
  slotsPerDay: number
}): string {
  const labels = scheduleSlotLabels(schedule)
  if (labels.length === 0) return ''
  const anchor = parseAnchor(schedule.anchor) ?? parseAnchor(PING_SCHEDULE_DEFAULTS.anchor)
  if (!anchor) return ''
  const max = maxSlotsPerDay(schedule.windowHours)
  const count = Math.max(1, Math.min(schedule.slotsPerDay, max))
  return clockLabel(anchor.hour * 60 + anchor.minute + count * schedule.windowHours * 60)
}

export interface ScheduleFormErrors {
  anchor: string
  windowHours: string
  slotsPerDay: string
}

/**
 * Field-level validation for the editor. The messages are what the modal shows
 * under each field, so they name the bound rather than saying "invalid".
 */
export function validateScheduleForm(form: {
  anchor: string
  windowHours: number
  slotsPerDay: number
}): ScheduleFormErrors {
  const anchorOk = parseAnchor(form.anchor) !== null
  const windowOk =
    Number.isFinite(form.windowHours) &&
    form.windowHours >= MIN_WINDOW_HOURS &&
    form.windowHours <= MAX_WINDOW_HOURS
  const slotsMax = windowOk ? maxSlotsPerDay(form.windowHours) : 24
  const slotsOk =
    Number.isInteger(form.slotsPerDay) && form.slotsPerDay >= 1 && form.slotsPerDay <= slotsMax
  return {
    anchor: anchorOk ? '' : 'Use HH:MM',
    windowHours: windowOk ? '' : `${MIN_WINDOW_HOURS} – ${MAX_WINDOW_HOURS}`,
    slotsPerDay: slotsOk ? '' : `1 – ${slotsMax}`
  }
}

export function isScheduleFormValid(errors: ScheduleFormErrors): boolean {
  return errors.anchor === '' && errors.windowHours === '' && errors.slotsPerDay === ''
}
