// Guards the bulk-model parser: what the operator pasted must arrive as the ids
// they meant, and nothing may be dropped without being reported — an id that
// quietly disappears between the paste and the table looks exactly like a typo,
// so it gets pasted again.
import assert from 'node:assert'
import { describe, it } from 'node:test'
import { parseModelIds, summarizeBulkModels } from './bulk-models'

describe('parseModelIds: separators', () => {
  const cases: { name: string; input: string; want: string[] }[] = [
    { name: 'newlines', input: 'glm-5.3\nglm-4.7\ngpt-5.2', want: ['glm-5.3', 'glm-4.7', 'gpt-5.2'] },
    { name: 'spaces', input: 'glm-5.3 glm-4.7 gpt-5.2', want: ['glm-5.3', 'glm-4.7', 'gpt-5.2'] },
    { name: 'commas', input: 'glm-5.3,glm-4.7,gpt-5.2', want: ['glm-5.3', 'glm-4.7', 'gpt-5.2'] },
    { name: 'semicolons', input: 'glm-5.3; glm-4.7', want: ['glm-5.3', 'glm-4.7'] },
    {
      name: 'a mixed paste from a pricing table',
      input: 'glm-5.3,\t glm-4.7\n\ngpt-5.2;  claude-sonnet-4.5',
      want: ['glm-5.3', 'glm-4.7', 'gpt-5.2', 'claude-sonnet-4.5'],
    },
    { name: 'surrounding whitespace is trimmed', input: '  glm-5.3  ,\n  glm-4.7 ', want: ['glm-5.3', 'glm-4.7'] },
    { name: "colons with commas split on the comma", input: "model1:free,modelX:paid", want: ["model1:free", "modelX:paid"] },
    { name: "colons with spaces split on spaces", input: "model1:free modelX:paid model2:xvertor", want: ["model1:free", "modelX:paid", "model2:xvertor"] },
    { name: 'an empty paste plans nothing', input: '   \n\t ', want: [] },
  ]

  for (const tc of cases) {
    it(tc.name, () => {
      assert.deepStrictEqual(parseModelIds(tc.input).ids, tc.want)
    })
  }
})

describe('parseModelIds: alias prefix', () => {
  it('strips the <alias>/ prefix so both paste shapes land the same rows', () => {
    const out = parseModelIds('nara/glm-5.3\nglm-4.7', 'nara')
    assert.deepStrictEqual(out.ids, ['glm-5.3', 'glm-4.7'])
  })

  it('reads a spaced "<alias> / <id>" as two tokens, not as a prefixed id', () => {
    // Whitespace is a separator, so this paste is the alias token plus the id and a
    // stray slash. The counts in the result line are where such a typo surfaces.
    const out = parseModelIds('nara / glm-5.3', 'nara')
    assert.deepStrictEqual(out.ids, ['nara', 'glm-5.3'])
    assert.deepStrictEqual(out.invalid, ['/'])
  })

  it('strips the matching prefix and preserves other namespaced model ids', () => {
    const out = parseModelIds('nara/glm-5.3,other/glm-4.7', 'nara')
    assert.deepStrictEqual(out.ids, ['glm-5.3', 'other/glm-4.7'])
  })

  it('ignores an alias-only entry', () => {
    assert.deepStrictEqual(parseModelIds('nara/', 'nara').ids, [])
  })
})

describe('parseModelIds: duplicates and invalids are reported, never silently dropped', () => {
  it('keeps the first occurrence and reports each repeat', () => {
    const out = parseModelIds('glm-5.3\nGLM-5.3\nglm-5.3')
    assert.deepStrictEqual(out.ids, ['glm-5.3', 'GLM-5.3'])
    assert.deepStrictEqual(out.duplicates, ['glm-5.3'])
  })

  it('accepts namespaced model ids carrying slashes', () => {
    const out = parseModelIds('deepseek/deepseek-v4-1-flash,glm/5.3')
    assert.deepStrictEqual(out.ids, ['deepseek/deepseek-v4-1-flash', 'glm/5.3'])
    assert.deepStrictEqual(out.invalid, [])
  })

  it('reports leading or trailing slashes and consecutive slashes as invalid', () => {
    const out = parseModelIds('/invalid,invalid/,a//b')
    assert.deepStrictEqual(out.ids, [])
    assert.deepStrictEqual(out.invalid, ['/invalid', 'invalid/', 'a//b'])
  })

  it('reports an invalid id once however often it was pasted', () => {
    assert.deepStrictEqual(parseModelIds('a|b a|b a|b').invalid, ['a|b'])
  })

  it('accepts the characters real model ids use', () => {
    const out = parseModelIds('claude-sonnet-4.5,deepseek/deepseek-v4-1-flash,deepseek-v3.2:free,gpt-5.2-2026-01-31,grok_4.1')
    assert.deepStrictEqual(out.invalid, [])
    assert.strictEqual(out.ids.length, 5)
  })
})

describe('summarizeBulkModels', () => {
  it('counts what the server confirmed, not what was planned', () => {
    const plan = parseModelIds('a,b,c')
    assert.strictEqual(summarizeBulkModels(plan, 2), '2 added, 1 refused')
  })

  it('names the reasons the paste itself carried', () => {
    const plan = parseModelIds('a,a,b|c')
    assert.strictEqual(summarizeBulkModels(plan, 1), '1 added, 1 duplicate, 1 invalid')
  })

  it('stays a plain count when everything landed', () => {
    assert.strictEqual(summarizeBulkModels(parseModelIds('a,b'), 2), '2 added')
  })
})
