// The command palette's search index: every model, provider, sidebar entry and
// settings section the dashboard can open, flattened into one list of rows.
//
// The index is built once and cached, and the query is only run when the
// operator asks for it — Enter or the Search button — rather than on every
// keystroke. A keystroke handler that filters 7,000 models re-renders the list
// between two keypresses, which is the lag this design exists to avoid: the
// input stays a plain controlled field, and the expensive pass happens once,
// on a click or a key the operator meant.
//
// Rows are plain data so the component can render them without knowing where
// they came from.
import { PROVIDER_ID_TO_ALIAS, PROVIDER_MODELS, type ProviderModel } from './models'
import { PROVIDER_CATALOG, type ProviderCatalogItem } from './providers'
import { NAV_GROUPS, type NavEntry, type NavLink } from './nav'
import { TAB_ROUTES, type ActiveTab } from './router'

export type PaletteGroup = 'nav' | 'provider' | 'model' | 'settings'

export interface PaletteRow {
  /** Stable across rebuilds, so a row can be keyed and re-selected. */
  id: string
  group: PaletteGroup
  label: string
  /** The line under the label: a provider name, a route, a section hint. */
  hint: string
  /** Where the row goes. A nav row navigates; a model row copies its id. */
  route: string
  icon: string
  /** Lower-cased haystack, built once so a query is one pass over strings. */
  search: string
}

/** Every sidebar entry, accordion children included, in render order. */
function navEntries(): NavLink[] {
  return NAV_GROUPS.flatMap((group) =>
    group.entries.flatMap((entry: NavEntry) =>
      entry.kind === 'accordion' ? entry.children : [entry]
    )
  )
}

function navRows(): PaletteRow[] {
  return navEntries().map((entry) => ({
    id: `nav:${entry.id}`,
    group: 'nav' as const,
    label: entry.label,
    hint: 'Sidebar',
    route: TAB_ROUTES[entry.id as ActiveTab] ?? '/dashboard/endpoint',
    icon: entry.icon,
    search: `${entry.label} sidebar ${entry.id}`.toLowerCase(),
  }))
}

function providerRows(): PaletteRow[] {
  return PROVIDER_CATALOG.filter((provider: ProviderCatalogItem) => !provider.hidden).map(
    (provider) => ({
      id: `provider:${provider.id}`,
      group: 'provider' as const,
      label: provider.name,
      hint: `Provider · ${provider.alias}`,
      route: '/dashboard/providers',
      icon: 'dns',
      search: `${provider.name} provider ${provider.id} ${provider.alias}`.toLowerCase(),
    })
  )
}

function modelRows(): PaletteRow[] {
  const rows: PaletteRow[] = []
  for (const [providerId, models] of Object.entries(PROVIDER_MODELS)) {
    const alias = PROVIDER_ID_TO_ALIAS[providerId] ?? providerId
    for (const model of models as ProviderModel[]) {
      const id = String(model.id ?? '')
      if (!id) continue
      rows.push({
        id: `model:${providerId}:${id}`,
        group: 'model' as const,
        label: id,
        hint: `${alias} · ${model.name ?? providerId}`,
        route: '/dashboard/endpoint',
        icon: 'bolt',
        search: `${id} ${model.name ?? ''} ${providerId} ${alias} model`.toLowerCase(),
      })
    }
  }
  return rows
}

/** The Settings sections, addressed by the route that opens them. */
const SETTINGS_SECTIONS: { id: string; label: string; hint: string }[] = [
  { id: 'display', label: 'Display & Appearance', hint: 'Settings › Display' },
  { id: 'sidebar', label: 'Sidebar & Feature Visibility', hint: 'Settings › Sidebar' },
  { id: 'usage-panels', label: 'Usage Panels', hint: 'Settings › Usage' },
  { id: 'currency', label: 'Currency & Rates', hint: 'Settings › Currency' },
  { id: 'auth', label: 'Auth & Master Access', hint: 'Settings › Security' },
  { id: 'proxy', label: 'Outbound Proxy', hint: 'Settings › Proxy' },
  { id: 'sso', label: 'SSO / OIDC / SAML', hint: 'Settings › SSO' },
]

function settingsRows(): PaletteRow[] {
  return SETTINGS_SECTIONS.map((section) => ({
    id: `settings:${section.id}`,
    group: 'settings' as const,
    label: section.label,
    hint: section.hint,
    route: '/dashboard/profile',
    icon: 'tune',
    search: `${section.label} settings ${section.hint} ${section.id}`.toLowerCase(),
  }))
}

let cache: PaletteRow[] | null = null

/**
 * The whole index, built on first call and reused after that.
 *
 * The rows are derived from the same catalogs the dashboard renders, so a
 * provider or model added to the registry appears here without a second list
 * to maintain.
 */
export function buildPaletteIndex(): PaletteRow[] {
  if (cache) return cache
  cache = [...navRows(), ...settingsRows(), ...providerRows(), ...modelRows()]
  return cache
}

/** Drops the cached index. Only a test or a hot reload needs this. */
export function resetPaletteIndex(): void {
  cache = null
}

export interface PaletteQuery {
  /** What the operator typed. Empty means "no query yet". */
  text: string
  /** Caps the result list so a one-character query cannot render 7,000 rows. */
  limit?: number
}

export interface PaletteResult {
  rows: PaletteRow[]
  /** How many rows the query matched before the cap. */
  total: number
}

/**
 * Runs a query over the index. Every term must appear somewhere in the row's
 * haystack, so "claude sonnet" narrows rather than widens.
 *
 * Nav and settings rows are ranked first: an operator typing a tab name wants
 * the tab, not the 400 models whose id happens to contain the same letters.
 */
export function searchPalette({ text, limit = 40 }: PaletteQuery): PaletteResult {
  const index = buildPaletteIndex()
  const terms = text.toLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) {
    return { rows: [], total: 0 }
  }
  const groupRank: Record<PaletteGroup, number> = { nav: 0, settings: 1, provider: 2, model: 3 }
  const matched: PaletteRow[] = []
  for (const row of index) {
    let ok = true
    for (const term of terms) {
      if (!row.search.includes(term)) {
        ok = false
        break
      }
    }
    if (ok) matched.push(row)
  }
  matched.sort((a, b) => groupRank[a.group] - groupRank[b.group] || a.label.localeCompare(b.label))
  return { rows: matched.slice(0, limit), total: matched.length }
}
