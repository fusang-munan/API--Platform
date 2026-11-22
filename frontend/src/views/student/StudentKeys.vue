<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { studentAPI } from '../../api/student'
import type { ApiKey, BindingAdapter, Model, Organization, StudentProject, StudentUsage, SubmitApiKey } from '../../api/types'
import { APIError } from '../../api/client'
import StatusProgress from '../../components/StatusProgress.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import StudentHarnessConfig from '../../components/StudentHarnessConfig.vue'
import { useLoadState } from '../../composables/useLoadState'
import { usePendingActions } from '../../composables/usePendingActions'
import { useToast } from '../../composables/useToast'
import { changedApiKeyID } from '../../utils/keyStatusEvents'
import { calculateCredits, fullTime, quotaBalance } from '../../utils/format'
import { formatCredits } from '../../utils/credits'
import { harnessDialogTitle, keyCanWithdraw, keyHarnessSupport } from '../../utils/studentHarnessConfig'
import { BINDING_ADAPTER_OPTIONS, bindingAdapterMeta } from '../../utils/bindingAdapters'

type KeyModelAvailability = 'available' | 'unverified' | 'maintenance'
type KeyHealthState = 'ok' | 'pending' | 'blocked'
type KeyHealthItem = {
  id: 'credential' | 'catalog' | 'probe'
  title: string
  state: KeyHealthState
  value: string
  detail: string
  action: string
}

const orgs = ref<Organization[]>([])
const projects = ref<StudentProject[]>([])
const keys = ref<ApiKey[]>([])
const usage = ref<StudentUsage | null>(null)
const usageLoaded = ref(false)
const usageFailed = ref(false)
const detail = ref<ApiKey | null>(null)
const memberBalance = computed(() => {
  const key = detail.value
  if (key?.member_monthly_credits == null || key.member_used_credits == null) return
  const available = key.member_pending_credits == null ? undefined :
    calculateCredits(key.member_monthly_credits, calculateCredits(key.member_used_credits, key.member_pending_credits, 'add'), 'subtract')
  return quotaBalance(key.member_monthly_credits, key.member_used_credits, available)
})
const claimConfirm = ref<ApiKey | null>(null)
const withdrawConfirm = ref<ApiKey | null>(null)
const configKey = ref<ApiKey | null>(null)
const regeneratingId = ref('')
const error = ref('')
const submittedKeyID = ref('')
const toast = useToast()
const loadState = useLoadState()
const actions = usePendingActions()
const form = reactive<SubmitApiKey>({ organization_id: '', project_id: '', name: '', requested_monthly_credits: '200.000' })
let loadSequence = 0

const keyDisplayStatus = (key: ApiKey) => {
  if (key.status === 'rejected' && key.audits?.some(audit => audit.action === 'student_withdrawn')) return 'withdrawn'
  return key.status
}
const liveKeyStatuses = new Set<ApiKey['status']>(['pending_mentor', 'pending_teacher', 'approved', 'active', 'inactive'])
const blockedProjectIds = computed(() => new Set(keys.value.filter(key => liveKeyStatuses.has(key.status)).map(key => key.project.id)))
const selectableProjects = computed(() => projects.value.filter(project =>
  project.status === 'active' && !blockedProjectIds.value.has(project.id),
))
const canSubmit = computed(() => Boolean(form.organization_id && form.project_id && form.name.trim() && form.requested_monthly_credits.trim()))
const keyUsageByID = computed(() => {
  const rows = new Map<string, { used: string; available: string | null }>()
  for (const project of usage.value?.projects ?? []) {
    for (const key of project.keys ?? []) {
      const available = key.available?.trim() ? key.available.trim() : (project.available?.trim() || null)
      rows.set(key.id, { used: key.used, available })
    }
  }
  return rows
})
const keyCards = computed(() => keys.value.map(key => {
  const rows = keyModelList(key).map(model => ({ model, availability: keyModelAvailability(model) }))
  const availableModels = rows.filter(row => row.availability === 'available').map(row => row.model)
  const hasUnverified = rows.some(row => row.availability === 'unverified')
  return {
    key, rows, availableCount: key.status === 'active' ? availableModels.length : 0,
    protocols: keyProtocols(key),
    health: keyHealth(key, availableModels, hasUnverified),
    usage: keyUsageByID.value.get(key.id) ?? null,
  }
}))
const maskedKey = (key: ApiKey) => key.key_prefix ? key.key_prefix + '••••••' : '密钥已生成，点击「查看密钥」'
const keySupportsHarness = (key: ApiKey) => {
  const support = keyHarnessSupport(keyModelList(key))
  return support.codex || support.claude
}
const harnessTitle = computed(() => harnessDialogTitle(keyHarnessSupport(configKey.value?.models ?? [])))
const harnessWide = computed(() => {
  const support = keyHarnessSupport(configKey.value?.models ?? [])
  return support.codex && support.claude
})

const protocolLabels: Record<string, string> = {
  openai_compatible: 'OpenAI Chat',
  openai_responses: 'OpenAI Responses',
  anthropic: 'Anthropic Messages',
  google_gemini_v1beta: 'Gemini 原生',
}

const keyHealthStateLabels: Record<KeyHealthState, string> = {
  ok: '正常',
  pending: '等一等',
  blocked: '需处理',
}

const modelAvailabilityLabels: Record<KeyModelAvailability, string> = {
  available: '可用',
  unverified: '待验证',
  maintenance: '维护中',
}

function keyModelList(key: ApiKey) {
  return key.models ?? []
}

// Key 下的模型与 /v1/models 使用同一组协议字段。
function protocolsForKeyModel(model: Model) {
  const protocols = model.protocols ?? model.supported_protocols
  const values = new Set(protocols)
  if (!protocols) {
    if (model.primary_adapter) values.add(model.primary_adapter)
    if (model.responses_ready) values.add('openai_responses')
    if (model.anthropic_ready) values.add('anthropic')
  }
  return BINDING_ADAPTER_OPTIONS.map(option => option.value as string).filter(value => values.has(value))
}

function keyProtocols(key: ApiKey) {
  const values = new Set<string>()
  for (const model of keyModelList(key)) {
    protocolsForKeyModel(model).forEach(value => values.add(value))
  }
  return BINDING_ADAPTER_OPTIONS.map(option => option.value as string).filter(value => values.has(value))
}

function keyProtocolLabel(value: string) {
  return protocolLabels[value] ?? bindingAdapterMeta(value as BindingAdapter).label
}

function keyModelProtocolLabel(model: Model) {
  const values = protocolsForKeyModel(model)
  return values.length ? values.map(keyProtocolLabel).join('、') : '暂无'
}

function keyModelRecommendedLabel(model: Model) {
  const value = model.recommended_protocol?.trim()
  return value ? keyProtocolLabel(value) : '暂无'
}

function keyModelAvailability(model: Model): KeyModelAvailability {
  const state = model.availability
  if (state === 'available' || state === 'unverified' || state === 'maintenance') return state
  if (state || model.status !== 'active' || !model.route_ready || model.probe_state === 'failed') {
    return 'maintenance'
  }
  return model.probe_state === 'passed' ? 'available' : 'unverified'
}

function studentSafeReason(reason: string, fallback: string) {
  if (!reason) return fallback
  return /(价格|报价|单价|倍率|官网|反推|基础积分|折后|加价|price|quote|cost|multiplier)/i.test(reason)
    ? fallback
    : reason
}

function keyModelReason(model: Model, availability: KeyModelAvailability) {
  const reason = model.availability_reason?.trim()
  if (reason) return studentSafeReason(reason, '老师暂未开放该模型。')
  if (availability === 'available') return '已有可调用线路。'
  if (availability === 'unverified') return '已有绑定线路，尚无通过探活的协议。'
  if (model.status === 'inactive') return '老师已停用该模型。'
  if (model.probe_state === 'failed') return '当前线路探活失败。'
  return '当前没有可接入的线路。'
}

function credentialHealth(key: ApiKey): KeyHealthItem {
  if (key.status === 'active') {
    if (key.key_prefix) {
      return {
        id: 'credential', title: '凭据', state: 'ok', value: '已签发',
        detail: `密钥前缀 ${key.key_prefix}，可以用于调用。`, action: '',
      }
    }
    return {
      id: 'credential', title: '凭据', state: 'pending', value: '已生效，待生成',
      detail: 'Key 已审批生效，但平台没有可查看的密钥前缀。',
      action: '先点「重新生成密钥」；失败时找老师重新签发。',
    }
  }
  if (key.status === 'approved') {
    return {
      id: 'credential', title: '凭据', state: 'pending', value: '待领取',
      detail: '老师已审批通过，需要你点「领取并启用」后才会签发 Key。',
      action: '点「领取并启用」；按钮不可用就找老师。',
    }
  }
  if (key.status === 'pending_mentor') {
    return {
      id: 'credential', title: '凭据', state: 'pending', value: '导师审核中',
      detail: '申请还在导师审核，尚未签发 Key。',
      action: '等待导师审核；超过一天找导师或老师询问。',
    }
  }
  if (key.status === 'pending_teacher') {
    return {
      id: 'credential', title: '凭据', state: 'pending', value: '老师终审中',
      detail: '导师已通过，等待老师终审。',
      action: '等待老师终审；超过一天找老师询问。',
    }
  }
  if (key.status === 'inactive') {
    return {
      id: 'credential', title: '凭据', state: 'blocked', value: '已停用',
      detail: '老师已停用这把 Key，继续调用会被拒绝。',
      action: '找老师确认是否恢复。',
    }
  }
  if (key.audits?.some(audit => audit.action === 'student_withdrawn')) {
    return {
      id: 'credential', title: '凭据', state: 'blocked', value: '已撤回',
      detail: '你已撤回这次申请，这把 Key 不再生效。',
      action: '如需确认撤回记录，请联系老师。',
    }
  }
  return {
    id: 'credential', title: '凭据', state: 'blocked', value: '已驳回',
    detail: '申请被驳回，这把 Key 不能调用。',
    action: '查看审核意见，有疑问请联系老师。',
  }
}

function keyHealth(key: ApiKey, availableModels: Model[], hasUnverified: boolean): KeyHealthItem[] {
  const models = keyModelList(key)
  if (!models.length) return [credentialHealth(key)]

  const catalog: KeyHealthItem = {
    id: 'catalog', title: '目录', state: 'ok', value: `已登记 ${models.length} 个模型`,
    detail: '每个模型的可用状态见下方 Key 白名单。', action: '',
  }

  const probe: KeyHealthItem = key.status !== 'active'
    ? {
        id: 'probe', title: '探活', state: 'pending', value: 'Key 当前未启用',
        detail: '已登记模型的探活记录见下方白名单。', action: '',
      }
    : availableModels.length > 0
      ? {
          id: 'probe', title: '探活', state: 'ok', value: '线路已打通',
          detail: `已确认可调用 ${availableModels.length} 个模型。`, action: '',
        }
      : hasUnverified
        ? {
            id: 'probe', title: '探活', state: 'pending', value: '待验证',
            detail: '可以尝试调用待验证的模型。', action: '',
          }
        : {
            id: 'probe', title: '探活', state: 'blocked', value: '线路不可用',
            detail: '当前白名单里的模型都在维护中。',
            action: '如需恢复线路，请联系老师并提供 Key 名称和模型名。',
          }

  return [credentialHealth(key), catalog, probe]
}

function openHarness(key: ApiKey) {
  configKey.value = key
}

function closeHarness() {
  configKey.value = null
}

async function load() {
  const sequence = ++loadSequence
  await loadState.run(async () => {
    try {
      const usageRequest = typeof studentAPI.usage === 'function'
        ? studentAPI.usage()
          .then(payload => {
            if (sequence !== loadSequence) return
            usage.value = payload
            usageLoaded.value = true
            usageFailed.value = false
          })
          .catch(() => {
            if (sequence !== loadSequence) return
            usage.value = null
            usageLoaded.value = false
            usageFailed.value = true
          })
        : Promise.resolve()
      const [organizations, apiKeys] = await Promise.all([
        studentAPI.organizations(), studentAPI.keys(), usageRequest,
      ])
      if (sequence !== loadSequence) return
      orgs.value = organizations
      keys.value = apiKeys
      error.value = ''
    } catch (caught) {
      if (sequence === loadSequence) error.value = msg(caught)
    }
  })
}

async function chooseOrg() {
  const organizationID = form.organization_id
  form.project_id = ''
  projects.value = []
  if (!organizationID) return
  try {
    const choices = await studentAPI.projects(organizationID)
    if (form.organization_id === organizationID) projects.value = choices
  } catch (caught) {
    if (form.organization_id === organizationID) error.value = msg(caught)
  }
}

function submit() {
  if (!canSubmit.value) return
  return actions.run('submit', async () => {
    error.value = ''
    try {
      const created = await studentAPI.submit({ ...form })
      submittedKeyID.value = created.id
      keys.value = [created, ...keys.value.filter(key => key.id !== created.id)]
      Object.assign(form, { organization_id: '', project_id: '', name: '', requested_monthly_credits: '200.000' })
      projects.value = []
      await load()
    } catch (caught) {
      error.value = msg(caught)
    }
  })
}

async function show(id: string) {
  try {
    detail.value = await studentAPI.key(id)
  } catch (caught) {
    error.value = msg(caught)
  }
}

function claim() {
  const approved = claimConfirm.value
  if (!approved) return
  return actions.run('claim', async () => {
    error.value = ''
    try {
      const result = await studentAPI.claim(approved.id)
      const active: ApiKey = {
        ...approved,
        status: 'active',
        progress: { current: 'active', completed_steps: [...approved.progress.completed_steps, 'claimed'] },
        key_prefix: result.key_prefix,
        claimed_at: result.claimed_at,
      }
      claimConfirm.value = null
      detail.value = { ...active, api_key: result.api_key }
      keys.value = keys.value.map(key => key.id === active.id ? active : key)
      toast.success('密钥已启用，可随时查看与复制')
      await load()
    } catch (caught) {
      error.value = msg(caught)
    }
  })
}

function removeKeyModel(keyID: string, modelID: string) {
  return actions.run(`remove:${keyID}`, async () => {
    error.value = ''
    try {
      await studentAPI.removeKeyModels(keyID, [modelID])
      const update = (key: ApiKey): ApiKey => ({
        ...key,
        models: keyModelList(key).filter(model => model.model_id !== modelID),
      })
      // 丢弃移除成功前可能捕获旧白名单的列表请求。
      loadSequence += 1
      keys.value = keys.value.map(key => key.id === keyID ? update(key) : key)
      if (detail.value?.id === keyID) detail.value = update(detail.value)
      toast.success('已从当前 Key 移除模型')
    } catch (caught) {
      error.value = msg(caught)
    }
  })
}

async function withdraw() {
  if (!withdrawConfirm.value) return
  const keyId = withdrawConfirm.value.id
  try {
    await studentAPI.withdraw(keyId)
    withdrawConfirm.value = null
    detail.value = null
    await load()
    toast.success('申请已撤回')
  } catch (caught) {
    error.value = msg(caught)
  }
}

async function regenerate(key: ApiKey) {
  if (!confirm('重新生成会使旧密钥立即失效，需在所有客户端更新为新密钥。继续？')) return
  regeneratingId.value = key.id
  try {
    const result = await studentAPI.regenerate(key.id)
    await load()
    detail.value = keys.value.find(item => item.id === key.id) ?? { ...key, api_key: result.api_key, key_prefix: result.key_prefix, status: key.status }
    toast.success('密钥已重新生成')
  } catch (caught) {
    error.value = msg(caught)
  } finally {
    regeneratingId.value = ''
  }
}

async function copy(text: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    toast.success('已复制密钥')
  } catch {
    toast.error('复制失败，请手动复制')
  }
}

async function refresh(event: Event) {
  const payload = (event as CustomEvent).detail
  const changedID = payload?.event === 'api_key.status_changed' ? changedApiKeyID(keys.value, payload.data) : null
  await load()
  if (changedID) await show(changedID)
}

function msg(caught: unknown) { return caught instanceof APIError ? caught.message : '请求失败，请稍后重试' }

onMounted(() => {
  void load()
  window.addEventListener('nebula:refresh', refresh)
})
onBeforeUnmount(() => window.removeEventListener('nebula:refresh', refresh))
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">STUDENT</p>
        <h1>申请密钥</h1>
      </div>
      <button class="button secondary" @click="load">刷新</button>
    </div>
    <p class="muted" data-testid="credit-rate-note">100 积分 = 1 元；金额统一用积分显示。</p>
    <p v-if="error" class="error banner">{{ error }}</p>
    <section class="panel key-health-guide" data-testid="key-health-guide">
      <h2>Key 可用状态怎么看</h2>
      <div class="key-health-guide-grid">
        <div><strong>凭据</strong><p>学校发给你的那串 Key 是否已签发、能证明你的身份。</p></div>
        <div><strong>目录</strong><p>这个 Key 的白名单里有哪些模型；模型不在目录里，调用会被拒绝。</p></div>
        <div><strong>探活</strong><p>网关确认请求能不能真正发到上游模型。学生端显示老师同步的线路状态，不是实时探测。</p></div>
      </div>
    </section>
    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="split" label="正在加载申请">
      <div class="grid two">
        <section class="panel">
          <h2>新申请</h2>
          <form @submit.prevent="submit">
            <label>1. 组织<select v-model="form.organization_id" @change="chooseOrg"><option value="">请选择</option><option v-for="org in orgs" :key="org.id" :value="org.id">{{ org.name }}</option></select></label>
            <label>2. 项目<select v-model="form.project_id" :disabled="!form.organization_id"><option value="">请选择</option><option v-for="project in selectableProjects" :key="project.id" :value="project.id">{{ project.name }}</option></select></label>
            <label>3. Key 名称<input v-model="form.name" maxlength="128" required></label>
            <label>4. 个人申请月额度（积分）<input v-model="form.requested_monthly_credits" inputmode="decimal" required></label>
            <button class="button primary wide" :disabled="!canSubmit || actions.pending('submit')">
              {{ actions.pending('submit') ? '提交中…' : '提交申请' }}
            </button>
          </form>
          <div v-if="submittedKeyID" class="key-models-next">
            <p class="muted" role="status">密钥申请已提交。审批通过并领取后即可添加模型。</p>
            <RouterLink
              class="button secondary"
              :to="{ name: 'student-models', query: { key: submittedKeyID } }"
              data-testid="submitted-key-models"
            >去模型广场选择模型</RouterLink>
          </div>
        </section>
        <section class="panel">
          <h2>我的申请</h2>
          <p v-if="!keys.length" class="muted">暂无进行中的申请，可在左侧提交新申请。</p>
          <article v-for="{ key, rows, availableCount, protocols, health, usage } in keyCards" :key="key.id" class="key-card">
            <div class="row">
              <div>
                <strong>{{ key.name }}</strong>
                <p class="muted">{{ key.project.name }} · 申请 {{ key.requested_monthly_credits }} 积分</p>
              </div>
              <StatusBadge :status="keyDisplayStatus(key)" />
            </div>
            <p class="muted key-credit-summary" data-testid="key-credit-summary">
              <template v-if="usageFailed">本月用量暂不可用</template>
              <template v-else-if="!usageLoaded">本月用量读取中…</template>
              <template v-else-if="usage && Number(usage.used) > 0">本月已用 <strong>{{ formatCredits(usage.used) }}</strong> 积分</template>
              <template v-else>本月未使用</template>
              · <template v-if="usageLoaded && usage?.available">余额 <strong>{{ formatCredits(usage.available) }}</strong> 积分</template>
              <template v-else>余额 —</template>
            </p>
            <StatusProgress :current="key.progress.current" :completed="key.progress.completed_steps" />
            <div v-if="rows.length" class="key-protocols" data-testid="key-protocols">
              <span class="muted">支持协议：</span>
              <span v-for="value in protocols" :key="value" class="chip protocol-chip">{{ keyProtocolLabel(value) }}</span>
              <span v-if="!protocols.length" class="muted">暂无协议信息。</span>
            </div>
            <p v-if="protocols.length" class="protocol-note">网关不做协议转换，客户端需使用对应模型支持的协议。</p>
            <dl class="key-health" data-testid="key-health">
              <div v-for="item in health" :key="item.id" class="key-health-item" :class="item.state" :data-health="item.id">
                <dt>{{ item.title }}<span class="key-health-state">{{ keyHealthStateLabels[item.state] }}</span></dt>
                <dd>
                  <strong>{{ item.value }}</strong>
                  <span>{{ item.detail }}</span>
                  <span v-if="item.action" class="key-health-action">{{ item.action }}</span>
                </dd>
              </div>
            </dl>
            <section v-if="key.status === 'active' || rows.length" class="key-whitelist" data-testid="key-whitelist">
              <h3>Key 白名单</h3>
              <p class="muted" role="status" data-testid="key-whitelist-summary">
                已登记 {{ rows.length }} · 可调用 {{ availableCount }} · 需处理 {{ rows.length - availableCount }}
              </p>
              <ul v-if="rows.length">
                <li
                  v-for="{ model, availability } in rows"
                  :key="model.model_id"
                  :data-model="model.model_id"
                  :data-availability="availability"
                  data-testid="key-model-row"
                >
                  <div class="key-model-header">
                    <span class="key-model-name">{{ model.model_id }}</span>
                    <span class="chip key-model-status" :class="availability">{{ modelAvailabilityLabels[availability] }}</span>
                  </div>
                  <small>原因：{{ keyModelReason(model, availability) }}<template v-if="availability === 'unverified'"> 可尝试调用。</template></small>
                  <small>支持协议：{{ keyModelProtocolLabel(model) }}</small>
                  <small>推荐协议：{{ keyModelRecommendedLabel(model) }}</small>
                  <small>
                    最后探活：<time v-if="model.last_probe_at" :datetime="model.last_probe_at">{{ fullTime(model.last_probe_at) }}</time>
                    <span v-else>{{ model.probe_state === 'passed' || model.probe_state === 'failed' ? '时间未同步' : '暂无记录' }}</span>
                  </small>
                  <button
                    class="button ghost key-model-remove"
                    :aria-label="`从当前 Key 移除 ${model.model_id}`"
                    :disabled="actions.pending(`remove:${key.id}`)"
                    @click="removeKeyModel(key.id, model.model_id)"
                  >从当前 Key 移除</button>
                </li>
              </ul>
              <p v-else class="muted" data-testid="empty-key-whitelist">已生效，待选择模型</p>
              <RouterLink
                v-if="key.status === 'active'"
                class="button secondary key-models-link"
                :to="{ name: 'student-models', query: { key: key.id } }"
              >去模型广场选择模型</RouterLink>
            </section>
            <div v-if="['active', 'inactive'].includes(key.status)" class="key-material">
              <p class="secret" data-testid="api-key-mask">{{ maskedKey(key) }}</p>
            </div>
            <p v-if="['active', 'inactive'].includes(key.status) && !key.key_prefix" class="muted">
              当前密钥是旧版记录，重新生成后才能查看。
              <button class="button ghost" :disabled="regeneratingId === key.id" @click="regenerate(key)">重新生成密钥</button>
            </p>
            <div class="actions key-card-actions">
              <button v-if="['active', 'inactive'].includes(key.status)" class="button secondary" @click="show(key.id)">查看密钥</button>
              <button class="button ghost" @click="show(key.id)">审核状态</button>
              <button v-if="keyCanWithdraw(key)" class="button ghost" @click="withdrawConfirm = key">撤回申请</button>
              <button v-if="key.status === 'approved'" class="button primary" @click="claimConfirm = key">领取并启用</button>
            </div>
          </article>
        </section>
      </div>
    </LoadingRegion>

    <AppDialog :open="Boolean(claimConfirm)" title="确认领取密钥" @update:open="claimConfirm = $event ? claimConfirm : null">
      <p>领取后可以在「我的申请」里查看和复制密钥。到模型广场添加模型后，就可以用这把 Key 调用。</p>
      <button class="button primary" :disabled="actions.pending('claim')" @click="claim">
        {{ actions.pending('claim') ? '领取中…' : '确认领取' }}
      </button>
    </AppDialog>

    <AppDialog :open="Boolean(withdrawConfirm)" title="确认撤回申请" @update:open="withdrawConfirm = $event ? withdrawConfirm : null">
      <p>撤回后，这次申请将结束。</p>
      <button class="button primary" @click="withdraw">确认撤回</button>
    </AppDialog>

    <AppDialog :open="Boolean(detail)" title="申请详情" @update:open="detail = $event ? detail : null">
      <template v-if="detail">
        <StatusProgress :current="detail.progress.current" :completed="detail.progress.completed_steps" />
        <div v-if="keyModelList(detail).length" class="key-protocols" data-testid="key-detail-protocols">
          <span class="muted">支持协议：</span>
          <span v-for="value in keyProtocols(detail)" :key="value" class="chip protocol-chip">{{ keyProtocolLabel(value) }}</span>
          <span v-if="!keyProtocols(detail).length" class="muted">暂无协议信息。</span>
        </div>
        <p v-if="keyProtocols(detail).length" class="protocol-note">网关不做协议转换，客户端需使用对应模型支持的协议。</p>
        <p>学生申请 {{ detail.requested_monthly_credits }} · 导师审核 {{ detail.mentor_monthly_credits ?? '—' }} · 审批额度 {{ detail.monthly_credits ?? '—' }} 积分</p>
        <p v-if="memberBalance">当前月额度 {{ detail.member_monthly_credits }} · 已用 {{ detail.member_used_credits }} · {{ memberBalance.label }} {{ memberBalance.amount }} 积分</p>
        <p v-if="keyModelList(detail).length">{{ keyModelList(detail).map(model => `${model.model_id}（${keyModelProtocolLabel(model)} · 推荐 ${keyModelRecommendedLabel(model)}）`).join('、') }}</p>
        <div v-if="keyCanWithdraw(detail)" class="actions key-card-actions">
          <button class="button ghost" @click="withdrawConfirm = detail">撤回申请</button>
        </div>
        <div v-if="detail.api_key" class="key-material">
          <p class="secret" data-testid="api-key-detail">{{ detail.api_key }}</p>
          <div class="actions key-card-actions">
            <button class="button secondary" data-testid="copy-api-key" @click="copy(detail.api_key!)">复制密钥</button>
            <button v-if="keySupportsHarness(detail)" class="button secondary" data-testid="configure-api-key" @click="openHarness(detail)">一键配置</button>
          </div>
        </div>
        <p v-else-if="['active', 'inactive'].includes(detail.status)" class="muted">
          密钥暂不可读，请点击下方按钮重新生成。
          <button class="button ghost" :disabled="regeneratingId === detail.id" @click="regenerate(detail)">重新生成密钥</button>
        </p>
        <div v-if="detail.status === 'active'" class="key-models-next">
          <p v-if="!keyModelList(detail).length" class="muted">已生效，待选择模型</p>
          <RouterLink
            class="button primary"
            :to="{ name: 'student-models', query: { key: detail.id } }"
            data-testid="detail-key-models"
          >去模型广场选择模型</RouterLink>
        </div>
      </template>
    </AppDialog>

    <AppDialog :open="Boolean(configKey)" :title="harnessTitle" :wide="harnessWide" @update:open="open => { if (!open) closeHarness() }">
      <StudentHarnessConfig
        v-if="configKey?.api_key"
        :api-key="configKey.api_key"
        :models="keyModelList(configKey)"
        :key-prefix="configKey.key_prefix"
        :key-name="configKey.name"
      />
    </AppDialog>
  </section>
</template>

<style scoped>
.key-health-guide { display: grid; gap: 10px; margin-bottom: 16px; }
.key-health-guide h2 { margin: 0; font-size: 18px; }
.key-health-guide-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; }
.key-health-guide-grid div { padding: 10px 12px; border-radius: 10px; background: #f8f9fc; }
.key-health-guide-grid strong { display: block; margin-bottom: 4px; }
.key-health-guide-grid p { margin: 0; color: var(--ink-soft); font-size: 12px; line-height: 1.6; }
.key-protocols { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 10px; }
.key-protocols .protocol-chip { background: #eef7f2; color: #1d6b52; }
.key-whitelist, .key-models-next { display: grid; gap: 8px; margin-top: 12px; }
.key-whitelist h3, .key-whitelist p, .key-models-next p { margin: 0; }
.key-whitelist h3 { font-size: 15px; }
.key-whitelist ul { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.key-whitelist li { display: grid; gap: 3px; padding: 8px 10px; border-radius: 8px; background: #f8f9fc; }
.key-model-header { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
.key-model-name { font-weight: 650; overflow-wrap: anywhere; }
.key-model-status.available { background: #e8f7ee; color: #1d6b3a; }
.key-model-status.unverified { background: #fff1d6; color: #8a4b00; }
.key-model-status.maintenance { background: #fdecec; color: #a12828; }
.key-whitelist small { color: var(--ink-soft); font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.key-model-remove, .key-models-link, .key-models-next .button { justify-self: start; }
.key-model-remove { margin-top: 3px; padding: 5px 8px; font-size: 12px; }
.protocol-note { margin: 6px 0 0; color: var(--ink-soft); font-size: 12px; line-height: 1.6; }
.key-health { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin: 12px 0 0; }
.key-health-item { padding: 10px 12px; border-left: 3px solid #cbd5e1; border-radius: 10px; background: #f8f9fc; }
.key-health-item:only-child { grid-column: 1 / -1; }
.key-health-item.ok { border-left-color: #2f9e5f; }
.key-health-item.pending { border-left-color: #d4931a; background: #fffaf0; }
.key-health-item.blocked { border-left-color: #c0392b; background: #fff5f4; }
.key-health-item dt { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-weight: 650; }
.key-health-state { padding: 2px 7px; border-radius: 999px; background: #e8edf5; color: #475569; font-size: 11px; font-weight: 600; }
.key-health-item.ok .key-health-state { background: #e8f7ee; color: #1d6b3a; }
.key-health-item.pending .key-health-state { background: #fff1d6; color: #8a4b00; }
.key-health-item.blocked .key-health-state { background: #fdecec; color: #a12828; }
.key-health-item dd { display: grid; gap: 4px; margin: 6px 0 0; }
.key-health-item dd strong { font-size: 13px; }
.key-health-item dd span { color: var(--ink-soft); font-size: 12px; line-height: 1.6; }
.key-health-item .key-health-action { color: #8a4b00; }
@media (max-width: 960px) {
  .key-health-guide-grid,
  .key-health { grid-template-columns: 1fr; }
}
</style>
