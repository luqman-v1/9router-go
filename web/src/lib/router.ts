export type ActiveTab =
  | 'login'
  | 'endpoint'
  | 'connections'
  | 'combos'
  | 'analytics'
  | 'quota'
  | 'token-saver'
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
  | 'security'
  | 'keys' // legacy alias for the endpoint tab; see ROUTE_TO_TAB below

export const TAB_ROUTES: Record<ActiveTab, string> = {
  login: '/login',
  endpoint: '/dashboard/endpoint',
  connections: '/dashboard/providers',
  combos: '/dashboard/combos',
  analytics: '/dashboard/usage',
  quota: '/dashboard/quota',
  'token-saver': '/dashboard/token-saver',
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
  security: '/dashboard/security',
  // Legacy alias: keys are managed on the endpoint tab (issue #199), so both
  // tabs resolve to the same route rather than leaving one unreachable.
  keys: '/dashboard/endpoint',
}

const ROUTE_TO_TAB: Record<string, ActiveTab> = {
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

  // quota tracker
  '/dashboard/quota': 'quota',
  '/quota': 'quota',

  // token saver
  '/dashboard/token-saver': 'token-saver',
  '/token-saver': 'token-saver',

  // cli-tools
  '/dashboard/cli-tools': 'cli-tools',
  '/cli-tools': 'cli-tools',

  // Client API keys now live on the endpoint tab (issue #199), so the old key
  // URLs resolve there instead of to a tab that no longer renders. Keeping the
  // mapping means a bookmarked /dashboard/keys still lands on the key table.
  '/dashboard/keys': 'endpoint',
  '/keys': 'endpoint',

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

  // security: vault + guardrails
  '/dashboard/security': 'security',
  '/security': 'security',
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
  if (normalized.includes('keys')) return 'endpoint'
  if (normalized.includes('security')) return 'security'
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
