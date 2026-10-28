/**
 * Header-override field validation for the dashboard (issue #490).
 *
 * These are the same rules the server applies in
 * `internal/db/provider_overrides.go` — the RFC 7230 §3.2.6 token grammar for
 * a name, the "would this survive being written as an HTTP field line?"
 * rule for a value, and the same value-length cap. The modal used to carry
 * its own, narrower copy (alphanumeric names, CR/LF-only values), so it
 * rejected headers the server accepts and accepted values the server
 * refuses; the operator found out from a 400 instead of in the field.
 *
 * The cap is counted in UTF-8 BYTES, because that is the unit the server
 * counts in: Go's `len(value)` is the length of the UTF-8 encoding. Counting
 * `value.length` instead counts UTF-16 code units, which is a smaller
 * number for every non-ASCII character, and the dashboard would then accept
 * a value the server refuses.
 *
 * The rule lives here rather than inline in the modal so it can be tested
 * without mounting the component, and so there is exactly one copy of it.
 */

/**
 * The value-length cap the server enforces (maxOverrideHeaderValueSize),
 * counted in UTF-8 bytes.
 */
export const MAX_OVERRIDE_VALUE_LENGTH = 8192

/** The encoder that gives a string's UTF-8 byte length, reused across calls. */
const encoder = new TextEncoder()

/**
 * The RFC 7230 §3.2.6 tchar set: ALPHA / DIGIT plus the punctuation a token
 * may carry. Vendors do use the punctuation (`X-!#$%&'*+.^_`|~`), and a name
 * rejected for being legal is a bug in the validator, not in the request.
 */
const TCHAR = /^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/

/**
 * The message for a name the server would reject, or '' when the name is
 * usable. A blank name is a row being typed, not a header, so it is not an
 * error.
 */
export function headerNameError(name: string): string {
  const key = name.trim()
  if (!key) return ''
  if (!TCHAR.test(key)) {
    return "Header names use the RFC 7230 token characters: letters, digits and ! # $ % & ' * + - . ^ _ ` | ~."
  }
  return ''
}

/**
 * The message for a value the server would reject, or '' when the value is
 * usable. The control-character rule comes first because a value carrying one
 * is not sent at all, whatever its length: NUL, DEL and every C0 control
 * except HTAB are in no field of an HTTP message, and net/http either
 * refuses them or lets a malformed header out.
 *
 * The length cap is measured in UTF-8 bytes, like the server's
 * `len(value) > maxOverrideHeaderValueSize` check.
 */
export function headerValueError(value: string): string {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i)
    if (code === 0x7f) return 'No control characters in a value.'
    if (code < 0x20 && code !== 0x09) return 'No control characters in a value.'
  }
  if (encoder.encode(value).length > MAX_OVERRIDE_VALUE_LENGTH) {
    return `Max ${MAX_OVERRIDE_VALUE_LENGTH} bytes.`
  }
  return ''
}
