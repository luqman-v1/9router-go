import { describe, expect, it } from 'bun:test'
import {
  PING_SCHEDULE_DEFAULTS,
  coverageEndLabel,
  isScheduleFormValid,
  maxSlotsPerDay,
  parseAnchor,
  scheduleSlotLabels,
  validateScheduleForm
} from './pingschedule'

// These pin the Go side's arithmetic (internal/schedule/pingschedule.go). The
// preview and the scheduler must agree on every slot: a preview that shows
// 09:30/14:30/19:24 while the scheduler fires 09:30/14:30/19:25 is the defect
// the rounding comment at SlotLabels records.

describe('parseAnchor', () => {
  it('accepts HH:MM and H:MM', () => {
    expect(parseAnchor('09:30')).toEqual({ hour: 9, minute: 30 })
    expect(parseAnchor('9:30')).toEqual({ hour: 9, minute: 30 })
    expect(parseAnchor(' 0:00 ')).toEqual({ hour: 0, minute: 0 })
    expect(parseAnchor('23:59')).toEqual({ hour: 23, minute: 59 })
  })

  it('rejects a field that is not a clock time', () => {
    for (const bad of ['', '24:00', '12:60', '9', '09:3', '09:300', 'noon', '09:30:00', null]) {
      expect(parseAnchor(bad)).toBeNull()
    }
  })
})

describe('maxSlotsPerDay', () => {
  it('divides a day by the window', () => {
    expect(maxSlotsPerDay(5)).toBe(4)
    expect(maxSlotsPerDay(1)).toBe(24)
    expect(maxSlotsPerDay(24)).toBe(1)
  })

  it('keeps at least one slot when the window is degenerate', () => {
    expect(maxSlotsPerDay(0)).toBe(1)
    expect(maxSlotsPerDay(48)).toBe(1)
    expect(maxSlotsPerDay(Number.NaN)).toBe(1)
  })
})

describe('scheduleSlotLabels', () => {
  it('renders the default schedule', () => {
    expect(scheduleSlotLabels({ ...PING_SCHEDULE_DEFAULTS })).toEqual([
      '09:30',
      '14:30',
      '19:30'
    ])
  })

  it('matches the Go SlotLabels preview for a fractional window', () => {
    // 09:30 + 2 * 5.1h = 1182 minutes. The rounding matters: upstream rounds
    // before the modulo, and truncating instead would print 19:41 for a slot
    // the scheduler fires at 19:42. The Go side was run against this exact
    // case; both answer [09:30 14:36 19:42].
    expect(scheduleSlotLabels({ anchor: '09:30', windowHours: 5.1, slotsPerDay: 3 })).toEqual([
      '09:30',
      '14:36',
      '19:42'
    ])
  })

  it('wraps past midnight on the next day', () => {
    expect(scheduleSlotLabels({ anchor: '22:00', windowHours: 5, slotsPerDay: 3 })).toEqual([
      '22:00',
      '03:00',
      '08:00'
    ])
  })

  it('clamps the count to what the window allows', () => {
    // A 12h window fits two slots; asking for five must not print five.
    expect(scheduleSlotLabels({ anchor: '00:00', windowHours: 12, slotsPerDay: 5 })).toEqual([
      '00:00',
      '12:00'
    ])
  })

  it('falls back to the default anchor rather than returning nothing', () => {
    expect(scheduleSlotLabels({ anchor: 'bogus', windowHours: 5, slotsPerDay: 2 })).toEqual([
      '09:30',
      '14:30'
    ])
  })
})

describe('coverageEndLabel', () => {
  it('measures from the anchor through the last slot window', () => {
    // 09:30 + 3 * 5h = 00:30 the following day.
    expect(coverageEndLabel({ ...PING_SCHEDULE_DEFAULTS })).toBe('00:30')
  })

  it('agrees with the label count when the count is clamped', () => {
    expect(coverageEndLabel({ anchor: '00:00', windowHours: 12, slotsPerDay: 5 })).toBe('00:00')
  })
})

describe('validateScheduleForm', () => {
  it('passes the defaults', () => {
    const errors = validateScheduleForm({ ...PING_SCHEDULE_DEFAULTS })
    expect(errors).toEqual({ anchor: '', windowHours: '', slotsPerDay: '' })
    expect(isScheduleFormValid(errors)).toBe(true)
  })

  it('names the bound it violated', () => {
    expect(validateScheduleForm({ anchor: 'x', windowHours: 5, slotsPerDay: 3 }).anchor).toBe(
      'Use HH:MM'
    )
    expect(validateScheduleForm({ anchor: '09:30', windowHours: 0.1, slotsPerDay: 3 }).windowHours)
      .toBe('0.5 – 24')
    expect(validateScheduleForm({ anchor: '09:30', windowHours: 5, slotsPerDay: 9 }).slotsPerDay)
      .toBe('1 – 4')
  })

  it('rejects a fractional slot count', () => {
    expect(validateScheduleForm({ anchor: '09:30', windowHours: 5, slotsPerDay: 2.5 }).slotsPerDay)
      .toBe('1 – 4')
  })
})
