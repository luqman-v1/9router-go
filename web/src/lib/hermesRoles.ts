/**
 * Hermes model-role catalog.
 *
 * Port of `CLI_TOOLS.hermes.roles` from upstream decolua/9router
 * (`src/shared/constants/cliTools.js`). The card renders the main model as its
 * own "Default Model" picker, so `default` is deliberately absent here and is
 * added at the payload level as the top-level `model:` block.
 *
 * `web_extract` is not in the list: upstream removed it when that role stopped
 * calling an LLM, and re-adding it would offer a knob that does nothing.
 */

export interface HermesRole {
  id: string
  label: string
}

export const HERMES_ROLES: HermesRole[] = [
  { id: 'delegation', label: 'Delegation (subagents)' },
  { id: 'vision', label: 'Vision' },
  { id: 'compression', label: 'Compression' },
  { id: 'title_generation', label: 'Title Generation' },
  { id: 'approval', label: 'Approval' },
  { id: 'skills_hub', label: 'Skills Hub' },
  { id: 'mcp', label: 'MCP' },
  { id: 'review', label: 'Review (/review subagent)' },
  { id: 'memory_query_rewrite', label: 'Memory Query Rewrite' },
  { id: 'background_review', label: 'Background Review' },
  { id: 'curator', label: 'Curator' },
  { id: 'monitor', label: 'Monitor' },
  { id: 'tts_audio_tags', label: 'TTS Audio Tags' },
  { id: 'triage_specifier', label: 'Triage Specifier' },
  { id: 'kanban_decomposer', label: 'Kanban Decomposer' },
  { id: 'profile_describer', label: 'Profile Describer' },
  { id: 'goal_judge', label: 'Goal Judge' },
]

/**
 * `delegation` is its own top-level block in `config.yaml`; every other role
 * is an entry under `auxiliary:`. The card and the yaml builder both branch on
 * this, so it lives next to the catalog rather than in the component.
 */
export function isDelegationRole(roleId: string): boolean {
  return roleId === 'delegation'
}

/** Human label for a role id, falling back to the id itself. */
export function hermesRoleLabel(roleId: string): string {
  return HERMES_ROLES.find((r) => r.id === roleId)?.label ?? roleId
}