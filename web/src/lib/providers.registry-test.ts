// Test-only parser for internal/providers/provider_registry.go.
//
// The provider catalog the dashboard renders is served from that Go registry, so
// a test that hard-codes its own copy would drift from the thing it claims to
// cover. This module turns the Go map literal into the same shape the endpoint
// emits, so the assertions run against what the server really sends.

export interface ProviderCatalogItem {
  id: string
  name: string
  category: string
  alias?: string
  color?: string
  icon?: string
  noAuth?: boolean
  priority?: number
  mediaPriority?: number
  hidden?: boolean
  serviceKinds?: string[]
  authModes?: string[]
  authType?: string
  authHint?: string
  website?: string
  notice?: { text?: string; apiKeyUrl?: string; signupUrl?: string }
  modelsFetcher?: { url: string; type: string }
  credentialForm?: string
  apiKeyOptionalWith?: string
  usage?: boolean
  passthroughModels?: boolean
}

/** Reads a Go string literal body, honouring backslash escapes. */
function goString(raw: string): string {
  return raw.replace(/\\(["\\])/g, '$1').replace(/\\n/g, '\n').replace(/\\t/g, '\t')
}

/** Reads a Go []string{"a", "b"} literal. */
function goStringSlice(raw: string): string[] {
  const out: string[] = []
  const re = /"((?:[^"\\]|\\.)*)"/g
  let m: RegExpExecArray | null
  while ((m = re.exec(raw))) out.push(goString(m[1]))
  return out
}

export function parseProviderRegistry(src: string): ProviderCatalogItem[] {
  const start = src.indexOf('var ProviderRegistry = map[string]ProviderMeta{')
  if (start < 0) throw new Error('ProviderRegistry map not found')
  const items: ProviderCatalogItem[] = []

  // Walk the top-level entries only: an entry starts with a tab-indented key and
  // its fields are indented deeper, so a nested struct literal never opens a
  // false entry.
  const re = /^\t"([a-z0-9-]+)": \{$/gm
  let m: RegExpExecArray | null
  const starts: Array<{ id: string; at: number }> = []
  while ((m = re.exec(src))) starts.push({ id: m[1], at: m.index })
  if (starts.length === 0) throw new Error('no registry entries found')

  for (let i = 0; i < starts.length; i++) {
    const { id, at } = starts[i]
    const end = i + 1 < starts.length ? starts[i + 1].at : src.length
    const body = src.slice(at, end)
    const item: ProviderCatalogItem = { id, name: id, category: 'apikey' }

    const name = body.match(/\bName:\s*"((?:[^"\\]|\\.)*)"/)
    if (name) item.name = goString(name[1])
    const alias = body.match(/\bAlias:\s*"((?:[^"\\]|\\.)*)"/)
    if (alias) item.alias = goString(alias[1])
    const color = body.match(/\bColor:\s*"((?:[^"\\]|\\.)*)"/)
    if (color) item.color = goString(color[1])
    const icon = body.match(/\bIcon:\s*"((?:[^"\\]|\\.)*)"/)
    if (icon) item.icon = goString(icon[1])
    const authType = body.match(/\bAuthType:\s*"((?:[^"\\]|\\.)*)"/)
    if (authType) item.authType = goString(authType[1])
    const authHint = body.match(/\bAuthHint:\s*"((?:[^"\\]|\\.)*)"/)
    if (authHint) item.authHint = goString(authHint[1])
    const website = body.match(/\bWebsite:\s*"((?:[^"\\]|\\.)*)"/)
    if (website) item.website = goString(website[1])
    const form = body.match(/\bCredentialForm:\s*"((?:[^"\\]|\\.)*)"/)
    if (form) item.credentialForm = goString(form[1])
    const optional = body.match(/\bAPIKeyOptionalWith:\s*"((?:[^"\\]|\\.)*)"/)
    if (optional) item.apiKeyOptionalWith = goString(optional[1])

    const priority = body.match(/\bPriority:\s*(\d+)/)
    if (priority) item.priority = Number(priority[1])
    const mediaPriority = body.match(/\bMediaPriority:\s*(\d+)/)
    if (mediaPriority) item.mediaPriority = Number(mediaPriority[1])

    if (/\bNoAuth:\s*true/.test(body)) item.noAuth = true
    if (/\bHidden:\s*true/.test(body)) item.hidden = true
    if (/\bUsage:\s*true/.test(body)) item.usage = true
    if (/\bPassthroughModels:\s*true/.test(body)) item.passthroughModels = true

    const kinds = body.match(/\bServiceKinds:\s*(\[\]string\{[^}]*\})/)
    if (kinds) item.serviceKinds = goStringSlice(kinds[1])
    const modes = body.match(/\bAuthModes:\s*(\[\]string\{[^}]*\})/)
    if (modes) item.authModes = goStringSlice(modes[1])

    const cat = body.match(/\bCategory:\s*Category([A-Za-z]+)/)
    if (cat) item.category = catConstToKey(cat[1])

    const fetcher = body.match(
      /\bModelsFetcher:\s*&Fetcher\{URL:\s*"((?:[^"\\]|\\.)*)",\s*Type:\s*"((?:[^"\\]|\\.)*)"\}/,
    )
    if (fetcher) item.modelsFetcher = { url: goString(fetcher[1]), type: goString(fetcher[2]) }

    const notice = body.match(/\bNotice:\s*&Notice\{([^}]*)\}/)
    if (notice) {
      item.notice = {}
      const text = notice[1].match(/\bText:\s*"((?:[^"\\]|\\.)*)"/)
      if (text) item.notice.text = goString(text[1])
      const apiKeyUrl = notice[1].match(/\bAPIKeyURL:\s*"((?:[^"\\]|\\.)*)"/)
      if (apiKeyUrl) item.notice.apiKeyUrl = goString(apiKeyUrl[1])
      const signupUrl = notice[1].match(/\bSignupURL:\s*"((?:[^"\\]|\\.)*)"/)
      if (signupUrl) item.notice.signupUrl = goString(signupUrl[1])
    }

    items.push(item)
  }
  return items
}

/** Maps the Go Category constant back to the wire value the UI expects. */
function catConstToKey(name: string): string {
  switch (name) {
    case 'OAuth':
      return 'oauth'
    case 'FreeTier':
      return 'freeTier'
    case 'Free':
      return 'free'
    case 'APIKey':
      return 'apikey'
    case 'WebCookie':
      return 'webCookie'
    case 'Custom':
      return 'custom'
    default:
      return 'apikey'
  }
}
