import { parseCustomModelsResponse, parseDisabledModelsMap, type CustomModelEntry } from '../../lib/customModels'

export interface PickerExtrasSource {
  getModelAliases: () => Promise<{ aliases?: Record<string, string> } | null>
  getCustomModels: () => Promise<unknown>
  getDisabledModels: () => Promise<unknown>
}

export interface ResolvedPickerExtras {
  modelAliases: Record<string, string>
  customModels: CustomModelEntry[]
  disabledModels: Record<string, string[]>
}

export const EMPTY_PICKER_EXTRAS: ResolvedPickerExtras = {
  modelAliases: {},
  customModels: [],
  disabledModels: {},
}

/**
 * Fetch the three model-metadata endpoints in one batch and hand back one
 * object, so the picker can publish it in a single state write.
 *
 * The modal used to chain three independent `.then()` callbacks, each writing
 * its own `$state`. Every settle re-ran `resolveModelPickerGroups` and
 * re-rendered the whole pill list — three full passes per open even when the
 * payload had not changed, which is most of the cost behind issue #61.
 *
 * Each endpoint catches on its own: one failing request must not discard the
 * two that succeeded.
 */
export async function loadPickerExtras(source: PickerExtrasSource): Promise<ResolvedPickerExtras> {
  const [aliasRes, customRes, disabledRes] = await Promise.all([
    source.getModelAliases().catch(() => null),
    source.getCustomModels().catch(() => null),
    source.getDisabledModels().catch(() => null),
  ])

  return {
    modelAliases: aliasRes?.aliases || {},
    customModels: parseCustomModelsResponse(customRes),
    disabledModels: parseDisabledModelsMap(disabledRes),
  }
}