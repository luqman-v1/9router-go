export function getCheckAllTargetModelIds(
  models: Array<{ id: string }>,
  disabledModelIds: string[] = []
): string[] {
  const disabledSet = new Set(disabledModelIds)
  return models.filter((m) => !disabledSet.has(m.id)).map((m) => m.id)
}
