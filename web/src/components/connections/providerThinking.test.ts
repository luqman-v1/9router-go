import { describe, expect, test } from 'bun:test'
import {
  computeProviderThinkingOptions,
  formatThinkingOptionLabel,
  resolveProviderThinkingSuffix,
} from './providerThinking'

describe('computeProviderThinkingOptions', () => {
  test('compatible node with 0 models always exposes auto, off, on', () => {
    expect(
      computeProviderThinkingOptions({
        modelThinkingLevels: [],
        isCompatibleNode: true,
        currentMode: 'auto',
      }),
    ).toEqual(['auto', 'off', 'on'])
  })

  test('compatible node with non-reasoning custom models exposes auto, off, on', () => {
    expect(
      computeProviderThinkingOptions({
        modelThinkingLevels: [[], undefined],
        isCompatibleNode: true,
        currentMode: 'off',
      }),
    ).toEqual(['auto', 'off', 'on'])
  })

  test('compatible node with reasoning models includes off, on, and discovered levels', () => {
    expect(
      computeProviderThinkingOptions({
        modelThinkingLevels: [['none', 'low', 'medium', 'high']],
        isCompatibleNode: true,
        currentMode: 'auto',
      }),
    ).toEqual(['auto', 'off', 'on', 'low', 'medium', 'high'])
  })

  test('built-in provider with disable-capable models includes off after auto', () => {
    expect(
      computeProviderThinkingOptions({
        modelThinkingLevels: [['none', 'low', 'high'], ['low', 'medium']],
        isCompatibleNode: false,
        currentMode: 'auto',
      }),
    ).toEqual(['auto', 'off', 'low', 'high', 'medium'])
  })

  test('built-in provider with mandatory-reasoning models omits off', () => {
    expect(
      computeProviderThinkingOptions({
        modelThinkingLevels: [['minimal', 'low', 'medium', 'high']],
        isCompatibleNode: false,
        currentMode: 'auto',
      }),
    ).toEqual(['auto', 'minimal', 'low', 'medium', 'high'])
  })

  test('built-in provider with no reasoning models returns null on auto', () => {
    expect(
      computeProviderThinkingOptions({
        modelThinkingLevels: [[], []],
        isCompatibleNode: false,
        currentMode: 'auto',
      }),
    ).toBeNull()
  })

  test('built-in non-reasoning provider keeps persisted non-auto override visible and resettable', () => {
    expect(
      computeProviderThinkingOptions({
        modelThinkingLevels: [[]],
        isCompatibleNode: false,
        currentMode: 'off',
      }),
    ).toEqual(['auto', 'off', 'on'])
  })
})

describe('resolveProviderThinkingSuffix', () => {
  test('never appends auto, off, or on as a model suffix', () => {
    const levels = ['none', 'low', 'high', 'off', 'on']
    expect(resolveProviderThinkingSuffix('', levels)).toBeNull()
    expect(resolveProviderThinkingSuffix('auto', levels)).toBeNull()
    expect(resolveProviderThinkingSuffix('off', levels)).toBeNull()
    expect(resolveProviderThinkingSuffix('on', levels)).toBeNull()
  })

  test('appends supported effort levels and skips unsupported ones', () => {
    expect(resolveProviderThinkingSuffix('high', ['none', 'low', 'high'])).toBe('high')
    expect(resolveProviderThinkingSuffix('xhigh', ['none', 'low', 'high'])).toBeNull()
    expect(resolveProviderThinkingSuffix('high', undefined)).toBeNull()
  })
})

describe('formatThinkingOptionLabel', () => {
  test('capitalizes the option label', () => {
    expect(formatThinkingOptionLabel('auto')).toBe('Auto')
    expect(formatThinkingOptionLabel('off')).toBe('Off')
    expect(formatThinkingOptionLabel('xhigh')).toBe('Xhigh')
  })
})
