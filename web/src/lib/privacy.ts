import { writable } from 'svelte/store'

const STORAGE_KEY = '9router_mask_email'

/**
 * Mask an email address for privacy / screen sharing.
 * E.g. "luqman.hakim@gmail.com" -> "l***m@gmail.com"
 * E.g. "ab@domain.com" -> "a***@domain.com"
 * Preserves non-email text, or masks emails embedded inside a string.
 *
 * Idempotent: a name that is already masked (the `***` run was written by an
 * earlier call) is left alone. Without that, masking a stored masked name
 * twice turns "l***m@gmail.com" into "l***m***@gmail.com".
 */
export function maskEmail(text: string | null | undefined): string {
  if (!text) return ''
  return text.replace(
    // The local part may begin with an existing mask run. Without it the
    // pattern cannot see the '*' characters that sit in front of the last
    // character it would match ("l***m@gmail.com" matches only "m"), and a
    // second pass would mask the already-masked name a second time.
    /(\*{0,3}[a-zA-Z0-9_.+-]+)@([a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+)/g,
    (match, user: string, domain: string) => {
      if (user.includes('***')) return match
      if (user.length <= 2) {
        return `${user[0] ?? ''}***@${domain}`
      }
      return `${user[0]}***${user[user.length - 1]}@${domain}`
    }
  )
}

/**
 * Formats a connection or account label. If masking is active,
 * any email addresses in the label will be masked.
 */
export function formatEmailLabel(text: string | null | undefined, isMasked: boolean): string {
  if (!text) return ''
  return isMasked ? maskEmail(text) : text
}

/**
 * The name an edit form should submit, given what the field was seeded with.
 *
 * A masked label is a rendering, not a rename, so an untouched field must
 * submit nothing and leave the stored name alone. The comparison is against
 * the value the field OPENED with, never against a freshly derived mask: if
 * masking is toggled in another window while the form is open, re-deriving it
 * turns an untouched masked field into a rename and persists the mask.
 *
 * Both sides are trimmed, so a stored name with surrounding whitespace still
 * compares equal to its own masked form.
 *
 * Returns undefined when the field was not meaningfully edited.
 */
export function submittedConnectionName(
  fieldValue: string,
  originalName: string,
  seededValue: string
): string | undefined {
  const trimmed = fieldValue.trim()
  if (trimmed === originalName.trim() || trimmed === seededValue.trim()) return undefined
  return trimmed || undefined
}

function createEmailPrivacyStore() {
  const initial =
    typeof window !== 'undefined'
      ? localStorage.getItem(STORAGE_KEY) === 'true'
      : false

  const store = writable<boolean>(initial)

  function toggle() {
    store.update((current) => {
      const next = !current
      if (typeof window !== 'undefined') {
        localStorage.setItem(STORAGE_KEY, String(next))
      }
      return next
    })
  }

  function set(value: boolean) {
    if (typeof window !== 'undefined') {
      localStorage.setItem(STORAGE_KEY, String(value))
    }
    store.set(value)
  }

  // Masking exists for screen sharing, which very often means a second window
  // or a projector view left open from earlier. A toggle applied in one tab
  // has to reach the others, otherwise the window the audience is actually
  // looking at keeps showing the full address.
  if (typeof window !== 'undefined') {
    window.addEventListener('storage', (event) => {
      if (event.key !== STORAGE_KEY || event.newValue === null) return
      store.set(event.newValue === 'true')
    })
  }

  return {
    subscribe: store.subscribe,
    toggle,
    set,
  }
}

export const emailPrivacy = createEmailPrivacyStore()
