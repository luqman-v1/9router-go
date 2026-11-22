import { describe, expect, test } from 'bun:test'
import {
  clearCallback,
  clearPending,
  dashboardCallbackURL,
  loadPendings,
  loopbackPeerOrigin,
  matchPending,
  OAUTH_CALLBACK_KEY,
  OAUTH_PENDING_KEY,
  oauthLoopbackCallbackURL,
  parseCallbackURL,
  readCallback,
  savePending,
  writeCallback,
  type KVStore,
  type OAuthPending,
} from './oauth-handoff'

function memStore(): KVStore & { dump: Record<string, string> } {
  const dump: Record<string, string> = {}
  return {
    dump,
    getItem: (k) => (k in dump ? dump[k] : null),
    setItem: (k, v) => {
      dump[k] = v
    },
    removeItem: (k) => {
      delete dump[k]
    },
  }
}

describe('oauth-handoff pending sessions', () => {
  test('save + load round-trip, stale pruned', () => {
    const s = memStore()
    savePending(s, { provider: 'cline', state: 'st1', verifier: 'v1' }, 1000)
    savePending(s, { provider: 'cline', state: 'st2', verifier: 'v2' }, 2000)
    expect(loadPendings(s, 3000)).toHaveLength(2)
    // 15 min TTL: both stale at +16min
    expect(loadPendings(s, 2000 + 16 * 60 * 1000)).toHaveLength(0)
  })

  test('re-saving same provider+state refreshes instead of duplicating', () => {
    const s = memStore()
    savePending(s, { provider: 'codex', state: 'a' }, 1000)
    savePending(s, { provider: 'codex', state: 'a', verifier: 'v9' }, 2000)
    const list = loadPendings(s, 3000)
    expect(list).toHaveLength(1)
    expect(list[0].verifier).toBe('v9')
  })

  test('clearPending removes only the matching entry', () => {
    const s = memStore()
    const now = Date.now()
    savePending(s, { provider: 'cline', state: 'a' }, now)
    savePending(s, { provider: 'codex', state: 'b' }, now)
    clearPending(s, 'cline', 'a')
    expect(loadPendings(s, now + 1000).map((e) => e.provider)).toEqual(['codex'])
  })
})

describe('matchPending', () => {
  const pendings: OAuthPending[] = [
    { provider: 'cline', state: 's1', at: 1000 },
    { provider: 'codex', state: 's2', at: 2000 },
  ]
  test('same state + provider wins', () => {
    expect(matchPending(pendings, 'codex', 's2')?.provider).toBe('codex')
  })
  test('same state, other provider falls back to state match', () => {
    expect(matchPending(pendings, 'xai', 's1')?.provider).toBe('cline')
  })
  test('no state falls back to freshest same-provider session', () => {
    expect(matchPending(pendings, 'cline', '')?.state).toBe('s1')
  })
  test('no match returns null', () => {
    expect(matchPending(pendings, 'zed', 'zzz')).toBeNull()
    expect(matchPending([], 'cline', '')).toBeNull()
  })
})

describe('callback read/write', () => {
  test('round-trip + clear', () => {
    const s = memStore()
    writeCallback(s, { state: 's1', raw: 'code=abc' }, 5000)
    expect(s.dump[OAUTH_CALLBACK_KEY]).toBeString()
    expect(readCallback(s)).toMatchObject({ state: 's1', raw: 'code=abc' })
    clearCallback(s)
    expect(readCallback(s)).toBeNull()
  })
})

describe('parseCallbackURL', () => {
  test('query code + state', () => {
    expect(parseCallbackURL('http://h:1/callback?code=abc&state=s')).toMatchObject({
      state: 's',
      raw: 'abc',
      error: '',
    })
  })
  test('fragment token (implicit flow)', () => {
    expect(parseCallbackURL('http://h:1/callback#access_token=tok&state=s')).toMatchObject({
      state: 's',
      raw: 'tok',
    })
  })
  test('error surfaces', () => {
    expect(parseCallbackURL('http://h:1/cb?error=access_denied&error_description=nope')).toMatchObject({
      error: 'access_denied',
      errorDesc: 'nope',
      raw: '',
    })
  })
  test('unknown shape hands over full query for provider parsers', () => {
    const p = parseCallbackURL('http://h:1/cb?refreshToken=r&loginHost=example.com')
    expect(p.raw).toContain('refreshToken=r')
  })
  test('garbage input is safe', () => {
    expect(parseCallbackURL('not a url')).toEqual({ state: '', raw: '', error: '', errorDesc: '' })
  })
})

describe('dashboardCallbackURL', () => {
  test('pins to origin, trims slashes', () => {
    expect(dashboardCallbackURL('http://localhost:20131/')).toBe('http://localhost:20131/callback')
    expect(dashboardCallbackURL('https://dash.example.com')).toBe('https://dash.example.com/callback')
    expect(dashboardCallbackURL('https://dash.example.com///')).toBe('https://dash.example.com/callback')
  })
})

describe('oauthLoopbackCallbackURL', () => {
  test('matches upstream Antigravity loopback behavior', () => {
    expect(oauthLoopbackCallbackURL('20130', false)).toBe('http://localhost:20130/callback')
    expect(oauthLoopbackCallbackURL('', false)).toBe('http://localhost:80/callback')
    expect(oauthLoopbackCallbackURL('', true)).toBe('http://localhost:443/callback')
  })
})

describe('loopbackPeerOrigin', () => {
  test('swaps localhost and 127.0.0.1 preserving scheme and port', () => {
    expect(loopbackPeerOrigin('http://localhost:20130')).toBe('http://127.0.0.1:20130')
    expect(loopbackPeerOrigin('http://127.0.0.1:20130')).toBe('http://localhost:20130')
    expect(loopbackPeerOrigin('https://example.com')).toBeNull()
    expect(loopbackPeerOrigin('not-a-url')).toBeNull()
  })
})

describe('storage keys', () => {
  test('uses versioned keys', () => {
    expect(OAUTH_PENDING_KEY).toContain('v1')
    expect(OAUTH_CALLBACK_KEY).toContain('v1')
  })
})
