export const PASSWORD_POLICY_HINT = '密码至少 8 位，且同时包含字母和数字'

export function isPasswordValid(password: string): boolean {
  if (password.length < 8) return false
  return /[a-zA-Z]/.test(password) && /\d/.test(password)
}
