import { describe, expect, it } from 'bun:test'
import { navBadgeFor, type NavBadgeInput } from './navBadges'

const base: NavBadgeInput = {
  totalConnections: 0,
  activeConnections: 0,
  comboCount: 0,
  apiKeyCount: 0,
  failingConnections: 0,
}

describe('sidebar badges', () => {
  it('says nothing for an entry with no fact worth the space', () => {
    // Most entries are a page the operator opens on purpose; a number beside
    // them would only restate the label.
    expect(navBadgeFor('quota', base)).toBeNull()
    expect(navBadgeFor('settings', base)).toBeNull()
    expect(navBadgeFor('media-tts', base)).toBeNull()
  })

  it('shows connections as a fraction, never as a bare total', () => {
    const badge = navBadgeFor('connections', {
      ...base,
      totalConnections: 12,
      activeConnections: 3,
    })
    expect(badge?.text).toBe('3 / 12')
    expect(badge?.tone).toBe('muted')
    expect(badge?.title).toBe('3 of 12 connections active')
  })

  it('stays quiet when every connection is active', () => {
    // "12 / 12 active" is the healthy state, and saying so is noise.
    expect(
      navBadgeFor('connections', { ...base, totalConnections: 12, activeConnections: 12 })
    ).toBeNull()
  })

  it('warns when a connection is failing, ahead of the fraction', () => {
    // A failing connection is the one number that earns the warning colour:
    // the fleet is degraded and the operator has not opened the page yet.
    const badge = navBadgeFor('connections', {
      ...base,
      totalConnections: 12,
      activeConnections: 3,
      failingConnections: 2,
    })
    expect(badge?.text).toBe('2')
    expect(badge?.tone).toBe('warn')
    expect(badge?.title).toBe('2 connections failing their last check')
  })

  it('uses the singular for one failing connection', () => {
    const badge = navBadgeFor('connections', { ...base, failingConnections: 1 })
    expect(badge?.title).toBe('1 connection failing their last check')
  })

  it('shows a combo count only while the list is short', () => {
    expect(navBadgeFor('combos', { ...base, comboCount: 4 })?.text).toBe('4')
    // Past twenty the number is slower to read than opening the page.
    expect(navBadgeFor('combos', { ...base, comboCount: 21 })).toBeNull()
    expect(navBadgeFor('combos', { ...base, comboCount: 0 })).toBeNull()
  })

  it('shows an API key count only while the list is short', () => {
    expect(navBadgeFor('endpoint', { ...base, apiKeyCount: 2 })?.text).toBe('2')
    expect(navBadgeFor('endpoint', { ...base, apiKeyCount: 25 })).toBeNull()
  })

  it('caps a number that would otherwise wrap the row', () => {
    const badge = navBadgeFor('connections', {
      ...base,
      totalConnections: 5000,
      activeConnections: 1,
    })
    expect(badge?.text).toBe('1 / 999+')
  })
})
