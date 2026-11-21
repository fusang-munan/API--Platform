<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { APIError, apiErrorMessage } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { BeichenAccount, BeichenAccountStatus, BeichenCrawlSummary, Provider } from '../../api/types'
import AppDialog from '../../components/AppDialog.vue'
import LoadingButton from '../../components/LoadingButton.vue'
import { fullTime } from '../../utils/format'

const { provider } = defineProps<{ provider: Provider }>()

type Action = 'login' | 'verify' | 'crawl' | 'clear'
type Mode = BeichenAccount['mode']

const statusMeta: Record<BeichenAccountStatus, { label: string; tone: string; hint: string }> = {
  none: { label: '未配置', tone: 'neutral', hint: '还没有保存北辰网页登录凭证，自动抓取暂不可用。' },
  active: { label: '正常', tone: 'success', hint: '网页会话可用，抓取会自动使用这份凭证。' },
  expired: { label: '已过期', tone: 'warning', hint: '网页会话已过期，请重新登录或粘贴新的 Token。' },
  error: { label: '错误', tone: 'danger', hint: '最近一次登录或验证失败，请检查下方原因后重试。' },
}
const modeLabels: Record<Mode, string> = { password: '账号密码', token: '网页 Token' }
const fallbackMessages: Record<Action, string> = {
  login: '登录失败，请检查账号和密码后重试',
  verify: '验证失败，请稍后重试',
  crawl: '抓取失败，请稍后重试',
  clear: '清除失败，请稍后重试',
}

const open = ref(false)
const loading = ref(false)
const busy = ref<Action | null>(null)
const account = ref<BeichenAccount>()
const error = ref('')
const notice = ref('')
const mode = ref<Mode>('password')
const email = ref('')
const passwordInput = ref<HTMLInputElement>()
const tokenInput = ref<HTMLInputElement>()
let accountRequest = 0

const currentStatus = computed(() => statusMeta[account.value?.status ?? 'none'] ?? statusMeta.error)
const configured = computed(() => Boolean(account.value?.configured))
const working = computed(() => loading.value || busy.value !== null)
const crawl = computed(() => account.value?.crawl ?? emptyCrawl())
const platformTitle = computed(() => namesOf(crawl.value.platforms))
const modelTitle = computed(() => namesOf(crawl.value.models))
const failureCount = computed(() => countOf(crawl.value.failures))
const failureDetails = computed(() => {
  const value = crawl.value.failure_details ?? (Array.isArray(crawl.value.failures) ? crawl.value.failures : [])
  return value
    .map(item => [item.platform, item.channel, item.path, item.reason].filter(Boolean).join(' · '))
    .filter(Boolean)
})

watch(mode, () => {
  error.value = ''
  notice.value = ''
})

function emptyCrawl(): BeichenCrawlSummary {
  return { last_run_at: null, platforms: null, models: null, failures: null }
}

function emptyAccount(): BeichenAccount {
  return {
    configured: false, mode: 'password', email: '', status: 'none',
    expires_at: null, last_verified_at: null, last_error: null, crawl: emptyCrawl(),
  }
}

function countOf(value: number | readonly unknown[] | null | undefined) {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (Array.isArray(value)) return value.length
  return null
}

function countText(value: number | null) {
  return value == null ? '—' : String(value)
}

function namesOf(value: number | string[] | null | undefined) {
  return Array.isArray(value) && value.length ? value.join('、') : undefined
}

function modeLabel(value?: string) {
  return value && value in modeLabels ? modeLabels[value as Mode] : '—'
}

function operationError(value: unknown, fallback: string) {
  if (value instanceof APIError) {
    if (/[\u3400-\u9fff]/u.test(value.serverMessage)) return value.serverMessage
    return apiErrorMessage(value, fallback)
  }
  return fallback
}

function clearSensitive() {
  if (passwordInput.value) passwordInput.value.value = ''
  if (tokenInput.value) tokenInput.value.value = ''
}

function applyAccount(value: BeichenAccount) {
  account.value = value
  if (value.mode === 'password' || value.mode === 'token') mode.value = value.mode
  if (value.email) email.value = value.email
}

function announce(value: BeichenAccount, success: string) {
  applyAccount(value)
  if (value.status === 'active') {
    error.value = ''
    notice.value = success
  } else {
    notice.value = ''
    error.value = value.last_error || currentStatus.value.hint
  }
}

async function loadAccount() {
  const request = ++accountRequest
  loading.value = true
  error.value = ''
  try {
    const value = await teacherAPI.beichenAccount(provider.id)
    if (request === accountRequest) applyAccount(value)
  } catch (value) {
    if (request !== accountRequest) return
    account.value = undefined
    error.value = operationError(value, '读取登录状态失败，请稍后重试')
  } finally {
    if (request === accountRequest) loading.value = false
  }
}

function show() {
  open.value = true
  error.value = ''
  notice.value = ''
  void loadAccount()
}

function setOpen(value: boolean) {
  open.value = value
  if (!value) clearSensitive()
}

async function runAccount(kind: Action, work: () => Promise<BeichenAccount>, success: string) {
  if (busy.value) return
  busy.value = kind
  error.value = ''
  notice.value = ''
  try {
    announce(await work(), success)
  } catch (value) {
    error.value = operationError(value, fallbackMessages[kind])
  } finally {
    busy.value = null
  }
}

async function submit() {
  if (busy.value) return
  error.value = ''
  notice.value = ''
  if (mode.value === 'token') {
    const token = tokenInput.value?.value?.trim() ?? ''
    if (tokenInput.value) tokenInput.value.value = ''
    if (!token) {
      error.value = '请粘贴北辰网页会话 Token。'
      return
    }
    await runAccount('login',
      () => teacherAPI.loginBeichenAccount(provider.id, { mode: 'token', token }),
      '已保存，服务器爬虫可直接使用。')
    return
  }
  const password = passwordInput.value?.value ?? ''
  if (passwordInput.value) passwordInput.value.value = ''
  const address = email.value.trim()
  if (!address) {
    error.value = '请填写北辰账号邮箱。'
    return
  }
  if (!password) {
    error.value = '请填写北辰账号密码。'
    return
  }
  await runAccount('login',
    () => teacherAPI.loginBeichenAccount(provider.id, { mode: 'password', email: address, password }),
    '已保存，服务器爬虫可直接使用。')
}

function verify() {
  if (busy.value || !configured.value) return
  void runAccount('verify', () => teacherAPI.verifyBeichenAccount(provider.id), '验证通过，网页会话可用。')
}

async function runCrawl() {
  if (busy.value || !configured.value) return
  busy.value = 'crawl'
  error.value = ''
  notice.value = ''
  try {
    const summary = await teacherAPI.crawlBeichen(provider.id)
    if (account.value) account.value = { ...account.value, crawl: summary }
    const failures = countOf(summary.failures) ?? 0
    const result = `平台 ${countText(countOf(summary.platforms))} 个，模型 ${countText(countOf(summary.models))} 个`
    if (failures > 0) {
      const first = failureDetails.value[0]
      error.value = `抓取完成，但有 ${failures} 条失败记录。${first ? `首条：${first}` : ''}`
    } else {
      notice.value = `抓取完成：${result}。`
    }
  } catch (value) {
    error.value = operationError(value, fallbackMessages.crawl)
    // An upstream error still records a completed attempt on the server.
    try {
      account.value = await teacherAPI.beichenAccount(provider.id)
    } catch {
      // Keep the previous summary if the server cannot be reached.
    }
  } finally {
    busy.value = null
  }
}

async function clearAccount() {
  if (busy.value || !account.value?.configured) return
  if (!window.confirm('清除已保存的北辰网页登录凭证？清除后服务器自动抓取会暂停。')) return
  busy.value = 'clear'
  error.value = ''
  notice.value = ''
  const previousCrawl = account.value.crawl
  try {
    const result = await teacherAPI.clearBeichenAccount(provider.id)
    account.value = result ?? { ...emptyAccount(), crawl: previousCrawl }
    mode.value = 'password'
    clearSensitive()
    notice.value = '凭证已清除，网页抓取会暂停。'
  } catch (value) {
    error.value = operationError(value, fallbackMessages.clear)
  } finally {
    busy.value = null
  }
}

</script>

<template>
  <button type="button" class="button secondary" @click="show">网页登录</button>
  <AppDialog :open="open" title="北辰网页登录" description="保存北辰网页会话，供服务器抓取目录、报价和用量。"
    wide @update:open="setOpen">
    <section class="beichen-login" :aria-busy="working">
      <p v-if="error" class="error banner" role="alert">{{ error }}</p>
      <p v-else-if="notice" class="success" role="status">{{ notice }}</p>

      <p v-if="loading && !account" class="muted" role="status">正在读取登录状态…</p>

      <section v-if="account" class="login-status" aria-label="网页登录状态">
        <div class="login-status-head">
          <span class="status-pill" :class="currentStatus.tone">{{ currentStatus.label }}</span>
          <span class="muted">{{ currentStatus.hint }}</span>
        </div>
        <dl class="login-summary">
          <div><dt>账号</dt><dd>{{ account.email || '—' }}</dd></div>
          <div><dt>凭证方式</dt><dd>{{ account.configured ? modeLabel(account.mode) : '—' }}</dd></div>
          <div><dt>最后验证</dt><dd>{{ fullTime(account.last_verified_at) }}</dd></div>
          <div><dt>会话到期</dt><dd>{{ fullTime(account.expires_at) }}</dd></div>
        </dl>
        <p v-if="account.last_error" class="error" role="alert">最近错误：{{ account.last_error }}</p>
      </section>

      <section v-if="account" class="login-crawl" aria-label="最近抓取">
        <h3>最近抓取</h3>
        <div v-if="crawl.last_run_at" class="chip-list">
          <span class="chip">时间 {{ fullTime(crawl.last_run_at) }}</span>
          <span class="chip" :title="platformTitle">平台 {{ countText(countOf(crawl.platforms)) }} 个</span>
          <span class="chip" :title="modelTitle">模型 {{ countText(countOf(crawl.models)) }} 个</span>
          <span class="chip" :class="{ 'chip-danger': failureCount }">失败 {{ countText(failureCount) }} 条</span>
        </div>
        <p v-else class="muted">还没有抓取记录。</p>
        <ul v-if="failureDetails.length" class="crawl-failures">
          <li v-for="(item, index) in failureDetails" :key="`${index}-${item}`">{{ item }}</li>
        </ul>
      </section>

      <div class="login-actions">
        <LoadingButton class="button secondary" :pending="busy === 'verify'"
          :disabled="!configured || (working && busy !== 'verify')" pending-label="验证中…" @click="verify">
          立即验证
        </LoadingButton>
        <LoadingButton class="button secondary" :pending="busy === 'crawl'"
          :disabled="!configured || (working && busy !== 'crawl')" pending-label="抓取中…" @click="runCrawl">
          测试抓取
        </LoadingButton>
        <LoadingButton class="button danger" :pending="busy === 'clear'"
          :disabled="!configured || (working && busy !== 'clear')" pending-label="清除中…" @click="clearAccount">
          清除凭证
        </LoadingButton>
      </div>

      <form class="login-form" @submit.prevent="submit">
        <fieldset class="login-mode" :disabled="working">
          <legend>凭证方式</legend>
          <label class="mode-option">
            <input v-model="mode" type="radio" value="password" name="mode">
            <span><strong>账号密码</strong><small>保存后由服务器自动续期</small></span>
          </label>
          <label class="mode-option">
            <input v-model="mode" type="radio" value="token" name="mode">
            <span><strong>粘贴 Token</strong><small>只保存网页会话，不保存账号密码</small></span>
          </label>
        </fieldset>

        <template v-if="mode === 'password'">
          <label>北辰账号邮箱
            <input v-model.trim="email" type="email" name="email" autocomplete="username"
              placeholder="teacher@example.com" :disabled="working">
          </label>
          <label>账号密码
            <input ref="passwordInput" type="password" name="password" autocomplete="current-password"
              placeholder="只用于本次登录" :disabled="working">
          </label>
          <p class="muted">密码提交后立即从输入框清空，不会保存在页面或浏览器存储里。保存成功后由服务器尝试自动续期。</p>
        </template>

        <template v-else>
          <label>网页会话 Token
            <input ref="tokenInput" type="password" name="token" autocomplete="off" autocapitalize="off"
              spellcheck="false" placeholder="粘贴后立即隐藏" :disabled="working">
          </label>
          <p class="muted">请从已登录的北辰网页会话复制。Token 不会回显，也不会保存在页面里；不要发到聊天或工单。</p>
        </template>

        <div class="login-actions">
          <LoadingButton class="button primary" type="submit" :pending="busy === 'login'"
            :disabled="working && busy !== 'login'" pending-label="提交中…">
            登录 / 更新
          </LoadingButton>
        </div>
      </form>

    </section>
  </AppDialog>
</template>

<style scoped>
.beichen-login { display: grid; gap: 0.9rem; min-width: 0; }
.beichen-login p { margin: 0; overflow-wrap: anywhere; }
.beichen-login .banner { margin: 0; }
.login-status, .login-crawl, .login-form {
  display: grid;
  gap: 0.75rem;
  min-width: 0;
  padding: 0.9rem;
  border: 1px solid var(--line);
  border-radius: 12px;
}
.login-status-head { display: flex; flex-wrap: wrap; align-items: center; gap: 0.55rem; }
.status-pill {
  display: inline-flex;
  align-items: center;
  border-radius: 999px;
  padding: 0.3rem 0.65rem;
  font-size: 0.75rem;
  font-weight: 760;
  white-space: nowrap;
}
.status-pill.neutral { background: #eef1f6; color: #66728a; }
.status-pill.success { background: #e3f8ef; color: #16765e; }
.status-pill.warning { background: #fff4dd; color: #9a6a17; }
.status-pill.danger { background: #fdecef; color: #b53b55; }
.login-summary { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 170px), 1fr)); gap: 0.65rem 1rem; margin: 0; }
.login-summary dt { color: var(--ink-soft); font-size: 0.75rem; font-weight: 700; }
.login-summary dd { margin: 0.2rem 0 0; overflow-wrap: anywhere; }
.login-crawl h3 { margin: 0; font-size: 0.95rem; }
.chip-danger { background: #fdecef; color: #b53b55; }
.crawl-failures { margin: 0; padding-inline-start: 1.1rem; color: #b53b55; font-size: 0.8rem; overflow-wrap: anywhere; }
.login-actions { display: flex; flex-wrap: wrap; gap: 0.55rem; }
.login-form { gap: 0.85rem; }
.login-mode { display: flex; flex-wrap: wrap; gap: 0.6rem 1.2rem; margin: 0; padding: 0; border: 0; }
.login-mode legend { margin-bottom: 0.35rem; padding: 0; }
.mode-option { display: flex; align-items: flex-start; gap: 0.5rem; font-weight: 500; cursor: pointer; }
.mode-option input { flex: none; width: 1rem; height: 1rem; margin: 0.15rem 0 0; padding: 0; }
.mode-option span { display: grid; gap: 0.15rem; }
.mode-option small { color: var(--ink-soft); font-size: 0.75rem; font-weight: 400; }
</style>
