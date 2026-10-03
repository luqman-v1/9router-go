import { writable } from 'svelte/store'

const STORAGE_KEY = '9router_mask_email'

/**
 * Mask an email address for privacy / screen sharing.
 * E.g. "luqman.hakim@gmail.com" -> "l***m@gmail.com"
 * E.g. "ab@domain.com" -> "a***@domain.com"
 * Preserves non-email text, or masks emails embedded inside a string.
 */
export function maskEmail(text: string | null | undefined): string {
  if (!text) return ''
  return text.replace(
    /([a-zA-Z0-9_.+-]+)@([a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+)/g,
    (_match, user: string, domain: string) => {
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

  return {
    subscribe: store.subscribe,
    toggle,
    set,
  }
}

export const emailPrivacy = createEmailPrivacyStore()
