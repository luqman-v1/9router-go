// Buy-key presets only pre-fill fields the existing create + policy routes
// already write. What is worth pinning is the part an operator can get wrong:
// that a preset carries a restriction at all, that an unknown id degrades to the
// default instead of producing an unrestricted key, and that the stamped expiry
// is 30 days out rather than "now" (a key that expires the moment it is minted).
import { describe, expect, it } from 'bun:test'
import {
  applyBuyerPreset,
  BUYER_PRESETS,
  buyerPresetExpiry,
  DEFAULT_BUYER_PRESET_ID,
  findBuyerPreset,
} from './buyerKeyPresets'

const NOW = new Date('2026-11-19T12:00:00.000Z')

describe('applyBuyerPreset', () => {
  it('restricts the models a buyer may call', () => {
    const draft = applyBuyerPreset(DEFAULT_BUYER_PRESET_ID, 'buyer-acme', NOW)
    // An empty allowlist means allow-all, so a preset that sold "chat only"
    // must not be allowed to produce one.
    expect(draft.models.length).toBeGreaterThan(0)
  })

  it('caps the spend and stops serving once the cap is spent', () => {
    const draft = applyBuyerPreset('buyer-chat-monthly', 'buyer-acme', NOW)
    expect(draft.limitMicros).toBe(10_000_000)
    expect(draft.hardCutoff).toBe(true)
  })

  it('leaves the cap off for the trusted-buyer shape', () => {
    const draft = applyBuyerPreset('buyer-chat-unlimited', 'buyer-acme', NOW)
    expect(draft.limitMicros).toBe(0)
    expect(draft.hardCutoff).toBe(false)
  })

  it('allows every model for the full-access shape', () => {
    const draft = applyBuyerPreset('buyer-full-30d', 'buyer-acme', NOW)
    expect(draft.models).toEqual([])
  })

  it('trims the buyer name so a stray space is not part of the label', () => {
    const draft = applyBuyerPreset(DEFAULT_BUYER_PRESET_ID, '  buyer-acme  ', NOW)
    expect(draft.name).toBe('buyer-acme')
  })

  it('copies the allowlist rather than aliasing the preset', () => {
    const first = applyBuyerPreset(DEFAULT_BUYER_PRESET_ID, 'b', NOW)
    first.models.push('mutated')
    const second = applyBuyerPreset(DEFAULT_BUYER_PRESET_ID, 'b', NOW)
    expect(second.models).not.toContain('mutated')
  })

  it('falls back to the default preset instead of minting an unrestricted key', () => {
    const draft = applyBuyerPreset('preset-that-no-longer-exists', 'buyer-acme', NOW)
    expect(draft.presetId).toBe(DEFAULT_BUYER_PRESET_ID)
    expect(draft.models.length).toBeGreaterThan(0)
  })
})

describe('buyerPresetExpiry', () => {
  it('stamps 30 days out, so the key survives its first day', () => {
    const expiry = buyerPresetExpiry(NOW)
    const days = (new Date(expiry).getTime() - NOW.getTime()) / 86_400_000
    expect(Math.round(days)).toBe(30)
  })

  it('survives a month boundary instead of overflowing into March', () => {
    // 2026-12-31 + 30 days is 2027-01-30; a naive month-add would give Jan 31.
    const expiry = buyerPresetExpiry(new Date('2026-12-31T12:00:00.000Z'))
    expect(expiry).toContain('2027-01-30')
  })
})

describe('preset catalogue', () => {
  it('has unique ids, so a change to one cannot be read as a change to another', () => {
    const ids = BUYER_PRESETS.map((p) => p.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('describes every preset, so the picker never shows a bare label', () => {
    for (const preset of BUYER_PRESETS) {
      expect(preset.description.length).toBeGreaterThan(0)
    }
  })

  it('resolves each preset by id', () => {
    for (const preset of BUYER_PRESETS) {
      expect(findBuyerPreset(preset.id)?.id).toBe(preset.id)
    }
  })

  it('returns undefined for an unknown id, which is what the fallback guards on', () => {
    expect(findBuyerPreset('nope')).toBeUndefined()
  })
})