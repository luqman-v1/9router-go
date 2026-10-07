import type { ProviderNode } from '../../api/client'

export interface NodeFormSeed {
  name: string
  /** The provider prefix the node's models already resolve under (`<prefix>/<model>`). */
  prefix: string
  apiType: 'chat' | 'responses'
  baseUrl: string
  urlSuffix: string
}

const DEFAULT_BASE_URL = {
  openai: 'https://api.openai.com/v1',
  anthropic: 'https://api.anthropic.com/v1',
} as const

/**
 * Values EditCompatibleNodeModal shows when it opens on a node.
 *
 * Every stored field has to be seeded: a blank Prefix field rendered only its
 * placeholder, left Save disabled (the submit requires all three fields) and
 * invited the user to retype what was already stored (issue #177).
 *
 * The suffix is the exception. A node created before the field existed carries
 * a random uuid tail; showing it as editable text would invite the user to
 * "fix" a value they never chose, so it starts empty and a blank submit keeps
 * the random id.
 */
export function nodeFormSeed(
  node: ProviderNode,
  isAnthropic = false
): NodeFormSeed {
  return {
    name: node.name || '',
    prefix: node.prefix || '',
    apiType: node.apiType === 'responses' ? 'responses' : 'chat',
    baseUrl:
      node.baseUrl ||
      (isAnthropic ? DEFAULT_BASE_URL.anthropic : DEFAULT_BASE_URL.openai),
    urlSuffix: node.urlSuffixGenerated ? '' : node.urlSuffix || '',
  }
}