import { describe, expect, test } from 'bun:test'
import { createEscapeStack } from './escapeStack'

type Listener = (e: KeyboardEvent) => void

function fakeTarget() {
  const listeners = new Set<Listener>()
  return {
    listeners,
    addEventListener: (_: 'keydown', l: Listener) => void listeners.add(l),
    removeEventListener: (_: 'keydown', l: Listener) => void listeners.delete(l),
    press(key: string) {
      for (const l of [...listeners]) l({ key } as KeyboardEvent)
    },
  }
}

describe('escape stack', () => {
  test('Escape closes only the topmost dialog', () => {
    const target = fakeTarget()
    const stack = createEscapeStack(target)
    const closed: string[] = []
    stack.push(() => closed.push('edit combo'))
    const popPicker = stack.push(() => closed.push('model picker'))

    target.press('Escape')
    expect(closed).toEqual(['model picker'])

    popPicker()
    target.press('Escape')
    expect(closed).toEqual(['model picker', 'edit combo'])
  })

  test('other keys are ignored', () => {
    const target = fakeTarget()
    const stack = createEscapeStack(target)
    let calls = 0
    stack.push(() => calls++)
    target.press('Enter')
    expect(calls).toBe(0)
  })

  test('removing a lower dialog keeps the top one in charge', () => {
    const target = fakeTarget()
    const stack = createEscapeStack(target)
    const closed: string[] = []
    const popLower = stack.push(() => closed.push('lower'))
    stack.push(() => closed.push('upper'))
    popLower()
    target.press('Escape')
    expect(closed).toEqual(['upper'])
  })

  test('the page listener is detached once no dialog is open', () => {
    const target = fakeTarget()
    const stack = createEscapeStack(target)
    const pop = stack.push(() => {})
    expect(target.listeners.size).toBe(1)
    pop()
    pop()
    expect(target.listeners.size).toBe(0)
  })
})
