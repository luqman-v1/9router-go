/**
 * The Add Custom Model modal makes the model's type an explicit choice.
 *
 * Upstream decolua/9router@850eb573 replaced a capability-style toggle with a
 * Model type selector: enabling one capability changed the model's type while
 * Test still probed it as chat, so a working chat model disappeared from the
 * provider's list. Here the type is stored in the row's `type` and in the
 * `alias|id|type` key, and the key is what a later delete addresses, so a
 * mismatch is not cosmetic — the row becomes unreachable from the screen that
 * lists it.
 *
 * The flow is asserted through the compiled module because the dashboard's
 * `bun test` has no DOM: what is checked is the contract the modal and its
 * caller agree on — the selected type, the key it writes, and the payload the
 * batch endpoint receives.
 */

import { describe, expect, it } from 'bun:test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { compile } from 'svelte/compiler'
import { customModelKeyFor } from '../../lib/modelKindInference'
import { MODEL_TYPE_OPTIONS } from './modelTypeOptions'

const MODAL_SRC = readFileSync(join(import.meta.dirname, 'AddCustomModelModal.svelte'), 'utf8')
const PROVIDER_SRC = readFileSync(join(import.meta.dirname, 'ProviderDetailView.svelte'), 'utf8')

/** The modal's script as the browser receives it, so the assertions are about behaviour. */
function compiledModal(): string {
  return compile(MODAL_SRC, { filename: 'AddCustomModelModal.svelte', generate: 'client' }).js.code
}

describe('explicit custom model type', () => {
  it('offers every kind the gateway can dispatch a probe for', () => {
    expect(MODEL_TYPE_OPTIONS.map((o) => o.value)).toEqual(['llm', 'embedding', 'image', 'stt', 'tts'])
  })

  it('defaults to llm and resets to it when the dialog is reopened', () => {
    const js = compiledModal()
    expect(js).toContain("let modelType = $.state('llm');")
    expect(js).toContain("$.set(modelType, editing()?.type ?? 'llm', true);")
  })

  it('passes the selected type to onSave instead of leaving the caller to infer it', () => {
    expect(compiledModal()).toContain(
      '$$props.onSave($.get(cleanId), { ...$.get(caps) }, { ...$.get(limits) }, $.get(modelType), $.get(extraBody).value);',
    )
  })

  it('offers the custom request body on OpenRouter single chat saves only and forwards it', () => {
    expect(MODAL_SRC).toContain("providerAlias === 'openrouter' && modelType === 'llm' && !isPaste")
    expect(PROVIDER_SRC).toContain('...(extraBody ? { extraBody } : {}),')
  })

  it('probes the model through the selected type, so Test and Save cannot disagree', () => {
    expect(compiledModal()).toContain('api.testModel(`${$$props.providerAlias}/${$.get(cleanId)}`, $.get(modelType))')
  })

  it('sends the selected type on the batch path, once per entry', () => {
    const js = compiledModal()
    expect(js).toContain('type: $.get(modelType),')
    // Capabilities belong to a chat model: an embedding row that carried `caps`
    // would be a chat row wearing another type.
    expect(js).toContain(': { id, type: $.get(modelType) })')
  })

  it('shows the chat-only fields for a chat model only', () => {
    // Capabilities and the token limits are one block, and the limits
    // contradiction is only checked on a chat model, which is the only kind
    // that carries a context window.
    expect(compiledModal()).toContain("if ($.get(modelType) === 'llm')")
  })

  it('locks the selector while a save or a test is in flight', () => {
    expect(compiledModal()).toContain(
      "select.disabled = !!editing() || $.get(saving) || $.get(bulkSaving) || $.get(testStatus) === 'testing';",
    )
  })

  it('stores the type the operator chose, in the key and in the value', () => {
    // The handler takes the modal's fourth argument and does not re-derive the
    // type from an id the operator already declared.
    expect(PROVIDER_SRC).toMatch(/async function submitAddCustomModel\([\s\S]*?modelType\?: CustomModelType,/)
    expect(PROVIDER_SRC).toContain('const kind = modelType ?? inferModelKind(modelId)')
    expect(PROVIDER_SRC).toContain('customModelKeyFor(storageAlias, modelId, kind)')
    expect(PROVIDER_SRC).toContain('type: kind,')
    expect(PROVIDER_SRC).toContain("...(kind !== 'llm' ? {} : caps ? { caps } : {}),")
  })

  it('keeps the key third segment and the stored type in agreement', () => {
    for (const kind of ['llm', 'embedding', 'image', 'stt', 'tts'] as const) {
      expect(customModelKeyFor('oc', 'whisper-1', kind)).toBe(`oc|whisper-1|${kind}`)
    }
  })

  it('keeps an audio chat id a chat model when the operator leaves the default', () => {
    // The regression upstream fixes: a chat model whose id mentions audio was
    // filed as speech-to-text and then vanished from the chat listing.
    expect(customModelKeyFor('oc', 'gpt-4o-audio-preview', 'llm')).toBe('oc|gpt-4o-audio-preview|llm')
  })
})