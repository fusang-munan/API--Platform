import type { Model, ModelCatalogCandidate } from '../api/types'

export function modelDescriptionText(model: Pick<Model, 'model_id' | 'display_name' | 'description'>) {
  const description = model.description?.trim()
  if (!description) return ''
  const modelID = model.model_id.trim()
  const displayName = (model.display_name || modelID).trim()
  const redundant = `${displayName}（${modelID}）`
  if (
    description.toLowerCase() === redundant.toLowerCase()
    || description.toLowerCase() === modelID.toLowerCase()
    || description.toLowerCase() === displayName.toLowerCase()
  ) {
    return ''
  }
  return description
}

export function modelProviderLabel(model: Pick<Model, 'provider_name'>) {
  return model.provider_name?.trim() || '—'
}

export function modelCreditsLabel(model: Pick<Model, 'credit_multiplier' | 'provider_name'>) {
  if (model.provider_name?.trim() === '北辰') return '基础积分 × 1.10'
  return model.credit_multiplier ? `${model.credit_multiplier} 点/次` : '未配置'
}

export function catalogProviderLabel(item: Pick<ModelCatalogCandidate, 'provider_name'>) {
  return item.provider_name?.trim() || '—'
}

export function catalogCreditsLabel(item: Pick<ModelCatalogCandidate, 'credit_multiplier' | 'provider_name'>) {
  return modelCreditsLabel({ provider_name: item.provider_name, credit_multiplier: item.credit_multiplier ?? null })
}
