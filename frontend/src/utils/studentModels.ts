import type { Model, StudentModelPlaza } from '../api/types'
import { sortModelsByRecency } from './modelSort'

export function mergeStudentPlazaModels(plaza: StudentModelPlaza): Model[] {
  const seen = new Set<string>()
  const result: Model[] = []
  for (const model of [...plaza.common_models, ...plaza.library_models]) {
    const key = model.model_id.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    result.push(model)
  }
  return sortModelsByRecency(result)
}

export function filterStudentModels(models: Model[], query: string, category: string, status: string) {
  const normalized = query.trim().toLowerCase()
  return models.filter(model =>
    (!normalized || `${model.display_name} ${model.model_id} ${model.capabilities.join(' ')}`.toLowerCase().includes(normalized))
    && (!category || model.category === category)
    && (!status || model.status === status),
  )
}
