import { describe, expect, it } from 'bun:test'
import { groupConnectionsByProvider } from './types'

describe('groupConnectionsByProvider', () => {
  it('groups contiguous connections by provider, preserving order', () => {
    const conns = [
      { id: 'a1', provider: 'claude' },
      { id: 'a2', provider: 'claude' },
      { id: 'b1', provider: 'codex' },
      { id: 'c1', provider: 'deepseek' },
    ]
    const groups = groupConnectionsByProvider(conns)
    expect(groups.map((g) => g.provider)).toEqual(['claude', 'codex', 'deepseek'])
    expect(groups[0].connections.map((c) => c.id)).toEqual(['a1', 'a2'])
    expect(groups[1].connections.map((c) => c.id)).toEqual(['b1'])
    expect(groups[2].connections.map((c) => c.id)).toEqual(['c1'])
  })

  it('re-groups an interleaved list by first-seen provider', () => {
    const conns = [
      { id: 'a1', provider: 'claude' },
      { id: 'b1', provider: 'codex' },
      { id: 'a2', provider: 'claude' },
    ]
    const groups = groupConnectionsByProvider(conns)
    expect(groups.map((g) => g.provider)).toEqual(['claude', 'codex'])
    expect(groups[0].connections.map((c) => c.id)).toEqual(['a1', 'a2'])
  })

  it('returns no groups for an empty list', () => {
    expect(groupConnectionsByProvider([])).toEqual([])
  })
})
