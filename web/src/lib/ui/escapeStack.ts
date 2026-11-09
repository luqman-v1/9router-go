// Escape closes only the most recently opened dialog.
//
// Every dialog used to register its own document keydown listener, so with a
// dialog stacked on another (the model picker over Edit Combo, a confirm over
// a form) one Escape closed both — or, when the top layer had no handler,
// closed the dialog underneath and left the top one floating over nothing.
// Dialogs register here instead; a single listener calls only the top entry.

export type EscapeHandler = () => void

interface KeydownTarget {
  addEventListener(type: 'keydown', listener: (e: KeyboardEvent) => void): void
  removeEventListener(type: 'keydown', listener: (e: KeyboardEvent) => void): void
}

export interface EscapeStack {
  /** Register a handler on top of the stack; call the result to remove it. */
  push(handler: EscapeHandler): () => void
}

export function createEscapeStack(target: KeydownTarget): EscapeStack {
  const handlers: EscapeHandler[] = []

  const onKeydown = (e: KeyboardEvent) => {
    if (e.key !== 'Escape' || handlers.length === 0) return
    handlers[handlers.length - 1]()
  }

  return {
    push(handler) {
      if (handlers.length === 0) target.addEventListener('keydown', onKeydown)
      handlers.push(handler)
      return () => {
        const i = handlers.lastIndexOf(handler)
        if (i === -1) return
        handlers.splice(i, 1)
        if (handlers.length === 0) target.removeEventListener('keydown', onKeydown)
      }
    },
  }
}

let shared: EscapeStack | null = null

/** Register a dialog's Escape handler on the page-wide stack. */
export function pushEscapeHandler(handler: EscapeHandler): () => void {
  shared ??= createEscapeStack(document)
  return shared.push(handler)
}
