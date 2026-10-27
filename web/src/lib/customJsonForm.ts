// Form model for a custom JSON node (port of upstream
// src/shared/utils/customJsonForm.js, decolua/9router#4929). The server
// re-validates everything in internal/customjson; these checks only turn the
// editor's rows back into a JSON template and report mistakes the user can fix
// before saving.

export const CUSTOM_JSON_API_TYPE = 'custom-json'
export const CUSTOM_JSON_METHODS = ['POST', 'PUT', 'PATCH'] as const
export type CustomJsonMethod = (typeof CUSTOM_JSON_METHODS)[number]
export type CustomJsonAuthType = 'bearer' | 'api-key' | 'none'

export interface CustomJsonConfig {
  method: CustomJsonMethod
  authType: CustomJsonAuthType
  authHeader: string
  headers: Record<string, string>
  requestTemplate: Record<string, unknown>
  responsePath: string
}

export const CUSTOM_JSON_DEFAULT_TEMPLATE: Readonly<Record<string, string>> = Object.freeze({
  model: '{{model}}',
  messages: '{{messages}}',
})

export const CUSTOM_JSON_DEFAULTS: CustomJsonConfig = {
  method: 'POST',
  authType: 'bearer',
  authHeader: 'X-API-Key',
  headers: {},
  requestTemplate: { ...CUSTOM_JSON_DEFAULT_TEMPLATE },
  responsePath: 'choices[0].message.content',
}

export interface CustomJsonExtraField {
  id: string
  name: string
  type: 'text' | 'json'
  value: string
}

export interface CustomJsonForm {
  method: CustomJsonMethod
  authType: CustomJsonAuthType
  authHeader: string
  responsePath: string
  /** "fields" adds optional rows to the default template; "json" edits it whole. */
  templateMode: 'fields' | 'json'
  extraFields: CustomJsonExtraField[]
  requestTemplateText: string
  headersText: string
}

const BLOCKED_KEYS = new Set(['__proto__', 'prototype', 'constructor'])

/** Opens the editor on a stored configuration, or on the defaults. */
export function customJsonForm(config?: Partial<CustomJsonConfig> | null): CustomJsonForm {
  const merged = { ...CUSTOM_JSON_DEFAULTS, ...(config ?? {}) }
  const template = merged.requestTemplate || { ...CUSTOM_JSON_DEFAULT_TEMPLATE }
  // A template that still sends model and messages unchanged opens in the
  // field editor; anything else is preserved verbatim in the JSON editor.
  const usesDefaultFields = Object.entries(CUSTOM_JSON_DEFAULT_TEMPLATE).every(([name, value]) => template[name] === value)
  return {
    method: merged.method,
    authType: merged.authType,
    authHeader: merged.authHeader,
    responsePath: merged.responsePath,
    templateMode: usesDefaultFields ? 'fields' : 'json',
    extraFields: Object.entries(template)
      .filter(([name]) => !Object.hasOwn(CUSTOM_JSON_DEFAULT_TEMPLATE, name))
      .map(([name, value], index) => ({
        id: `saved-${index}`,
        name,
        type: typeof value === 'string' ? 'text' : 'json',
        value: typeof value === 'string' ? value : JSON.stringify(value),
      })),
    requestTemplateText: JSON.stringify(template, null, 2),
    headersText: JSON.stringify(merged.headers || {}, null, 2),
  }
}

/** Builds the template the field editor describes: the defaults plus every named row. */
export function buildCustomJsonTemplate(fields: CustomJsonExtraField[] = []): Record<string, unknown> {
  const template: Record<string, unknown> = { ...CUSTOM_JSON_DEFAULT_TEMPLATE }
  for (const field of fields) {
    const name = field.name.trim()
    // An untouched added row is optional and does not block saving.
    if (!name && !field.value.trim()) continue
    if (!name) throw new Error('Enter a name for the extra field, or remove the row')
    if (BLOCKED_KEYS.has(name)) throw new Error(`Field name cannot be used: ${name}`)
    if (Object.hasOwn(template, name)) throw new Error(`Duplicate field name: ${name}`)
    let value: unknown = field.value
    if (field.type === 'json') {
      try {
        value = JSON.parse(field.value)
      } catch {
        throw new Error(`Field ${name} must contain valid JSON`)
      }
    }
    template[name] = value
  }
  return template
}

function parseObject(text: string, message: string): Record<string, unknown> {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    throw new Error(message)
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error(message)
  return parsed as Record<string, unknown>
}

/** Turns the editor back into the configuration the API stores. */
export function parseCustomJsonForm(form: CustomJsonForm): CustomJsonConfig {
  const requestTemplate =
    form.templateMode === 'fields'
      ? buildCustomJsonTemplate(form.extraFields)
      : parseObject(form.requestTemplateText, 'Request template must be a JSON object')
  const headers = parseObject(form.headersText || '{}', 'Custom headers must be a JSON object') as Record<string, string>
  const responsePath = form.responsePath.trim()
  if (!responsePath) throw new Error('Response field path is required (use $ for the entire JSON response)')
  return {
    method: form.method,
    authType: form.authType,
    authHeader: form.authHeader.trim(),
    headers,
    requestTemplate,
    responsePath,
  }
}

/**
 * Switches the template editor mode. Leaving the field editor writes its rows
 * into the JSON text; returning reads the JSON back into rows.
 */
export function switchCustomJsonTemplateMode(form: CustomJsonForm, mode: CustomJsonForm['templateMode']): CustomJsonForm {
  if (mode === form.templateMode) return form
  if (mode === 'json') {
    return { ...form, templateMode: 'json', requestTemplateText: JSON.stringify(buildCustomJsonTemplate(form.extraFields), null, 2) }
  }
  const requestTemplate = parseObject(form.requestTemplateText, 'Request template must be a JSON object')
  const restored = customJsonForm({ ...parseCustomJsonForm({ ...form, templateMode: 'json' }), requestTemplate })
  return { ...restored, templateMode: 'fields' }
}
