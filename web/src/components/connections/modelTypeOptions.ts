/**
 * The Model type options the Add Custom Model modal offers.
 *
 * They live outside the component so the set is testable without rendering the
 * modal (the dashboard's `bun test` has no DOM) and so a second caller that has
 * to label a kind cannot invent its own order.
 *
 * Upstream decolua/9router@850eb573: the ids are the gateway's service kinds,
 * which is what `POST /api/models/test` dispatches a probe on and what the
 * stored row's `type` becomes.
 */

/** The kinds a custom model can be filed as. `llm` is the default. */
export type CustomModelType = 'llm' | 'embedding' | 'image' | 'stt' | 'tts'

export const MODEL_TYPE_OPTIONS: ReadonlyArray<{ value: CustomModelType; label: string }> = [
  { value: 'llm', label: 'Chat / LLM' },
  { value: 'embedding', label: 'Embedding' },
  { value: 'image', label: 'Image' },
  { value: 'stt', label: 'Speech to text (STT)' },
  { value: 'tts', label: 'Text to speech (TTS)' },
]