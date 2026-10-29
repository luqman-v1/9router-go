import { describe, expect, it } from 'bun:test'
import {
  clearJudgeModel,
  COMBO_SORT_OPTIONS,
  COMBO_STRATEGIES,
  effectiveComboStrategy,
  isComboSorted,
  MAX_FIRST_TOKEN_TIMEOUT_MS,
  MAX_FIRST_TOKEN_TIMEOUT_SECONDS,
  graceMsToSeconds,
  graceSecondsToMs,
  updateFirstTokenGrace,
  secondsToTimeoutMs,
  timeoutMsToSeconds,
  resolveComboStrategy,
  sortComboMembers,
  sortMembersByProvider,
  updateComboStrategy,
  updateFirstTokenTimeout,
  type ComboStrategyInfo
} from './types'
import type { ModelMetaMap } from './types'

const OPTION_VALUES = COMBO_STRATEGIES.map((s) => s.value)

describe('resolveComboStrategy', () => {
  // The server fills combo.strategy from settings.comboStrategies[name] and
  // otherwise from the global `comboStrategy`, so a card can receive either
  // vocabulary. Anything it can receive has to render as one of the options.
  it('maps every value the server can send onto an existing option', () => {
    const serverValues = [
      undefined,
      null,
      '',
      'fallback',
      'round-robin',
      'first-model',
      'sticky',
      'capacity',
      'fusion',
      'nonsense-from-a-hand-edited-backup'
    ]

    for (const value of serverValues) {
      expect(OPTION_VALUES).toContain(resolveComboStrategy(value))
    }
  })

  it('treats the global routing mode as the card fallback', () => {
    // `first-model` is the global Combo Routing Mode and the default when a
    // combo has no per-combo entry. It is try-in-order, which is exactly what
    // the card calls `fallback`.
    expect(resolveComboStrategy('first-model')).toBe('fallback')
  })

  it('passes a real per-combo strategy through unchanged', () => {
    for (const value of ['round-robin', 'sticky', 'capacity', 'fusion']) {
      expect(resolveComboStrategy(value)).toBe(value)
    }
  })

  it('falls back for a missing or unrecognised value', () => {
    expect(resolveComboStrategy(undefined)).toBe('fallback')
    expect(resolveComboStrategy(null)).toBe('fallback')
    expect(resolveComboStrategy('')).toBe('fallback')
    expect(resolveComboStrategy('nonsense')).toBe('fallback')
  })

  it('lists every option with a non-empty label', () => {
    for (const strategy of COMBO_STRATEGIES) {
      expect(strategy.value.length).toBeGreaterThan(0)
      expect(strategy.label.length).toBeGreaterThan(0)
    }
  })
})

describe('per-combo response timeout', () => {
  it('sets and disables only the timeout without mutating existing settings', () => {
    const current: Record<string, ComboStrategyInfo> = {
      primary: { fallbackStrategy: 'smart', judgeModel: 'judge', routerModel: 'router', sessionSticky: false },
      other: { firstTokenTimeoutMs: 750 }
    }
    const enabled = updateFirstTokenTimeout(current, 'primary', 2500)
    expect(enabled.primary).toEqual({ ...current.primary, firstTokenTimeoutMs: 2500 })
    expect(enabled.other).toBe(current.other)
    expect(current.primary.firstTokenTimeoutMs).toBeUndefined()

    const disabled = updateFirstTokenTimeout(enabled, 'primary', 0)
    expect(disabled.primary).toEqual({ ...current.primary, firstTokenTimeoutMs: 0 })
    expect(enabled.primary.firstTokenTimeoutMs).toBe(2500)
  })

  it('keeps inherited strategy when adding or disabling a timeout', () => {
    const enabled = updateFirstTokenTimeout({}, 'inherited', 1000)
    const disabled = updateFirstTokenTimeout(enabled, 'inherited', 0)
    for (const settings of [enabled, disabled]) {
      expect(settings.inherited.fallbackStrategy).toBeUndefined()
      expect(effectiveComboStrategy(settings.inherited.fallbackStrategy, 'round-robin')).toBe('round-robin')
    }
  })

  it('preserves timeout when switching to inherited strategy or clearing a judge', () => {
    const current = { primary: { fallbackStrategy: 'fusion', judgeModel: 'judge', firstTokenTimeoutMs: 1200 } }
    const inherited = updateComboStrategy(current, 'primary', 'inherit')
    expect(inherited.primary).toEqual({ judgeModel: 'judge', firstTokenTimeoutMs: 1200 })
    expect(clearJudgeModel(inherited, 'primary').primary).toEqual({ firstTokenTimeoutMs: 1200 })
  })

  it('accepts the backend maximum timeout', () => {
    expect(updateFirstTokenTimeout({}, 'primary', MAX_FIRST_TOKEN_TIMEOUT_MS)).toEqual({
      primary: { firstTokenTimeoutMs: MAX_FIRST_TOKEN_TIMEOUT_MS }
    })
  })

  it('rejects values outside the backend integer millisecond range', () => {
    const current = { primary: { firstTokenTimeoutMs: 1000 } }
    for (const invalid of [-1, 0.5, NaN, Infinity, MAX_FIRST_TOKEN_TIMEOUT_MS + 1, Number.MAX_SAFE_INTEGER + 1]) {
      expect(updateFirstTokenTimeout(current, 'primary', invalid)).toBe(current)
    }
  })
})

describe('sortMembersByProvider', () => {
  // The tool is a one-shot grouping, not a standing sort: the operator presses
  // it and can drag anything back. So the order INSIDE a group must survive —
  // re-sorting within a provider would discard the fallback ordering the
  // operator arranged.
  it('groups by provider and keeps the order inside each group', () => {
    const input = ['ds/beta', 'oc/alpha', 'ds/alpha', 'cutad/gamma', 'oc/beta']
    expect(sortMembersByProvider(input)).toEqual([
      'ds/beta',
      'ds/alpha',
      'oc/alpha',
      'oc/beta',
      'cutad/gamma'
    ])
  })

  it('orders providers by first appearance, not alphabetically', () => {
    // The operator put zzz first on purpose; alphabetical provider order would
    // move it last and discard that.
    expect(sortMembersByProvider(['zzz/a', 'aaa/b'])).toEqual(['zzz/a', 'aaa/b'])
  })

  it('keeps members without a provider segment in a trailing group', () => {
    expect(sortMembersByProvider(['ds/a', 'bare', 'oc/b'])).toEqual(['ds/a', 'oc/b', 'bare'])
  })

  it('is a no-op on an already grouped list', () => {
    const grouped = ['ds/a', 'ds/b', 'oc/c']
    expect(sortMembersByProvider(grouped)).toEqual(grouped)
  })

  it('handles an empty list', () => {
    expect(sortMembersByProvider([])).toEqual([])
  })

  it('does not mutate the input', () => {
    const input = ['oc/a', 'ds/b']
    const copy = [...input]
    sortMembersByProvider(input)
    expect(input).toEqual(copy)
  })
})

const META: ModelMetaMap = {
  'ds|deepseek-chat': {
    provider: 'ds', model: 'deepseek-chat', ttftMs: 400, totalMs: 2000, samples: 12,
    contextWindow: 0, kind: 'llm', vision: false, reasoning: false, audioInput: false
  },
  'oc|gpt-4o': {
    provider: 'oc', model: 'gpt-4o', ttftMs: 900, totalMs: 3000, samples: 8,
    contextWindow: 128000, kind: 'llm', vision: true, reasoning: true, audioInput: false
  },
  'cutad|glm-5': {
    provider: 'cutad', model: 'glm-5', ttftMs: 0, totalMs: 0, samples: 0,
    contextWindow: 0, kind: 'llm', vision: false, reasoning: false, audioInput: false
  }
}

describe('sortComboMembers', () => {
  const models = ['oc/gpt-4o', 'ds/deepseek-chat', 'cutad/glm-5']

  it('manual returns the list untouched', () => {
    expect(sortComboMembers(models, 'manual')).toEqual(models)
  })

  it('alphabetical sorts ascending by the full id', () => {
    expect(sortComboMembers(models, 'alphabetical')).toEqual([
      'cutad/glm-5',
      'ds/deepseek-chat',
      'oc/gpt-4o'
    ])
  })

  it('alphabetical-desc is the exact reverse', () => {
    expect(sortComboMembers(models, 'alphabetical-desc')).toEqual([
      'oc/gpt-4o',
      'ds/deepseek-chat',
      'cutad/glm-5'
    ])
  })

  it('latency puts the fastest TTFT first and the unmeasured last', () => {
    // ds has the lowest TTFT (400), oc is next (900), cutad has no sample at
    // all — unproven, not slow, so it goes to the end rather than the front.
    expect(sortComboMembers(models, 'latency', META)).toEqual([
      'ds/deepseek-chat',
      'oc/gpt-4o',
      'cutad/glm-5'
    ])
  })

  it('latency falls back to total when TTFT was never observed', () => {
    const onlyTotal: ModelMetaMap = {
      'a|slow': {
        provider: 'a', model: 'slow', ttftMs: 0, totalMs: 9000, samples: 3,
        contextWindow: 0, kind: 'llm', vision: false, reasoning: false, audioInput: false
      },
      'a|fast': {
        provider: 'a', model: 'fast', ttftMs: 0, totalMs: 1000, samples: 3,
        contextWindow: 0, kind: 'llm', vision: false, reasoning: false, audioInput: false
      }
    }
    expect(sortComboMembers(['a/slow', 'a/fast'], 'latency', onlyTotal)).toEqual(['a/fast', 'a/slow'])
  })

  it('latency with no data at all leaves the order alone', () => {
    expect(sortComboMembers(models, 'latency', {})).toEqual(models)
    expect(sortComboMembers(models, 'latency')).toEqual(models)
  })

  it('shuffle keeps every member exactly once', () => {
    const many = Array.from({ length: 40 }, (_, i) => `p/m${i}`)
    const shuffled = sortComboMembers(many, 'shuffle')
    expect(shuffled).toHaveLength(many.length)
    expect([...shuffled].sort()).toEqual([...many].sort())
  })

  it('context-window sorts descending, unknown windows last', () => {
    // gpt-4o declares 128k in the meta map; a model with no entry declares
    // nothing and lands at the end rather than being dropped.
    const out = sortComboMembers(['zz/unknown-model', 'oc/gpt-4o'], 'context-window', META)
    expect(out[0]).toBe('oc/gpt-4o')
    expect(out[1]).toBe('zz/unknown-model')
  })

  it('context-window with no meta leaves the order alone', () => {
    const input = ['oc/gpt-4o', 'zz/unknown-model']
    expect(sortComboMembers(input, 'context-window')).toEqual(input)
  })

  it('capabilities floats vision and reasoning to the front, stably', () => {
    const out = sortComboMembers(
      ['zz/plain-chat', 'oc/gpt-4o', 'aa/also-plain'],
      'capabilities',
      META
    )
    // gpt-4o carries both flags in the meta map, so it leads; the two plain
    // models keep their relative order because the sort is stable.
    expect(out[0]).toBe('oc/gpt-4o')
    expect(out.slice(1)).toEqual(['zz/plain-chat', 'aa/also-plain'])
  })

  it('capabilities with no meta leaves the order alone', () => {
    const input = ['zz/plain-chat', 'oc/gpt-4o']
    expect(sortComboMembers(input, 'capabilities')).toEqual(input)
  })

  it('kind puts chat first and media kinds after', () => {
    const kindMeta: ModelMetaMap = {
      'x|some-embedding-model': {
        provider: 'x', model: 'some-embedding-model', ttftMs: 0, totalMs: 0, samples: 0,
        contextWindow: 0, kind: 'embedding', vision: false, reasoning: false, audioInput: false
      },
      'x|plain-chat': {
        provider: 'x', model: 'plain-chat', ttftMs: 0, totalMs: 0, samples: 0,
        contextWindow: 0, kind: 'llm', vision: false, reasoning: false, audioInput: false
      }
    }
    const out = sortComboMembers(['x/some-embedding-model', 'x/plain-chat'], 'kind', kindMeta)
    expect(out[0]).toBe('x/plain-chat')
    expect(out[1]).toBe('x/some-embedding-model')
  })

  it('kind treats an unknown kind as chat rather than last', () => {
    // A custom model whose kind nothing declared is still a chat model in
    // practice, so it sorts with the chat group instead of after the media.
    const kindMeta: ModelMetaMap = {
      'x|mystery': {
        provider: 'x', model: 'mystery', ttftMs: 0, totalMs: 0, samples: 0,
        contextWindow: 0, kind: '', vision: false, reasoning: false, audioInput: false
      },
      'x|embed': {
        provider: 'x', model: 'embed', ttftMs: 0, totalMs: 0, samples: 0,
        contextWindow: 0, kind: 'embedding', vision: false, reasoning: false, audioInput: false
      }
    }
    const out = sortComboMembers(['x/embed', 'x/mystery'], 'kind', kindMeta)
    expect(out[0]).toBe('x/mystery')
  })

  it('does not mutate the input', () => {
    const input = ['oc/gpt-4o', 'ds/deepseek-chat']
    const copy = [...input]
    sortComboMembers(input, 'alphabetical')
    sortComboMembers(input, 'shuffle')
    expect(input).toEqual(copy)
  })
})

describe('isComboSorted', () => {
  const models = ['cutad/glm-5', 'ds/deepseek-chat', 'oc/gpt-4o']

  it('reports true for a list already in the requested order', () => {
    expect(isComboSorted(models, 'alphabetical')).toBe(true)
    expect(isComboSorted(models, 'alphabetical-desc')).toBe(false)
  })

  it('manual and shuffle are never "already sorted"', () => {
    // Shuffle especially: any order is a valid shuffle, and claiming otherwise
    // would make the control look broken on the one arbitrary option.
    expect(isComboSorted(models, 'manual')).toBe(true)
    expect(isComboSorted(models, 'shuffle')).toBe(true)
  })

  it('a single-member list is always sorted', () => {
    expect(isComboSorted(['ds/x'], 'alphabetical')).toBe(true)
  })

  it('latency uses the same map the sort does', () => {
    const ordered = ['ds/deepseek-chat', 'oc/gpt-4o', 'cutad/glm-5']
    expect(isComboSorted(ordered, 'latency', META)).toBe(true)
    expect(isComboSorted([...ordered].reverse(), 'latency', META)).toBe(false)
  })
})

describe('COMBO_SORT_OPTIONS', () => {
  it('offers every key the sort function handles', () => {
    const keys = COMBO_SORT_OPTIONS.map((o) => o.value)
    for (const key of [
      'manual',
      'group-provider',
      'alphabetical',
      'alphabetical-desc',
      'latency',
      'shuffle',
      'context-window',
      'capabilities',
      'kind'
    ]) {
      expect(keys).toContain(key)
    }
  })

  it('gives every option a label and a hint', () => {
    for (const opt of COMBO_SORT_OPTIONS) {
      expect(opt.label.length).toBeGreaterThan(0)
      expect(opt.hint.length).toBeGreaterThan(0)
    }
  })
})

describe('response timeout seconds conversion', () => {
  it('converts dashboard seconds to stored milliseconds', () => {
    expect(secondsToTimeoutMs('30')).toBe(30000)
    expect(secondsToTimeoutMs(1.5)).toBe(1500)
    expect(secondsToTimeoutMs('0.001')).toBe(1)
    expect(secondsToTimeoutMs('')).toBe(0)
    expect(secondsToTimeoutMs(null)).toBe(0)
  })
  it('rejects negative, non-numeric and oversized values', () => {
    for (const bad of [-1, 'abc', NaN, Infinity, MAX_FIRST_TOKEN_TIMEOUT_SECONDS + 1]) {
      expect(secondsToTimeoutMs(bad)).toBeNull()
    }
    expect(secondsToTimeoutMs(MAX_FIRST_TOKEN_TIMEOUT_SECONDS)).toBe(MAX_FIRST_TOKEN_TIMEOUT_SECONDS * 1000)
  })
  it('shows stored milliseconds as seconds', () => {
    expect(timeoutMsToSeconds(2500)).toBe(2.5)
    expect(timeoutMsToSeconds(undefined)).toBe(0)
  })
})

describe('timeout grace period', () => {
  it('treats blank as default (undefined) but 0 as an explicit immediate re-include', () => {
    expect(graceSecondsToMs('')).toBeUndefined()
    expect(graceSecondsToMs(null)).toBeUndefined()
    expect(graceSecondsToMs('0')).toBe(0)
    expect(graceSecondsToMs('90.5')).toBe(90500)
    expect(graceSecondsToMs(-1)).toBeNull()
    expect(graceSecondsToMs('x')).toBeNull()
    expect(graceMsToSeconds(undefined)).toBe('')
    expect(graceMsToSeconds(0)).toBe(0)
    expect(graceMsToSeconds(90500)).toBe(90.5)
  })
  it('sets and clears only the grace key', () => {
    const current = { primary: { firstTokenTimeoutMs: 2000, judgeModel: 'judge' } }
    const set = updateFirstTokenGrace(current, 'primary', 60000)
    expect(set.primary).toEqual({ firstTokenTimeoutMs: 2000, judgeModel: 'judge', firstTokenGraceMs: 60000 })
    expect(current.primary).not.toHaveProperty('firstTokenGraceMs')
    const cleared = updateFirstTokenGrace(set, 'primary', undefined)
    expect(cleared.primary).toEqual(current.primary)
    expect(updateFirstTokenGrace(current, 'primary', -5)).toBe(current)
  })
})
