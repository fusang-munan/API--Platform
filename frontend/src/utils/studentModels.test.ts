import { describe, expect, it } from 'vitest'
import { filterStudentModels, mergeStudentPlazaModels } from './studentModels'
import type { Model, StudentModelPlaza } from '../api/types'

const baseModel = (modelID: string, isCommon = false): Model => ({
  model_id: modelID,
  display_name: modelID,
  category: 'text',
  capabilities: [],
  input_modalities: ['text'],
  output_modalities: ['text'],
  is_common: isCommon,
  status: 'active',
  route_ready: true,
  credit_multiplier: '1.00',
})

describe('mergeStudentPlazaModels', () => {
  it('merges common and library models without duplicates', () => {
    const plaza: StudentModelPlaza = {
      common_models: [baseModel('gpt-4o', true)],
      library_models: [baseModel('gpt-4o'), baseModel('claude-3')],
    }
    const merged = mergeStudentPlazaModels(plaza)
    expect(merged.map(model => model.model_id)).toEqual(['claude-3', 'gpt-4o'])
  })
})

describe('filterStudentModels', () => {
  it('filters by query and status', () => {
    const models = [baseModel('gpt-4o', true), { ...baseModel('claude-3'), status: 'inactive' as const }]
    const filtered = filterStudentModels(models, 'gpt', '', 'active')
    expect(filtered).toHaveLength(1)
    expect(filtered[0].model_id).toBe('gpt-4o')
  })
})
