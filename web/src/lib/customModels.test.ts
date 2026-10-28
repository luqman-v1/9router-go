// Guards the "Import from /models" regression: the server refuses an id that
// already addresses a model alias or a combo with a typed 409, and the import
// loop awaited every save inside one try/catch — so the first refusal threw past
// the ids listed after it and one collision ended the whole import. Every id
// after the refused one must still be attempted.
import assert from 'node:assert'
import { describe, it } from 'node:test'
import { importCustomModelsBestEffort, type CustomModelWrite } from './customModels'

function writeFor(id: string, providerAlias = 'node'): CustomModelWrite {
  return {
    id,
    key: `${providerAlias}|${id}|llm`,
    value: { id, providerAlias, type: 'llm' },
  }
}

describe('importCustomModelsBestEffort', () => {
  it('stores every row when nothing is refused', async () => {
    const saved: string[] = []
    const outcome = await importCustomModelsBestEffort(
      ['m1', 'm2', 'm3'].map((id) => writeFor(id)),
      async (key) => {
        saved.push(key)
        return { success: true }
      },
    )

    assert.deepEqual(saved, ['node|m1|llm', 'node|m2|llm', 'node|m3|llm'])
    assert.equal(outcome.imported, 3)
    assert.deepEqual(outcome.refused, [])
  })

  it('keeps importing after a refusal instead of aborting the batch', async () => {
    const attempted: string[] = []
    const outcome = await importCustomModelsBestEffort(
      ['m1', 'claude-opus-4-6', 'm2'].map((id) => writeFor(id)),
      async (key) => {
        const id = key.split('|')[1]
        attempted.push(id)
        if (id === 'claude-opus-4-6') {
          throw new Error('a model alias "claude-opus-4-6" already resolves this name')
        }
        return { success: true }
      },
    )

    assert.deepEqual(
      attempted,
      ['m1', 'claude-opus-4-6', 'm2'],
      'the id listed after the refused one must still be attempted',
    )
    assert.equal(outcome.imported, 2, 'only the successful writes count as imported')
    assert.deepEqual(outcome.refused, [
      {
        id: 'claude-opus-4-6',
        reason: 'a model alias "claude-opus-4-6" already resolves this name',
      },
    ])
  })

  it('records every refusal when several rows collide', async () => {
    const outcome = await importCustomModelsBestEffort(
      ['agy', 'm1', 'agy'].map((id) => writeFor(id, 'xai')),
      async () => {
        throw new Error('a combo named "agy" already answers to this name')
      },
    )

    assert.equal(outcome.imported, 0)
    assert.deepEqual(
      outcome.refused.map((r) => r.id),
      ['agy', 'm1', 'agy'],
    )
  })

  it('reports a non-Error rejection as text', async () => {
    const outcome = await importCustomModelsBestEffort([writeFor('m1')], async () => {
      throw 'HTTP 500'
    })

    assert.equal(outcome.imported, 0)
    assert.deepEqual(outcome.refused, [{ id: 'm1', reason: 'HTTP 500' }])
  })

  it('saves nothing for an empty batch', async () => {
    let calls = 0
    const outcome = await importCustomModelsBestEffort([], async () => {
      calls += 1
      return { success: true }
    })

    assert.equal(calls, 0)
    assert.deepEqual(outcome, { imported: 0, refused: [] })
  })
})
