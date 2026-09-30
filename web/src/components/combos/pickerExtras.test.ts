import assert from 'node:assert'
import { describe, it } from 'node:test'
import { loadPickerExtras, type PickerExtrasSource } from './pickerExtras'

function source(overrides: Partial<PickerExtrasSource> = {}): PickerExtrasSource {
  return {
    getModelAliases: async () => ({ aliases: { cbcn: 'codebuddy-cn/sonnet' } }),
    getCustomModels: async () => ({ models: [{ id: 'my-model', providerAlias: 'openai' }] }),
    getDisabledModels: async () => ({ disabled: { openai: ['gpt-4-32k'] } }),
    ...overrides,
  }
}

describe('loadPickerExtras', () => {
  it('unwraps the alias, custom and disabled envelopes into one result', async () => {
    const extras = await loadPickerExtras(source())

    assert.deepEqual(extras.modelAliases, { cbcn: 'codebuddy-cn/sonnet' })
    assert.deepEqual(
      extras.customModels.map((m) => m.id),
      ['my-model']
    )
    assert.deepEqual(extras.disabledModels, { openai: ['gpt-4-32k'] })
  })

  it('keeps the endpoints that answered when one of them fails', async () => {
    const extras = await loadPickerExtras(
      source({
        getCustomModels: async () => {
          throw new Error('500')
        },
      })
    )

    assert.deepEqual(extras.modelAliases, { cbcn: 'codebuddy-cn/sonnet' })
    assert.deepEqual(extras.customModels, [])
    assert.deepEqual(extras.disabledModels, { openai: ['gpt-4-32k'] })
  })

  it('returns empty maps when every endpoint fails', async () => {
    const fail = async () => {
      throw new Error('offline')
    }
    const extras = await loadPickerExtras(
      source({ getModelAliases: fail, getCustomModels: fail, getDisabledModels: fail })
    )

    assert.deepEqual(extras, { modelAliases: {}, customModels: [], disabledModels: {} })
  })

  it('accepts the bare map shapes the go port serves', async () => {
    const extras = await loadPickerExtras(
      source({
        getDisabledModels: async () => ({ claude: ['old-model'] }),
        getCustomModels: async () => [{ id: 'bare-model' }],
      })
    )

    assert.deepEqual(extras.customModels.map((m) => m.id), ['bare-model'])
    assert.deepEqual(extras.disabledModels, { claude: ['old-model'] })
  })
})