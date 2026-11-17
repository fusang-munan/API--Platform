import { describe, expect, it } from 'vitest'
import { isPasswordValid } from './passwordPolicy'

describe('isPasswordValid', () => {
  it('accepts passwords with at least 8 chars, letters and digits', () => {
    expect(isPasswordValid('abc12345')).toBe(true)
    expect(isPasswordValid('Pass1234')).toBe(true)
  })

  it('rejects passwords missing length or character classes', () => {
    expect(isPasswordValid('short1')).toBe(false)
    expect(isPasswordValid('allletters')).toBe(false)
    expect(isPasswordValid('12345678')).toBe(false)
  })
})
