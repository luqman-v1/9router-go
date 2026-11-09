/**
 * The arrangement half of the Usage panel preferences: which order the panels of
 * a section are drawn in, and how that order folds into renderable blocks.
 *
 * Pure on purpose — the reactive state (and its localStorage draft) lives in
 * lib/usagePanel.svelte.ts, so this module can be unit-tested without the Svelte
 * compiler, the way lib/period.ts and lib/comboMatch.ts are.
 */
import { USAGE_PANEL_GROUPS, type UsagePanelGroupId, type UsagePanelId } from './usagePanel'

/** The order a section draws its panels in when nothing has been arranged. */
export const USAGE_PANEL_DEFAULT_ORDER: Record<UsagePanelGroupId, UsagePanelId[]> =
  Object.fromEntries(USAGE_PANEL_GROUPS.map((group) => [group.id, group.items.map((item) => item.id)])) as Record<
    UsagePanelGroupId,
    UsagePanelId[]
  >

/**
 * Two panels that share one layout row. In the Overview the provider map and the
 * Recent Requests list sit side by side; they still hide independently, so the
 * arrangement treats them as a row only while they are both visible and adjacent.
 */
export const USAGE_PANEL_ROW_PARTNERS: Partial<Record<UsagePanelId, UsagePanelId>> = {
  'overview.topology': 'overview.recent',
  'overview.recent': 'overview.topology',
}

export interface UsagePanelBlock {
  /** Stable key for the `{#each}` over the section's blocks. */
  key: string
  ids: UsagePanelId[]
}

/**
 * blocksFor turns one section's ordered, visible panels into the blocks the
 * template draws: a row block for the two panel ids that share a row and ended up
 * next to each other, a panel block for everything else. Ordering is preserved
 * exactly as given, so what the settings list shows is what the page draws.
 */
export function blocksFor(orderedVisible: readonly UsagePanelId[]): UsagePanelBlock[] {
  const blocks: UsagePanelBlock[] = []
  const consumed = new Set<UsagePanelId>()
  for (let i = 0; i < orderedVisible.length; i++) {
    const id = orderedVisible[i]
    if (consumed.has(id)) continue
    const partner = USAGE_PANEL_ROW_PARTNERS[id]
    if (partner && i + 1 < orderedVisible.length && orderedVisible[i + 1] === partner) {
      blocks.push({ key: `${id}+${partner}`, ids: [id, partner] })
      consumed.add(id)
      consumed.add(partner)
      continue
    }
    blocks.push({ key: id, ids: [id] })
    consumed.add(id)
  }
  return blocks
}
