// Bulk-create API-key planner.
//
// Create-key is one name field today, so an operator standing up N clients
// clicks the same modal N times and copies N secrets one at a time. This
// planner turns a pasted block into the requests `POST /api/keys` already
// accepts — one per line, `name | machineId` — and leaves the secret to the
// server, so no batch path can invent a credential the dashboard never saw.
//
// `planBulkAdd` in bulk-add.ts is deliberately NOT reused: it parses
// `name | apiKey` for provider connections, where the caller supplies the
// secret. A key is the opposite — the secret is minted here and must never be
// pasted — so the grammar, the preview and the failure modes all differ.
export interface BulkKeyEntry {
  name: string
  machineId: string
  /** True when the line parsed but carried no name, so a placeholder is used. */
  nameWasBlank: boolean
}
export interface BulkKeyProblem {
  /** 1-based line number in the pasted block, for the operator to find it. */
  line: number
  reason: string
}
export interface PlanBulkKeysResult {
  entries: BulkKeyEntry[]
  problems: BulkKeyProblem[]
}

/**
 * Splits one pasted block into key requests.
 *
 * The line grammar is `name | machineId` with both halves optional, so a bare
 * name works and a bare machineId works:
 *
 *   office | m-abc-123
 *   laptop
 *   | m-def-456
 *
 * A blank line is skipped rather than reported — an operator pasting a list
 * with trailing newlines did not make a mistake, and a problem list that
 * counts blank lines teaches them to ignore it.
 *
 * A line with more than one separator is refused rather than truncated: the
 * extra text is a machineId the operator meant to send, and silently dropping
 * it would create a key that looks right and authorises the wrong machine.
 */
export function planBulkKeys(text: string): PlanBulkKeysResult {
  const entries: BulkKeyEntry[] = []
  const problems: BulkKeyProblem[] = []
  const lines = typeof text === 'string' ? text.split(/\r?\n/) : []

  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i]
    const line = raw.trim()
    const lineNo = i + 1
    if (!line) continue

    const parts = line.split('|')
    if (parts.length > 2) {
      problems.push({ line: lineNo, reason: 'expected "name | machineId" — too many separators' })
      continue
    }
    const name = (parts[0] ?? '').trim()
    const machineId = (parts[1] ?? '').trim()

    // A separator with nothing on the machineId side is a typo for "no
    // machineId", not an empty one: `office |` reads as an unfinished line.
    if (parts.length === 2 && machineId === '') {
      problems.push({ line: lineNo, reason: 'machineId is empty after "|"' })
      continue
    }
    entries.push({ name, machineId, nameWasBlank: name === '' })
  }
  return { entries, problems }
}

/**
 * Fills a placeholder for every entry whose line carried no name.
 *
 * The server accepts an empty name — the row is identified by its secret — but
 * a list of unnamed rows is unreadable, so the preview shows what will be
 * stored. Numbering follows the entry's position in the batch rather than a
 * gap-fill against existing names: a collision is refused at submit time by
 * `duplicateBatchKeyNames`, which is the honest answer, because two clients
 * sharing a name is a mistake the operator has to resolve rather than one the
 * dashboard can silently renumber.
 */
export function fillBatchKeyNames(entries: BulkKeyEntry[]): BulkKeyEntry[] {
  let auto = 0
  return entries.map((e) => {
    if (!e.nameWasBlank) return e
    auto += 1
    return { ...e, name: `Key ${auto}` }
  })
}

/**
 * Reports names that would collide within the batch or with an existing key.
 *
 * Comparison is case-insensitive because the server stores what it is given
 * and the list renders it verbatim: "Office" and "office" are two spellings of
 * one label to a human reading the table, and a batch that creates both has
 * produced something nobody can tell apart later.
 */
export function duplicateBatchKeyNames(
  entries: BulkKeyEntry[],
  existingNames: string[] = []
): BulkKeyProblem[] {
  const problems: BulkKeyProblem[] = []
  const seen = new Map<string, number>()
  const taken = new Set(
    existingNames.map((n) => (typeof n === 'string' ? n.trim().toLowerCase() : '')).filter(Boolean)
  )
  entries.forEach((e, idx) => {
    const key = e.name.trim().toLowerCase()
    if (!key) return
    if (taken.has(key)) {
      problems.push({ line: idx + 1, reason: `"${e.name}" is already the name of an existing key` })
      return
    }
    const firstAt = seen.get(key)
    if (firstAt !== undefined) {
      problems.push({ line: idx + 1, reason: `"${e.name}" is used twice in this batch` })
      return
    }
    seen.set(key, idx)
  })
  return problems
}

/**
 * Renders the batch result as the file an operator downloads.
 *
 * One line per key, `name<TAB>machineId<TAB>secret`, with a header. Tab
 * separation rather than CSV because a machineId is free text and a name may
 * contain a comma; the file is read by a human or pasted into a config, not
 * parsed by a spreadsheet.
 *
 * The secret appears exactly once — here and in the success dialog. That is
 * the point of the file: an operator creating five keys should not have to
 * copy five secrets one at a time before the dialog closes.
 */
export function formatKeyDownload(
  created: { name: string; machineId: string; key: string }[]
): string {
  const lines = ['name\tmachineId\tkey']
  for (const c of created) {
    lines.push(`${c.name}\t${c.machineId}\t${c.key}`)
  }
  return lines.join('\n')
}
