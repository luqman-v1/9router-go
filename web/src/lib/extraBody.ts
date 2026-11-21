/** Top-level extraBody keys the gateway accepts. Mirrors db.ExtraBodyAllowedKeys (Go). */
export const EXTRA_BODY_SAMPLING_KEYS = [
  'temperature', 'top_p', 'top_k', 'min_p', 'top_a', 'repetition_penalty',
  'frequency_penalty', 'presence_penalty', 'seed', 'stop', 'logit_bias',
  'logprobs', 'top_logprobs', 'max_completion_tokens',
] as const
export const EXTRA_BODY_REASONING_KEYS = ['reasoning', 'reasoning_effort'] as const
export const EXTRA_BODY_ALLOWED_KEYS: readonly string[] = [...EXTRA_BODY_SAMPLING_KEYS, ...EXTRA_BODY_REASONING_KEYS]

export const EXTRA_BODY_EXAMPLE = '{"temperature":0.7,"reasoning":{"effort":"high"}}'

type Check = (v: unknown) => boolean
const isNum: Check = (v) => typeof v === 'number'
const isInt: Check = (v) => Number.isInteger(v)
const isBool: Check = (v) => typeof v === 'boolean'
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const oneOf = (list: string[]): Check => (v) => typeof v === 'string' && list.includes(v)

const EFFORTS = ['max', 'xhigh', 'high', 'medium', 'low', 'minimal', 'none']
const KEY_CHECKS: Record<string, [Check, string]> = {
  temperature: [isNum, 'a number'], top_p: [isNum, 'a number'], top_k: [isInt, 'an integer'],
  min_p: [isNum, 'a number'], top_a: [isNum, 'a number'], repetition_penalty: [isNum, 'a number'],
  frequency_penalty: [isNum, 'a number'], presence_penalty: [isNum, 'a number'],
  seed: [isInt, 'an integer'], top_logprobs: [isInt, 'an integer'], max_completion_tokens: [isInt, 'an integer'],
  logprobs: [isBool, 'true or false'],
  stop: [(v) => typeof v === 'string' || (Array.isArray(v) && v.every((s) => typeof s === 'string')), 'a string or an array of strings'],
  logit_bias: [(v) => isObj(v) && Object.values(v).every(isNum), 'an object of numbers'],
  reasoning_effort: [oneOf(EFFORTS), `one of: ${EFFORTS.join(', ')}`],
}

const REASONING_CHECKS: Record<string, [Check, string]> = {
  effort: [oneOf(EFFORTS), `one of: ${EFFORTS.join(', ')}`],
  max_tokens: [isInt, 'an integer'],
  enabled: [isBool, 'true or false'],
  exclude: [isBool, 'true or false'],
  summary: [oneOf(['auto', 'concise', 'detailed']), 'one of: auto, concise, detailed'],
  context: [oneOf(['auto', 'all_turns', 'current_turn']), 'one of: auto, all_turns, current_turn'],
  mode: [oneOf(['standard', 'pro']), 'one of: standard, pro'],
}

function checkReasoning(v: unknown): string {
  if (!isObj(v)) return '"reasoning" must be an object.'
  for (const [k, val] of Object.entries(v)) {
    const rule = REASONING_CHECKS[k]
    if (!rule) return `"reasoning.${k}" is not accepted. Allowed: ${Object.keys(REASONING_CHECKS).join(', ')}.`
    if (val !== null && !rule[0](val)) return `"reasoning.${k}" must be ${rule[1]}.`
  }
  return ''
}

/** Returns the first problem with a parsed extraBody, or '' when the gateway would accept it. */
export function validateExtraBody(obj: Record<string, unknown>): string {
  for (const [key, val] of Object.entries(obj)) {
    if (!EXTRA_BODY_ALLOWED_KEYS.includes(key)) {
      return `"${key}" is not accepted. Allowed: ${EXTRA_BODY_ALLOWED_KEYS.join(', ')}.`
    }
    if (val === null) continue
    const err = key === 'reasoning' ? checkReasoning(val) : KEY_CHECKS[key][0](val) ? '' : `"${key}" must be ${KEY_CHECKS[key][1]}.`
    if (err) return err
  }
  const r = obj.reasoning
  if (isObj(r) && r.max_tokens != null && (r.effort != null || obj.reasoning_effort != null)) {
    return '"reasoning.max_tokens" cannot be combined with reasoning.effort or reasoning_effort.'
  }
  return ''
}

/**
 * Parses the Add Custom Model "Custom request body" field. Blank means no
 * extraBody; anything else must be a JSON object of allowed keys. The gateway
 * re-validates (size, keys, types); this mirrors it so the operator sees why.
 */
export function parseExtraBodyInput(raw: string): { value?: Record<string, unknown>; error: string } {
  const text = raw.trim()
  if (!text) return { error: '' }
  let v: unknown
  try {
    v = JSON.parse(text)
  } catch (err) {
    return { error: `Invalid JSON: ${err instanceof Error ? err.message : String(err)}` }
  }
  if (!isObj(v)) return { error: 'Custom request body must be a JSON object.' }
  const error = validateExtraBody(v)
  return error ? { error } : { value: v, error: '' }
}
