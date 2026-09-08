import { describe, expect, it } from 'vitest'
import { catalogCreditsLabel, catalogProviderLabel, modelCreditsLabel, modelDescriptionText, modelProviderLabel } from './modelPlazaDisplay'

describe('modelPlazaDisplay', () => {
  it('formats provider and credits labels', () => {
    expect(modelProviderLabel({ provider_name: 'gpt' })).toBe('gpt')
    expect(modelProviderLabel({ provider_name: '' })).toBe('—')
    expect(modelCreditsLabel({ credit_multiplier: '1.00' })).toBe('1.00 点/次')
    expect(catalogProviderLabel({ provider_name: 'relay' })).toBe('relay')
    expect(catalogCreditsLabel({ credit_multiplier: null })).toBe('未配置')
  })

  it('drops redundant model descriptions', () => {
    expect(modelDescriptionText({
      model_id: 'gpt-5.6-terra',
      display_name: 'gpt-5.6-terra',
      description: 'gpt-5.6-terra（gpt-5.6-terra）',
    })).toBe('')
  })
})
