import { describe, expect, it } from 'bun:test'

import { customModelKeyFor, inferModelKind } from './modelKindInference'

// The dashboard writes a custom-model row before anything knows what the model
// is, and the gateway believes the stored `type`: modelKind() reads it first, so
// a row saying "llm" is an LLM even when its id ends in `-tts`.
//
// Hardcoding `type: 'llm'` on the import paths is how veo-3.1-generate-preview,
// lyria-3.5, gemini-3.8-flash-tts and gemini-nano-banana-2.1 came to be listed
// as chat models on /v1/models while their endpoints serve video, music, speech
// and images.
describe('inferModelKind', () => {
  it('classifies the models that leaked into the chat listing', () => {
    expect(inferModelKind('veo-3.1-generate-preview')).toBe('video')
    expect(inferModelKind('gemini-3.8-flash-tts')).toBe('tts')
    expect(inferModelKind('gemini-nano-banana-2.1')).toBe('image')
  })

  // Lyria is a music model: it answers /v1/audio/music, so "video" would be a
  // lie, and the honest kind does not help either — "music" is a
  // serviceKindLabels entry but not a nonLLMServiceKinds one, so a row filed
  // that way stays in the LLM list regardless. Upstream agrees: its
  // MODEL_TYPE_TO_KIND has no music entry. It therefore stays llm until the
  // gateway gives music a lane of its own, and this pins that so a later
  // "fix" to video has to argue with the lane instead of the name.
  it('leaves a music model as llm rather than guessing video', () => {
    expect(inferModelKind('lyria-3.5')).toBe('llm')
    expect(inferModelKind('lyria-2')).toBe('llm')
  })

  it('leaves chat models as llm', () => {
    for (const id of [
      'gemini-4-argon',
      'gemini-3.8-flash',
      'gemini-3.1-pro-preview',
      'gemini-3.6-flash',
      'gemini-3.5-flash-lite',
      'claude-opus-5',
      'gpt-4o',
      'mimo-v2.5-pro',
      'gemini-omni-1.2-flash',
      'mimo-v2-omni',
    ]) {
      expect(inferModelKind(id)).toBe('llm')
    }
  })

  // Upstream's `audio` branch claims these, and filing a working chat model as
  // TTS removes it from the default listing — a worse failure than leaving a
  // media model in. Both ids were caught misclassified while building this.
  it('keeps audio-capable chat models in the llm list', () => {
    expect(inferModelKind('stepaudio-2.5-chat')).toBe('llm')
    expect(inferModelKind('stepaudio-2.5-realtime')).toBe('llm')
    expect(inferModelKind('gpt-4o-audio-preview')).toBe('llm')
    expect(inferModelKind('gemini-2.5-flash-native-audio-preview-09-17')).toBe('llm')
  })

  // "speech" and "audio" are synthesis signals (minimax speech-2.8-hd), so the
  // recognition direction has to be excluded explicitly or whisper reads as TTS.
  it('classifies speech recognition as stt, never tts', () => {
    for (const id of [
      'whisper-1',
      'whisper-large-v3',
      'stt-1',
      'asr-pro',
      'speech-to-text',
      'speech_to_text',
      'transcribe-v1',
      'stepaudio-2.5-asr',
    ]) {
      expect(inferModelKind(id)).toBe('stt')
    }
  })

  it('classifies synthesis models whose ids name only the architecture', () => {
    expect(inferModelKind('tts-1')).toBe('tts')
    expect(inferModelKind('speech-2.8-hd')).toBe('tts')
    expect(inferModelKind('kokoro')).toBe('tts')
    expect(inferModelKind('fastpitch')).toBe('tts')
    expect(inferModelKind('tacotron2')).toBe('tts')
  })

  it('classifies embeddings and image generation', () => {
    expect(inferModelKind('text-embedding-004')).toBe('embedding')
    expect(inferModelKind('gemini-embedding-001')).toBe('embedding')
    expect(inferModelKind('gemini-2.5-flash-image')).toBe('image')
    expect(inferModelKind('dall-e-3')).toBe('image')
    expect(inferModelKind('flux-pro')).toBe('image')
  })

  // llm is upstream's fallback and the safe answer: a misclassified chat model
  // vanishes from the listing, so an unknown id stays reachable.
  it('falls back to llm for an id with no signal', () => {
    expect(inferModelKind('sonic-2')).toBe('llm')
    expect(inferModelKind('')).toBe('llm')
    expect(inferModelKind('   ')).toBe('llm')
  })
})

describe('customModelKeyFor', () => {
  it('infers the type segment from the id', () => {
    expect(customModelKeyFor('studio', 'gemini-3.8-flash-tts')).toBe('studio|gemini-3.8-flash-tts|tts')
    expect(customModelKeyFor('studio', 'gemini-4-argon')).toBe('studio|gemini-4-argon|llm')
    expect(customModelKeyFor('studio', 'veo-3.1-generate-preview')).toBe('studio|veo-3.1-generate-preview|video')
  })

  // A delete has to address the key that exists, not the one inference would
  // pick: rows written by an earlier build carry `llm` for a TTS id, and
  // re-inferring would build a key that was never stored.
  it('prefers a stored kind over inference', () => {
    expect(customModelKeyFor('studio', 'gemini-3.8-flash-tts', 'llm')).toBe('studio|gemini-3.8-flash-tts|llm')
    expect(customModelKeyFor('studio', 'gemini-4-argon', 'llm')).toBe('studio|gemini-4-argon|llm')
  })

  it('falls back to inference when the stored kind is absent or blank', () => {
    expect(customModelKeyFor('studio', 'gemini-3.8-flash-tts', undefined)).toBe('studio|gemini-3.8-flash-tts|tts')
    expect(customModelKeyFor('studio', 'gemini-3.8-flash-tts', '  ')).toBe('studio|gemini-3.8-flash-tts|tts')
  })
})
