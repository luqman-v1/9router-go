/**
 * The counts the sidebar shows beside an entry, and the rules that keep them
 * quiet.
 *
 * Pure on purpose — the sidebar renders these, but the decision of *whether* to
 * show one lives here so it can be unit-tested without the Svelte compiler,
 * the way lib/navFavorites.ts and lib/usagePanelPlan.ts are.
 *
 * A badge is a fact the operator would otherwise open a page to learn, and it
 * is only worth the space when it is actionable: "3 / 12 active" says a third
 * of the fleet is down, while "12" beside a list of twelve says nothing the
 * label does not. So a count is shown when it is a subset of a whole, and a
 * bare total is not shown at all.
 */
export interface NavBadgeInput {
  /** Connections this deployment has, active and inactive. */
  totalConnections: number
  /** Of those, the ones currently answering. */
  activeConnections: number
  /** Combos configured. */
  comboCount: number
  /** API keys issued. */
  apiKeyCount: number
  /** Connections whose last probe or request failed. */
  failingConnections: number
}

export interface NavBadge {
  /** What the badge reads, e.g. "3 / 12" or "2". */
  text: string
  /**
   * `warn` paints it in the warning colour and is reserved for a number that
   * means something is wrong; `muted` is a plain count.
   */
  tone: 'muted' | 'warn'
  /** The full sentence, for the tooltip a screen reader and a hover both get. */
  title: string
}

/** Above this, a badge stops being a number and becomes noise. */
const BADGE_DISPLAY_CAP = 999

function cap(value: number): string {
  return value > BADGE_DISPLAY_CAP ? `${BADGE_DISPLAY_CAP}+` : String(value)
}

/**
 * The badge for one sidebar entry, or null when the entry has nothing worth
 * saying.
 *
 * `null` is the common answer: most entries are a page the operator opens on
 * purpose, and a number beside them would be decoration.
 */
export function navBadgeFor(id: string, input: NavBadgeInput): NavBadge | null {
  switch (id) {
    case 'connections': {
      // A failing connection is the one number that earns the warning colour:
      // the operator's fleet is degraded and they have not opened the page yet.
      if (input.failingConnections > 0) {
        return {
          text: cap(input.failingConnections),
          tone: 'warn',
          title: `${input.failingConnections} connection${input.failingConnections === 1 ? '' : 's'} failing their last check`,
        }
      }
      if (input.totalConnections === 0) return null
      // Shown as a fraction, never as a bare total: "3 / 12 active" says a
      // quarter of the fleet is down, which "12" does not.
      if (input.activeConnections === input.totalConnections) return null
      return {
        text: `${cap(input.activeConnections)} / ${cap(input.totalConnections)}`,
        tone: 'muted',
        title: `${input.activeConnections} of ${input.totalConnections} connections active`,
      }
    }
    case 'combos':
      // A bare total, so it is only shown while the list is short enough that
      // the number is quicker to read than opening the page.
      if (input.comboCount === 0 || input.comboCount > 20) return null
      return {
        text: cap(input.comboCount),
        tone: 'muted',
        title: `${input.comboCount} combo${input.comboCount === 1 ? '' : 's'} configured`,
      }
    case 'endpoint':
      if (input.apiKeyCount === 0 || input.apiKeyCount > 20) return null
      return {
        text: cap(input.apiKeyCount),
        tone: 'muted',
        title: `${input.apiKeyCount} API key${input.apiKeyCount === 1 ? '' : 's'} issued`,
      }
    default:
      return null
  }
}
