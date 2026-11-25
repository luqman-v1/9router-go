import assert from 'node:assert'
import { describe, it } from 'node:test'
import {
  customJsonForm,
  parseCustomJsonForm,
  switchCustomJsonTemplateMode,
  type CustomJsonExtraField,
} from './customJsonForm'

const field = (name: string, value: string, type: 'text' | 'json' = 'text'): CustomJsonExtraField => ({
  id: name || 'empty',
  name,
  value,
  type,
})

describe('custom JSON default template and optional fields', () => {
  it('saves a standard chat endpoint without any extra fields', () => {
    const form = customJsonForm()
    assert.equal(form.templateMode, 'fields')
    assert.deepEqual(form.extraFields, [])
    const config = parseCustomJsonForm(form)
    assert.deepEqual(config.requestTemplate, { model: '{{model}}', messages: '{{messages}}' })
    assert.equal(config.responsePath, 'choices[0].message.content')
  })

  it('ignores an untouched added row and keeps a named empty text value', () => {
    const config = parseCustomJsonForm({ ...customJsonForm(), extraFields: [field('', ''), field('optional_label', '')] })
    assert.deepEqual(config.requestTemplate, { model: '{{model}}', messages: '{{messages}}', optional_label: '' })
  })

  it('adds text, placeholders and typed JSON values alongside model and messages', () => {
    const config = parseCustomJsonForm({
      ...customJsonForm(),
      extraFields: [
        field('project_id', 'demo-project'),
        field('question', '{{prompt}}'),
        field('temperature', '0.5', 'json'),
        field('enabled', 'false', 'json'),
        field('options', '{"tags":["demo"]}', 'json'),
      ],
    })
    assert.deepEqual(config.requestTemplate, {
      model: '{{model}}',
      messages: '{{messages}}',
      project_id: 'demo-project',
      question: '{{prompt}}',
      temperature: 0.5,
      enabled: false,
      options: { tags: ['demo'] },
    })
  })

  it('restores saved extra fields and stops sending a removed one', () => {
    const config = parseCustomJsonForm({ ...customJsonForm(), extraFields: [field('project_id', 'demo-project'), field('temperature', '0', 'json')] })
    const restored = customJsonForm(config)
    assert.equal(restored.templateMode, 'fields')
    assert.deepEqual(
      restored.extraFields.map(({ name, type }) => ({ name, type })),
      [{ name: 'project_id', type: 'text' }, { name: 'temperature', type: 'json' }],
    )
    restored.extraFields = restored.extraFields.filter(({ name }) => name !== 'project_id')
    const template = parseCustomJsonForm(restored).requestTemplate
    assert.equal('project_id' in template, false)
    assert.equal(template.temperature, 0)
  })

  for (const [label, extraFields] of [
    ['overrides model', [field('model', 'override')]],
    ['overrides messages', [field('messages', 'override')]],
    ['duplicate name', [field('x', 'a'), field('x', 'b')]],
    ['value without a name', [field('', 'demo-project')]],
    ['invalid JSON', [field('temperature', 'invalid JSON', 'json')]],
    ['unsafe name', [field('__proto__', 'bad')]],
  ] as const) {
    it(`rejects an added field that ${label}`, () => {
      assert.throws(() => parseCustomJsonForm({ ...customJsonForm(), extraFields: [...extraFields] }))
    })
  }

  it('preserves an existing business-only template in JSON mode', () => {
    const existing = { requestTemplate: { project_id: '{{prompt}}' }, responsePath: 'data.answer' }
    const restored = customJsonForm(existing)
    assert.equal(restored.templateMode, 'json')
    assert.deepEqual(parseCustomJsonForm(restored).requestTemplate, existing.requestTemplate)
  })

  it('round-trips rows through the JSON editor', () => {
    const withRow = { ...customJsonForm(), extraFields: [field('project_id', 'demo')] }
    const json = switchCustomJsonTemplateMode(withRow, 'json')
    assert.match(json.requestTemplateText, /"project_id": "demo"/)
    const back = switchCustomJsonTemplateMode(json, 'fields')
    assert.deepEqual(back.extraFields.map((f) => f.name), ['project_id'])
  })
})
