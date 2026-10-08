export type ActiveTab =
  | 'login'
  | 'endpoint'
  | 'connections'
  | 'combos'
  | 'analytics'
  | 'quota'
  | 'token-saver'
// Cache Analytics and Compression Analytics are sections of the Usage page
// (issue #200), not sidebar destinations. They keep their own paths so a
// section stays linkable and survives a reload.
  | 'usage-cache'
  | 'usage-compression'
  | 'cli-tools'
  | 'media-embedding'
  | 'media-image'
  | 'media-tts'
  | 'media-stt'
  | 'media-video'
  | 'media-systemone'
  | 'media-web'
  | 'proxy-pools'
  | 'skills'
  | 'console-log'
  | 'terminal'
  | 'settings'
  | 'keys'

export const TAB_ROUTES: Record<ActiveTab, string> = {
  login: '/login',
  endpoint: '/dashboard/endpoint',
  connections: '/dashboard/providers',
  combos: '/dashboard/combos',
  analytics: '/dashboard/usage',
  quota: '/dashboard/quota',
  'token-saver': '/dashboard/token-saver',
  'usage-cache': '/dashboard/usage/cache',
  'usage-compression': '/dashboard/usage/compression',
  'cli-tools': '/dashboard/cli-tools',
  'media-embedding': '/dashboard/media-providers/embedding',
  'media-image': '/dashboard/media-providers/image',
  'media-tts': '/dashboard/media-providers/tts',
  'media-stt': '/dashboard/media-providers/stt',
  'media-video': '/dashboard/media-providers/video',
  'media-systemone': '/dashboard/media-providers/systemone',
  'media-web': '/dashboard/media-providers/web',
  'proxy-pools': '/dashboard/proxy-pools',
  skills: '/dashboard/skills',
  'console-log': '/dashboard/console-log',
  terminal: '/dashboard/console-log',
  settings: '/dashboard/profile',
  keys: '/dashboard/cli-tools',
}

const ROUTE_TO_TAB: Record<string, ActiveTab> = {
  // login
  '/login': 'login',

  // endpoint
  '/': 'endpoint',
  '/dashboard': 'endpoint',
  '/dashboard/endpoint': 'endpoint',
  '/endpoint': 'endpoint',

  // connections / providers
  '/dashboard/providers': 'connections',
  '/dashboard/connections': 'connections',
  '/providers': 'connections',
  '/connections': 'connections',

  // combos
  '/dashboard/combos': 'combos',
  '/combos': 'combos',

  // usage / analytics
  '/dashboard/usage': 'analytics',
  '/dashboard/analytics': 'analytics',
  '/usage': 'analytics',
  '/analytics': 'analytics',

  // Cache + compression analytics, sections of the Usage page (issue #200).
  '/dashboard/usage/cache': 'usage-cache',
  '/dashboard/usage/compression': 'usage-compression',

  // The pre-move paths stay resolvable so an old bookmark or a tab left open
  // across the upgrade lands on the Usage section instead of the endpoint view
  // the substring fallback would otherwise pick.
  '/dashboard/cache': 'usage-cache',
  '/dashboard/analytics/compression': 'usage-compression',
  '/cache': 'usage-cache',
  '/analytics/compression': 'usage-compression',

  // quota tracker
  '/dashboard/quota': 'quota',
  '/quota': 'quota',

  // token saver
  '/dashboard/token-saver': 'token-saver',
  '/token-saver': 'token-saver',

  // cli-tools
  '/dashboard/cli-tools': 'cli-tools',
  '/dashboard/keys': 'cli-tools',
  '/cli-tools': 'cli-tools',
  '/keys': 'cli-tools',

  // media providers
  '/dashboard/media-providers/embedding': 'media-embedding',
  '/media-providers/embedding': 'media-embedding',
  '/media/embedding': 'media-embedding',

  '/dashboard/media-providers/image': 'media-image',
  '/media-providers/image': 'media-image',
  '/media/image': 'media-image',

  '/dashboard/media-providers/tts': 'media-tts',
  '/media-providers/tts': 'media-tts',
  '/media/tts': 'media-tts',

  '/dashboard/media-providers/stt': 'media-stt',
  '/media-providers/stt': 'media-stt',
  '/media/stt': 'media-stt',

  '/dashboard/media-providers/video': 'media-video',
  '/media-providers/video': 'media-video',
  '/media/video': 'media-video',
  '/dashboard/media-providers/systemone': 'media-systemone',
  '/media-providers/systemone': 'media-systemone',
  '/media/systemone': 'media-systemone',
  '/dashboard/media-providers/web': 'media-web',
  '/dashboard/media-providers': 'media-web',
  '/media-providers/web': 'media-web',
  '/media/web': 'media-web',
  '/media': 'media-web',

  // proxy pools
  '/dashboard/proxy-pools': 'proxy-pools',
  '/proxy-pools': 'proxy-pools',

  // skills
  '/dashboard/skills': 'skills',
  '/skills': 'skills',

  // console-log / terminal
  '/dashboard/console-log': 'console-log',
  '/dashboard/terminal': 'console-log',
  '/dashboard/logs': 'console-log',
  '/console-log': 'console-log',
  '/terminal': 'console-log',

  // settings / profile
  '/dashboard/profile': 'settings',
  '/dashboard/settings': 'settings',
  '/profile': 'settings',
  '/settings': 'settings',
}

// The Usage page's sections. Cache Analytics and Compression Analytics joined
// Overview and Details as sections (issue #200): they report on the same
// traffic as the Overview, and as top-level sidebar entries they read as
// separate products. Each section that owns a path gets its own tab, so the
// section is linkable and survives a reload.
export type UsageSection = 'overview' | 'cache' | 'compression' | 'details'

export const USAGE_SECTIONS: { value: UsageSection; label: string; icon: string }[] = [
  { value: 'overview', label: 'Overview', icon: 'bar_chart' },
  { value: 'cache', label: 'Cache Analytics', icon: 'cached' },
  { value: 'compression', label: 'Compression Analytics', icon: 'compress' },
  { value: 'details', label: 'Details', icon: 'receipt_long' },
]

// USAGE_SECTION_BY_VALUE is partial on purpose: Overview and Details share the
// 'analytics' tab, so the page owns their path and only the two moved sections
// need a tab of their own.
export const USAGE_SECTION_BY_VALUE: Partial<Record<UsageSection, ActiveTab>> = {
  cache: 'usage-cache',
  compression: 'usage-compression',
}

// USAGE_SECTION_BY_TAB is the reverse lookup, and is deliberately absent for
// 'analytics': one path serves both Overview and Details, so only the two moved
// sections can be recovered from a URL.
export const USAGE_SECTION_BY_TAB: Partial<Record<ActiveTab, UsageSection>> = {
  'usage-cache': 'cache',
  'usage-compression': 'compression',
}

export function pathToTab(pathname: string): ActiveTab {
  if (!pathname) return 'endpoint'
  const clean = pathname.trim().split('?')[0].split('#')[0]
  const normalized = (clean.replace(/\/+$/, '') || '/').toLowerCase()
  if (ROUTE_TO_TAB[normalized]) {
    return ROUTE_TO_TAB[normalized]
  }
  if (normalized.includes('login')) return 'login'
  if (normalized.includes('endpoint')) return 'endpoint'
  if (normalized.includes('embedding')) return 'media-embedding'
  if (normalized.includes('image')) return 'media-image'
  if (normalized.includes('tts')) return 'media-tts'
  if (normalized.includes('stt')) return 'media-stt'
  if (normalized.includes('video')) return 'media-video'
  if (normalized.includes('systemone')) return 'media-systemone'
  if (normalized.includes('media')) return 'media-web'
  if (normalized.includes('token-saver')) return 'token-saver'
  if (normalized.includes('cli-tools')) return 'cli-tools'
  if (normalized.includes('proxy-pools')) return 'proxy-pools'
  if (normalized.includes('skills')) return 'skills'
  if (
    normalized.includes('console-log') ||
    normalized.includes('terminal') ||
    normalized.includes('logs')
  ) {
    return 'console-log'
  }
  if (normalized.includes('quota')) return 'quota'
  if (normalized.includes('usage') || normalized.includes('analytics')) return 'analytics'
  if (normalized.includes('combos')) return 'combos'
  if (normalized.includes('providers') || normalized.includes('connections')) return 'connections'
  if (normalized.includes('profile') || normalized.includes('settings')) return 'settings'
  return 'endpoint'
}

export function parseProviderId(pathname: string): string | null {
  if (!pathname) return null
  const clean = pathname.trim().split('?')[0].split('#')[0]
  const normalized = clean.replace(/\/+$/, '')
  const match = normalized.match(/^(?:\/dashboard)?\/providers\/([^/]+)$/)
  if (match) {
    const id = match[1].toLowerCase()
    if (id !== 'new' && id !== 'providers') {
      return match[1]
    }
  }
  return null
}

export function providerPath(providerId: string): string {
  return `/dashboard/providers/${encodeURIComponent(providerId)}`
}

export interface MediaProviderRoute {
  kind: string
  providerId: string
}

export function parseMediaProvider(pathname: string): MediaProviderRoute | null {
  if (!pathname) return null
  const clean = pathname.trim().split('?')[0].split('#')[0]
  const normalized = clean.replace(/\/+$/, '')
  const match = normalized.match(/^(?:\/dashboard)?\/media-providers\/([^/]+)\/([^/]+)$/)
  if (match) {
    const kind = match[1]
    const providerId = match[2]
    if (kind !== 'combo') {
      return { kind, providerId }
    }
  }
  return null
}

export function mediaProviderPath(kind: string, providerId: string): string {
  return `/dashboard/media-providers/${encodeURIComponent(kind)}/${encodeURIComponent(providerId)}`
}
