// PWA Service Worker & Install Prompt helper
export interface BeforeInstallPromptEvent extends Event {
  readonly platforms: string[]
  readonly userChoice: Promise<{
    outcome: 'accepted' | 'dismissed'
    platform: string
  }>
  prompt(): Promise<void>
}

let deferredPrompt: BeforeInstallPromptEvent | null = null
const listeners: Array<(canInstall: boolean) => void> = []

export function isStandalone(): boolean {
  if (typeof window === 'undefined') return false
  return (
    window.matchMedia('(display-mode: standalone)').matches ||
    Boolean((navigator as unknown as { standalone?: boolean }).standalone) ||
    document.referrer.includes('android-app://')
  )
}

// A deploy swaps hashed chunk names; a tab still running the old index.html
// then fails a dynamic import with a 404 and shows a blank screen. Reload
// once (guarded so a persistently broken deploy cannot loop) after clearing
// the SW caches, which may hold the stale index that requested the dead chunk.
const CHUNK_RELOAD_KEY = '9router-chunk-reload'

export function selfHealStaleChunks() {
  if (typeof window === 'undefined') return
  window.addEventListener('vite:preloadError', () => {
    if (sessionStorage.getItem(CHUNK_RELOAD_KEY)) return
    sessionStorage.setItem(CHUNK_RELOAD_KEY, '1')
    if ('caches' in window) {
      caches.keys().then((keys) => keys.forEach((key) => caches.delete(key)))
    }
    location.reload()
  })
  // Fresh load succeeded: re-arm the guard for the next deploy.
  window.addEventListener('load', () => sessionStorage.removeItem(CHUNK_RELOAD_KEY))
}

export function registerServiceWorker() {
  if (typeof window === 'undefined') return

  if ('serviceWorker' in navigator) {
    window.addEventListener('load', () => {
      navigator.serviceWorker
        .register('/sw.js')
        .then((reg) => {
          // Listen for updates
          reg.onupdatefound = () => {
            const installingWorker = reg.installing
            if (installingWorker) {
              installingWorker.onstatechange = () => {
                if (installingWorker.state === 'installed' && navigator.serviceWorker.controller) {
                  // New content is available once old tabs close
                }
              }
            }
          }
        })
        .catch((err) => {
          console.warn('PWA service worker registration failed:', err)
        })
    })
  }

  window.addEventListener('beforeinstallprompt', (e) => {
    // Already running as an installed app: no custom button will ever be
    // shown, so do not swallow the event — that only earns Chrome's
    // "preventDefault() called, the page must call prompt()" console error.
    if (isStandalone()) return
    e.preventDefault()
    deferredPrompt = e as BeforeInstallPromptEvent
    notify(true)
  })

  window.addEventListener('appinstalled', () => {
    deferredPrompt = null
    notify(false)
  })
}

function notify(canInstall: boolean) {
  for (const fn of listeners) {
    fn(canInstall)
  }
}

export function subscribeInstallPrompt(callback: (canInstall: boolean) => void): () => void {
  listeners.push(callback)
  callback(deferredPrompt !== null && !isStandalone())
  return () => {
    const idx = listeners.indexOf(callback)
    if (idx !== -1) {
      listeners.splice(idx, 1)
    }
  }
}

export async function promptInstall(): Promise<boolean> {
  if (!deferredPrompt) return false
  // A BeforeInstallPromptEvent is single-use. Keeping it after a dismissed
  // dialog made the next click call prompt() on a spent event, which Chrome
  // rejects with "Ignored bad install" in the console.
  const event = deferredPrompt
  deferredPrompt = null
  notify(false)
  try {
    await event.prompt()
    const choice = await event.userChoice
    return choice.outcome === 'accepted'
  } catch (err) {
    console.error('Error prompting install:', err)
    return false
  }
}
