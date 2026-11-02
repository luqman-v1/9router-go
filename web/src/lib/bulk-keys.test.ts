// Guards the bulk-create key planner. The failure this file exists for is a
// batch that looks right and is not: a dropped machineId, a duplicate name, or
// a secret that never reached the operator. Ported in spirit from
// bulk-add.test.ts, which covers the provider-connection variant.
import assert from 'node:assert'
import { describe, it } from 'node:test'
import {
  duplicateBatchKeyNames,
  fillBatchKeyNames,
  formatKeyDownload,
  planBulkKeys,
} from './bulk-keys'

describe('planBulkKeys: line grammar', () => {
  it('parses "name | machineId"', () => {
    const { entries, problems } = planBulkKeys('office | m-abc-123\nlaptop')
    assert.deepStrictEqual(problems, [])
    assert.deepStrictEqual(entries, [
      { name: 'office', machineId: 'm-abc-123', nameWasBlank: false },
      { name: 'laptop', machineId: '', nameWasBlank: false },
    ])
  })
  it('accepts a bare machineId with no name', () => {
    const { entries, problems } = planBulkKeys('| m-def-456')
    assert.deepStrictEqual(problems, [])
    assert.strictEqual(entries[0].name, '')
    assert.strictEqual(entries[0].machineId, 'm-def-456')
    assert.strictEqual(entries[0].nameWasBlank, true)
  })
  it('skips blank lines without reporting them', () => {
    const { entries, problems } = planBulkKeys('office\n\n   \nlaptop\n')
    assert.deepStrictEqual(problems, [])
    assert.deepStrictEqual(entries.map((e) => e.name), ['office', 'laptop'])
  })
  it('refuses a line with too many separators rather than truncating it', () => {
    // The third field is a machineId the operator meant to send. Dropping it
    // would create a key that looks right and authorises the wrong machine.
    const { entries, problems } = planBulkKeys('office | m-abc | m-xyz')
    assert.deepStrictEqual(entries, [])
    assert.strictEqual(problems.length, 1)
    assert.strictEqual(problems[0].line, 1)
    assert.match(problems[0].reason, /too many separators/)
  })
  it('refuses a dangling separator', () => {
    const { entries, problems } = planBulkKeys('office |')
    assert.deepStrictEqual(entries, [])
    assert.strictEqual(problems[0].reason, 'machineId is empty after "|"')
  })
  it('handles CRLF line endings', () => {
    const { entries, problems } = planBulkKeys('office | m-abc\r\nlaptop\r\n')
    assert.deepStrictEqual(problems, [])
    assert.deepStrictEqual(entries.map((e) => e.name), ['office', 'laptop'])
  })
})

describe('fillBatchKeyNames', () => {
  it('numbers only the entries that had no name', () => {
    const out = fillBatchKeyNames([
      { name: 'office', machineId: '', nameWasBlank: false },
      { name: '', machineId: 'm-1', nameWasBlank: true },
      { name: '', machineId: 'm-2', nameWasBlank: true },
    ])
    assert.deepStrictEqual(out.map((e) => e.name), ['office', 'Key 1', 'Key 2'])
  })
  it('leaves an explicitly named entry alone', () => {
    const out = fillBatchKeyNames([{ name: 'Office', machineId: '', nameWasBlank: false }])
    assert.strictEqual(out[0].name, 'Office')
  })
})

describe('duplicateBatchKeyNames', () => {
  const base = (name: string) => ({ name, machineId: '', nameWasBlank: false })
  it('passes a clean batch', () => {
    assert.deepStrictEqual(duplicateBatchKeyNames([base('a'), base('b')], ['office']), [])
  })
  it('catches a repeat inside the batch', () => {
    const p = duplicateBatchKeyNames([base('office'), base('laptop'), base('office')])
    assert.strictEqual(p.length, 1)
    assert.strictEqual(p[0].line, 3)
    assert.match(p[0].reason, /used twice/)
  })
  it('catches a collision with an existing key', () => {
    const p = duplicateBatchKeyNames([base('Office')], ['office'])
    assert.strictEqual(p.length, 1)
    assert.match(p[0].reason, /already the name/)
  })
  it('ignores unnamed entries — they are filled later', () => {
    assert.deepStrictEqual(duplicateBatchKeyNames([base(''), base('')], []), [])
  })
})

describe('formatKeyDownload', () => {
  it('writes a header and one line per key', () => {
    const out = formatKeyDownload([
      { name: 'office', machineId: 'm-abc', key: 'sk-1' },
      { name: 'laptop', machineId: '', key: 'sk-2' },
    ])
    assert.strictEqual(out, 'name\tmachineId\tkey\noffice\tm-abc\tsk-1\nlaptop\t\tsk-2')
  })
  it('keeps a name containing a comma on one line', () => {
    // Tab separation, not CSV: a machineId is free text and a name may hold a
    // comma, so a CSV writer would split the row.
    const out = formatKeyDownload([{ name: 'office, main', machineId: 'm', key: 'sk-1' }])
    assert.strictEqual(out.split('\n').length, 2)
    assert.ok(out.includes('office, main\tm\tsk-1'))
  })
})
