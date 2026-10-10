import { describe, expect, it } from 'bun:test'
import { HERMES_ROLES, hermesRoleLabel, isDelegationRole } from './hermesRoles'

// The catalog is a port of upstream `CLI_TOOLS.hermes.roles`. Its exact id set
// is what the card's per-role inputs, the yaml builder and the POST payload's
// `selections[]` agree on; a rename here silently drops the role.
describe('HERMES_ROLES', () => {
  it('exposes exactly the upstream role ids', () => {
    expect(HERMES_ROLES.map((r) => r.id)).toEqual([
      'delegation',
      'vision',
      'compression',
      'title_generation',
      'approval',
      'skills_hub',
      'mcp',
      'review',
      'memory_query_rewrite',
      'background_review',
      'curator',
      'monitor',
      'tts_audio_tags',
      'triage_specifier',
      'kanban_decomposer',
      'profile_describer',
      'goal_judge',
    ])
  })

  it('omits "default", which the card renders as the main model picker', () => {
    expect(HERMES_ROLES.map((r) => r.id)).not.toContain('default')
  })

  it('omits web_extract, removed upstream when it stopped calling an LLM', () => {
    expect(HERMES_ROLES.map((r) => r.id)).not.toContain('web_extract')
  })

  it('labels delegation and knows it is its own top-level block', () => {
    expect(hermesRoleLabel('delegation')).toBe('Delegation (subagents)')
    expect(isDelegationRole('delegation')).toBe(true)
    expect(isDelegationRole('compression')).toBe(false)
  })

  it('falls back to the id for an unknown role', () => {
    expect(hermesRoleLabel('mystery')).toBe('mystery')
  })
})