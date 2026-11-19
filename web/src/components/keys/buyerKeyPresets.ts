// Buy-key presets: the small set of resale shapes an operator actually repeats.
//
// The dashboard already exposes every field a buyer key needs — allowlist,
// budget, hard cutoff, expiry — but as separate form controls, so handing a
// key to one buyer is a dozen clicks and an easy place to misconfigure. A
// preset turns that into one choice at mint time. It is NOT a second policy
// mechanism: a preset only pre-fills fields the existing POST /api/keys +
// policy routes write, and every field stays editable afterwards.
//
// Money is in micro-dollars throughout, matching limitMicros in the API.

export interface BuyerPreset {
  id: string
  /** Shown in the picker. */
  label: string
  description: string
  /** Model allowlist patterns. Empty means "allow all", which is the same
   *  meaning an empty allowlist carries everywhere else in the gateway. */
  models: string[]
  /** Monthly spend limit in micro-dollars. 0 means no limit. */
  limitMicros: number
  /** Lifetime token limit. 0 means no limit. */
  limitTokens: number
  /** Requests per minute. 0 means unlimited. */
  rpm: number
  /** True when the preset should stop serving once the budget is spent. */
  hardCutoff: boolean
  /** The resale bookkeeping stamped onto the key, in the validated vocabulary. */
  metadata: BuyerPresetMetadata
}

/** One month of calendar days, used to date the default expiry. */
const MONTH_DAYS = 30

/** The metadata vocabulary apiKeys.metadata already accepts
 *  (internal/handlers/dashboard/apikeys_metadata_validate.go). An unknown key
 *  is refused at the write, so a preset that invents one fails the whole policy
 *  request instead of storing its label. */
export interface BuyerPresetMetadata {
  /** Which preset stamped this key, for the operator's bookkeeping. */
  notes: string
  /** Price in CENTS, the unit apiKeys.metadata validates. */
  priceCents: number
  /** One of the accepted cycles: one-off | monthly | quarterly | yearly. */
  billingCycle: 'one-off' | 'monthly' | 'quarterly' | 'yearly'
}

/** Presets are ordered by how much they restrict, tightest first. */
export const BUYER_PRESETS: readonly BuyerPreset[] = [
  {
    id: 'buyer-chat-monthly',
    label: 'Buyer — chat, $10/month',
    description: 'Chat models only, $10 per month, hard stop at the cap, key expires in 30 days.',
    models: ['*/deepseek-chat', '*/gpt-5.5-mini', '*/claude-sonnet*'],
    limitMicros: 10_000_000,
    limitTokens: 0,
    rpm: 60,
    hardCutoff: true,
    metadata: { notes: 'chat-only, $10/month', priceCents: 1000, billingCycle: 'monthly' },
  },
  {
    id: 'buyer-chat-unlimited',
    label: 'Buyer — chat, no cap',
    description: 'Chat models only, no spend or token cap. Use for a trusted buyer or a trial.',
    models: ['*/deepseek-chat', '*/gpt-5.5-mini', '*/claude-sonnet*'],
    limitMicros: 0,
    limitTokens: 0,
    rpm: 0,
    hardCutoff: false,
    metadata: { notes: 'chat-only, no cap', priceCents: 0, billingCycle: 'one-off' },
  },
  {
    id: 'buyer-full-30d',
    label: 'Buyer — all models, 30 days',
    description: 'Every model the gateway can route, key expires in 30 days, no spend cap.',
    models: [],
    limitMicros: 0,
    limitTokens: 0,
    rpm: 0,
    hardCutoff: false,
    metadata: { notes: 'all models, 30 days', priceCents: 0, billingCycle: 'one-off' },
  },
] as const

export const DEFAULT_BUYER_PRESET_ID = BUYER_PRESETS[0].id

export function findBuyerPreset(id: string): BuyerPreset | undefined {
  return BUYER_PRESETS.find((p) => p.id === id)
}

/** The RFC3339 expiry a preset-created key should carry, or '' for no expiry. */
export function buyerPresetExpiry(now: Date): string {
  const expiry = new Date(now.getTime())
  expiry.setUTCDate(expiry.getUTCDate() + MONTH_DAYS)
  return expiry.toISOString()
}

/** The fields a preset pre-fills, in the shape the create form holds. */
export interface BuyerPresetDraft {
  name: string
  models: string[]
  limitMicros: number
  limitTokens: number
  rateLimitRpm: number
  hardCutoff: boolean
  expiresAt: string
  presetId: string
  /** Serialized straight into the policy request's metadata field. */
  metadata: string
}

/**
 * A preset applied to a name. The name is what the buyer is called — "buyer-acme"
 * against "Buyer — chat, $10/month" — so the two are concatenated and the caller
 * decides whether that is useful by passing a name at all.
 *
 * An unknown id falls back to the default preset rather than throwing: a
 * renamed preset must not make the create form unusable.
 */
export function applyBuyerPreset(presetId: string, name: string, now: Date): BuyerPresetDraft {
  const preset = findBuyerPreset(presetId) ?? findBuyerPreset(DEFAULT_BUYER_PRESET_ID)!
  return {
    name: name.trim(),
    models: [...preset.models],
    limitMicros: preset.limitMicros,
    limitTokens: preset.limitTokens,
    rateLimitRpm: preset.rpm,
    hardCutoff: preset.hardCutoff,
    expiresAt: buyerPresetExpiry(now),
    presetId: preset.id,
    metadata: buyerPresetMetadata(preset, name),
  }
}

/**
 * The metadata blob a preset stamps, in the vocabulary apiKeys.metadata
 * validates. An invented key would be refused at the write and take the whole
 * policy request down with it, so the preset carries only accepted keys and
 * the buyer's name rides in `customerId`.
 */
export function buyerPresetMetadata(preset: BuyerPreset, buyerName: string): string {
  const name = buyerName.trim()
  return JSON.stringify({
    ...preset.metadata,
    ...(name ? { customerId: name } : {}),
  })
}