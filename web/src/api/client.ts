import connectionsEndpoints from './endpoints/connections.ts'
import nodesEndpoints from './endpoints/nodes.ts'
import combosEndpoints from './endpoints/combos.ts'
import keysEndpoints from './endpoints/keys.ts'
import modelsEndpoints from './endpoints/models.ts'
import settingsEndpoints from './endpoints/settings.ts'
import oauthEndpoints from './endpoints/oauth.ts'
import usageEndpoints from './endpoints/usage.ts'
import tunnelEndpoints from './endpoints/tunnel.ts'
import headroomEndpoints from './endpoints/headroom.ts'
import poolsEndpoints from './endpoints/pools.ts'
import cliEndpoints from './endpoints/cli.ts'
import authEndpoints from './endpoints/auth.ts'
import jailbreakEndpoints from './endpoints/jailbreak.ts'

export type * from './types'
export * from './runtime'

export const api = {
  ...connectionsEndpoints,
  ...nodesEndpoints,
  ...combosEndpoints,
  ...keysEndpoints,
  ...modelsEndpoints,
  ...settingsEndpoints,
  ...oauthEndpoints,
  ...usageEndpoints,
  ...tunnelEndpoints,
  ...headroomEndpoints,
  ...poolsEndpoints,
  ...cliEndpoints,
  ...authEndpoints,
  ...jailbreakEndpoints,
}
