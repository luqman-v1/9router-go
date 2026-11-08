// Service-kind inference for a model id, ported from upstream
// inferKindFromUnknownModelId (src/app/api/v1/models/route.js).
//
// It exists because the dashboard writes a custom-model row before anything
// knows what the model is. The import paths used to hardcode `type: 'llm'`,
// which the gateway then believed: modelKind() reads the stored `type` first,
// so a row saying "llm" is an LLM even when its id ends in `-tts`. That is how
// veo-3.1-generate-preview, lyria-3.5, gemini-3.8-flash-tts and
// gemini-nano-banana-2.1 ended up listed as chat models on /v1/models.
//
// The regexes are upstream's, in upstream's order — embedding, then tts, then
// image — so a row written here resolves to the same kind the Node gateway
// would infer for the same id.

/** Service kinds a custom-model row can carry. */
export type ModelServiceKind = 'llm' | 'embedding' | 'tts' | 'stt' | 'image' | 'video'

const EMBEDDING_HINT = /embed/i
const TTS_HINT = /tts|speech|audio|voice|kokoro|fastpitch|tacotron/i
const IMAGE_HINT = /image|imagen|dall-?e|flux|sdxl|sd-|stable-diffusion|nano-?banana/i

// A chat model whose id merely contains a media word. Checked before every
// positive hint, because upstream's `audio` branch claims ids like
// stepaudio-2.5-chat and gpt-4o-audio-preview: filing a working chat model as
// TTS removes it from the default listing, which is a worse failure than
// leaving a media model in.
//
// The `audio` spellings are the native audio-in/audio-out chat models — they
// speak the chat protocol and belong in the LLM list, unlike a synthesis model
// that only answers /v1/audio/speech.
const CHAT_DESPITE_MEDIA_WORD =
  /-(chat|instruct|realtime|thinking|reasoner)\b|\bchat-|native-?audio|audio-preview/i

// Not upstream's: upstream has no video hint, so a video id falls through to
// "llm" there. Veo and Lyria are reachable through this gateway's media lanes,
// and a generation model in the chat list is a model a client will call and get
// nothing usable back from.
const VIDEO_HINT = /\bveo\b|veo-|lyria|\bsora\b|sora-|video/i

// Speech recognition is the opposite direction of TTS and must not be caught by
// the "speech" or "audio" hints. Checked first, like notTTSName in the Go
// providers package.
const STT_HINT = /speech[-_]?to[-_]?text|\b(stt|asr|whisper)\b|transcri/i

/**
 * Infers the service kind of a model from its id alone.
 *
 * Returns 'llm' when no hint matches, which is both upstream's fallback and the
 * safe answer: a misclassified chat model disappears from the default listing,
 * so an unknown id stays where a client can still reach it.
 */
export function inferModelKind(modelId: string): ModelServiceKind {
  const id = (modelId || '').trim()
  if (!id) return 'llm'

  if (CHAT_DESPITE_MEDIA_WORD.test(id)) return 'llm'

  // Checked before the TTS hint so "speech" and "audio" cannot claim a
  // recognition id. 'stt' is one of the kinds the gateway's own
  // nonLLMServiceKinds knows, so returning it keeps whisper out of the chat
  // list instead of only steering it away from TTS.
  if (STT_HINT.test(id)) return 'stt'

  if (EMBEDDING_HINT.test(id)) return 'embedding'
  if (TTS_HINT.test(id)) return 'tts'
  if (VIDEO_HINT.test(id)) return 'video'
  if (IMAGE_HINT.test(id)) return 'image'
  return 'llm'
}

/**
 * Builds the `alias|id|type` key a customModels row is stored under, with the
 * type inferred from the id.
 *
 * The key's third segment and the value's `type` field have to agree — the
 * gateway reads the value, the dashboard de-duplicates on the key — so both
 * come from one call rather than two places that can drift.
 */
export function customModelKeyFor(alias: string, modelId: string, storedKind?: string): string {
  // Plain string, not ModelServiceKind: a kv row carries whatever an earlier
  // build or a hand edit wrote, and a delete must address the key that exists.
  const kind = storedKind?.trim() || inferModelKind(modelId)
  return `${alias}|${modelId}|${kind}`
}
