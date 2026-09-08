/** 从 model_id 提取版本权重；只用首个 x.y 段，跳过 2026-8-11 这类日期。 */
export function modelVersionScore(modelId: string): number {
  for (const match of modelId.matchAll(/(\d+)\.(\d+)(?:\.(\d+))?/g)) {
    const major = Number(match[1])
    const minor = Number(match[2])
    const patch = Number(match[3] ?? 0)
    if (major >= 2000) continue
    return major * 1_000_000 + minor * 1_000 + patch
  }
  for (const match of modelId.matchAll(/(?:^|[./-])(\d{1,2})(?=[./-]|$)/g)) {
    const major = Number(match[1])
    if (major >= 20) continue
    return major * 1_000_000
  }
  return 0
}

export function compareModelsByRecency(leftId: string, rightId: string): number {
  const byVersion = modelVersionScore(rightId) - modelVersionScore(leftId)
  if (byVersion !== 0) return byVersion
  return rightId.localeCompare(leftId)
}

export function sortModelsByRecency<T extends { model_id: string }>(models: T[]): T[] {
  return [...models].sort((a, b) => compareModelsByRecency(a.model_id, b.model_id))
}
