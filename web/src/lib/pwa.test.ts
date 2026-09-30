import assert from 'node:assert'
import { afterEach, describe, it } from 'node:test'

type Listener = (event: FakeInstallEvent) => void
type ListenerRegistry = Record<string, Listener[]>

interface FakeInstallEvent {
  prevented: boolean
  promptCalls: number
  preventDefault(): void
  prompt(): Promise<void>
  userChoice: Promise<{ outcome: string; platform: string }>
}

interface GlobalStubs {
  window: unknown
  navigator: unknown
  document: unknown
}

const STUBBED_GLOBALS = ['window', 'navigator', 'document'] as const

// bun test runs every file in one process, so a stubbed window that outlives
// this file breaks whatever reads it next: client.ts evaluates
// `window.location.pathname` inside handleUnauthorized, and a stub without
// `location` throws there - the 401 listener never fires and its test hangs
// until the 5s timeout. Descriptors, not values, so a global that was absent
// stays absent and a read-only one is not assigned over.
const pristine = STUBBED_GLOBALS.map(
  (key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)] as const
)

afterEach(() => {
  for (const [key, descriptor] of pristine) {
    if (descriptor) {
      Object.defineProperty(globalThis, key, descriptor)
    } else {
      delete (globalThis as unknown as Record<string, unknown>)[key]
    }
  }
})

function harness(standalone: boolean) {
  const listeners: ListenerRegistry = {}
  const win = {
    addEventListener(type: string, fn: Listener) {
      const existing = listeners[type] || []
      existing.push(fn)
      listeners[type] = existing
    },
    matchMedia: (query: string) => ({ matches: standalone && query.includes('standalone') }),
  }
  const g = globalThis as unknown as GlobalStubs
  g.window = win
  g.navigator = {}
  g.document = { referrer: '' }

  return {
    fire: (type: string, event: FakeInstallEvent) => {
      for (const fn of listeners[type] || []) fn(event)
    },
    // Fresh module instance per case: pwa.ts keeps the deferred event in module
    // state, so a shared instance would leak between tests.
    load: () => import(`./pwa.ts?case=${Math.random()}`),
  }
}

function installEvent(outcome: 'accepted' | 'dismissed'): FakeInstallEvent {
  return {
    prevented: false,
    promptCalls: 0,
    preventDefault() {
      this.prevented = true
    },
    async prompt() {
      this.promptCalls += 1
    },
    userChoice: Promise.resolve({ outcome, platform: 'web' }),
  }
}

describe('pwa install prompt', () => {
  it('defers the browser banner so the custom Install button can use the event', async () => {
    const h = harness(false)
    const pwa = await h.load()
    const seen: boolean[] = []
    pwa.subscribeInstallPrompt((canInstall) => seen.push(canInstall))
    pwa.registerServiceWorker()

    const event = installEvent('accepted')
    h.fire('beforeinstallprompt', event)

    assert.equal(event.prevented, true)
    assert.deepEqual(seen, [false, true])
  })

  it('leaves the browser banner alone when already running as an installed app', async () => {
    const h = harness(true)
    const pwa = await h.load()
    const seen: boolean[] = []
    pwa.subscribeInstallPrompt((canInstall) => seen.push(canInstall))
    pwa.registerServiceWorker()

    const event = installEvent('accepted')
    h.fire('beforeinstallprompt', event)

    assert.equal(event.prevented, false)
    assert.deepEqual(seen, [false])
  })

  it('spends the single-use event on an accepted install', async () => {
    const h = harness(false)
    const pwa = await h.load()
    pwa.registerServiceWorker()
    const event = installEvent('accepted')
    h.fire('beforeinstallprompt', event)

    assert.equal(await pwa.promptInstall(), true)
    assert.equal(event.promptCalls, 1)
    assert.equal(await pwa.promptInstall(), false, 'a spent event must not prompt again')
    assert.equal(event.promptCalls, 1)
  })

  it('spends the event on a dismissed install so the next click cannot reuse it', async () => {
    const h = harness(false)
    const pwa = await h.load()
    pwa.registerServiceWorker()
    const event = installEvent('dismissed')
    h.fire('beforeinstallprompt', event)

    assert.equal(await pwa.promptInstall(), false)
    assert.equal(event.promptCalls, 1)

    // Chrome re-arms the CTA only through a fresh beforeinstallprompt event.
    const seen: boolean[] = []
    pwa.subscribeInstallPrompt((canInstall) => seen.push(canInstall))
    assert.deepEqual(seen, [false], 'a dismissed dialog must not leave the button armed')
    assert.equal(await pwa.promptInstall(), false)
    assert.equal(event.promptCalls, 1)
  })
})