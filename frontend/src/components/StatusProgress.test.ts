import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import StatusProgress from './StatusProgress.vue'

describe('StatusProgress', () => {
  it('places mentor rejection at the mentor review step', () => {
    const wrapper = mount(StatusProgress, { props: { current: 'rejected', completed: ['submitted'] } })
    expect(wrapper.findAll('li')[1].classes()).toContain('terminal')
  })
})
