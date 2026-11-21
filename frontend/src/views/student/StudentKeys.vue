<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { studentAPI } from '../../api/student'
import type { ApiKey, BindingAdapter, Model, Organization, RequestedModel, StudentProject } from '../../api/types'
import { APIError } from '../../api/client'
import StatusProgress from '../../components/StatusProgress.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import StudentHarnessConfig from '../../components/StudentHarnessConfig.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import { changedApiKeyID } from '../../utils/keyStatusEvents'
import { sortModelsByRecency } from '../../utils/modelSort'
import { mergeStudentPlazaModels } from '../../utils/studentModels'
import { calculateCredits, quotaBalance } from '../../utils/format'
import { modelCreditsLabel } from '../../utils/modelPlazaDisplay'
import { harnessDialogTitle, keyCanWithdraw, keyHarnessSupport } from '../../utils/studentHarnessConfig'
import { BINDING_ADAPTER_OPTIONS, bindingAdapterMeta } from '../../utils/bindingAdapters'

type KeyModel = Model & { supported_protocols?: string[] }
type KeyHealthState = 'ok' | 'pending' | 'blocked'
type KeyHealthItem = {
  id: 'credential' | 'catalog' | 'pricing' | 'probe'
  title: string
  state: KeyHealthState
  value: string
  detail: string
  action: string
}

const orgs = ref<Organization[]>([])
const projects = ref<StudentProject[]>([])
const models = ref<Model[]>([])
const keys = ref<ApiKey[]>([])
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
const modelQuery = ref('')
const toast = useToast()
const loadState = useLoadState()
const form = reactive({ organization_id: '', project_id: '', models: [] as RequestedModel[], name: '', requested_monthly_credits: '200.000' })

const selectedKeys = computed(() => new Set(form.models.map(model => model.model_id.toLowerCase())))
const activeLibrary = computed(() => sortModelsByRecency(models.value.filter(model => model.status === 'active' && model.route_ready)))
const keyDisplayStatus = (key: ApiKey) => {
  if (key.status === 'active' && !key.route_ready) return 'inactive'
  if (key.status === 'rejected' && key.audits?.some(audit => audit.action === 'student_withdrawn')) return 'withdrawn'
  return key.status
}
const filteredModels = computed(() => activeLibrary.value.filter(model =>
  !modelQuery.value || `${model.display_name} ${model.model_id} ${model.capabilities.join(' ')}`.toLowerCase().includes(modelQuery.value.toLowerCase()),
))
const liveKeyStatuses = new Set<ApiKey['status']>(['pending_mentor', 'pending_teacher', 'approved', 'active', 'inactive'])
const blockedProjectIds = computed(() => new Set(keys.value.filter(key => liveKeyStatuses.has(key.status)).map(key => key.project.id)))
const selectableProjects = computed(() => projects.value.filter(project =>
  project.status === 'active' && !blockedProjectIds.value.has(project.id),
))
const canSubmit = computed(() => Boolean(form.organization_id && form.project_id && form.name.trim() && form.requested_monthly_credits && form.models.length > 0 && form.models.length <= 100))
const maskedKey = (key: ApiKey) => key.key_prefix ? key.key_prefix + '••••••' : '密钥已生成，点击「查看密钥」'
const keySupportsHarness = (key: ApiKey) => {
  const support = keyHarnessSupport(key.models)
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

function keyModelList(key: ApiKey) {
  return (key.models ?? []) as KeyModel[]
}

// Key 下的模型与 /v1/models 使用同一组协议字段。
function protocolsForKeyModel(model: KeyModel) {
  const values = new Set((model.protocols ?? model.supported_protocols ?? []).filter(Boolean))
  if (!values.size) {
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

function keyModelProtocolLabel(model: KeyModel) {
  const values = protocolsForKeyModel(model)
  return values.length ? values.map(keyProtocolLabel).join('、') : '协议待同步'
}

function keyModelRecommendedLabel(model: KeyModel) {
  const value = model.recommended_protocol?.trim()
  return value ? keyProtocolLabel(value) : '推荐协议待同步'
}

function modelPriced(model: Model) {
  const raw = model.credit_multiplier?.trim()
  return Boolean(raw) && /^\d+(?:\.\d+)?$/.test(raw!) && Number(raw) > 0
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
      action: '需要继续使用请重新提交申请；不清楚状态找老师确认。',
    }
  }
  return {
    id: 'credential', title: '凭据', state: 'blocked', value: '已驳回',
    detail: '申请被驳回，这把 Key 不能调用。',
    action: '查看审核意见，修改后重新申请；不清楚原因找老师。',
  }
}

function keyHealth(key: ApiKey): KeyHealthItem[] {
  const models = keyModelList(key)
  const active = models.filter(model => model.status === 'active')
  const inactiveCount = models.length - active.length
  const priced = models.filter(modelPriced)
  const unpricedCount = models.length - priced.length
  const routed = active.filter(model => model.route_ready)

  const catalog: KeyHealthItem = models.length === 0
    ? {
        id: 'catalog', title: '目录', state: 'blocked', value: '白名单为空',
        detail: '这把 Key 没有绑定任何模型，调用会返回 model_not_allowed。',
        action: key.status === 'active' ? '到「模型广场」把需要的模型添加到这把 Key。' : 'Key 生效后可在「模型广场」添加模型。',
      }
    : active.length === 0
      ? {
          id: 'catalog', title: '目录', state: 'blocked', value: '模型全部停用',
          detail: `白名单 ${models.length} 个模型都已停用。`,
          action: '找老师重新启用模型，也可在「模型广场」挑选其他已启用模型。',
        }
      : inactiveCount > 0
        ? {
            id: 'catalog', title: '目录', state: 'pending', value: `${active.length}/${models.length} 个模型可用`,
            detail: `有 ${inactiveCount} 个模型已停用，停用模型无法调用。`,
            action: '找老师确认停用模型是否恢复。',
          }
        : {
            id: 'catalog', title: '目录', state: 'ok', value: `已登记 ${models.length} 个模型`,
            detail: '白名单里的模型都在架。', action: '',
          }

  const pricing: KeyHealthItem = models.length === 0
    ? {
        id: 'pricing', title: '报价', state: 'pending', value: '暂无可报价模型',
        detail: '目录为空，暂无模型需要报价。',
        action: '先找老师把模型加入目录。',
      }
    : unpricedCount > 0
      ? {
          id: 'pricing', title: '报价', state: 'blocked', value: `缺少 ${unpricedCount} 个模型的报价`,
          detail: `已配置 ${priced.length}/${models.length} 个模型；没有报价的模型不会对学生开放。`,
          action: '找老师补学校倍率（报价）；补好前这些模型会显示暂不可用。',
        }
      : {
          id: 'pricing', title: '报价', state: 'ok', value: `已配置 ${priced.length} 个模型`,
          detail: '调用时会按模型卡片上的点数扣费。', action: '',
        }

  const probe: KeyHealthItem = key.status !== 'active'
    ? {
        id: 'probe', title: '探活', state: 'pending', value: '审批后检测',
        detail: 'Key 尚未生效，现在还没有可探测的线路。',
        action: '先完成审批并领取；领取后仍不可用找老师。',
      }
    : key.route_ready && routed.length > 0
      ? {
          id: 'probe', title: '探活', state: 'ok', value: '线路已打通',
          detail: `可路由模型：${routed.map(model => model.model_id).join('、')}。`, action: '',
        }
      : {
          id: 'probe', title: '探活', state: 'blocked', value: '线路不可用',
          detail: '当前白名单里没有可路由的模型，请求发不出去。',
          action: '等待老师恢复线路后点「刷新」；持续不可用请找老师，并附上 Key 名称和模型名。',
        }

  return [credentialHealth(key), catalog, pricing, probe]
}

function openHarness(key: ApiKey) {
  configKey.value = key
}

function closeHarness() {
  configKey.value = null
}

async function load() {
  await loadState.run(async () => {
    try {
      [orgs.value, keys.value] = await Promise.all([studentAPI.organizations(), studentAPI.keys()])
      models.value = mergeStudentPlazaModels(await studentAPI.models())
      error.value = ''
    } catch (caught) {
      error.value = msg(caught)
    }
  })
}

async function chooseOrg() {
  if (form.models.length && !confirm('切换组织将清空项目和已选模型，是否继续？')) return
  form.project_id = ''
  form.models = []
  projects.value = form.organization_id ? await studentAPI.projects(form.organization_id) : []
}

function selected(modelID: string) { return selectedKeys.value.has(modelID.toLowerCase()) }

function toggleModel(model: Model) {
  const index = form.models.findIndex(value => value.model_id.toLowerCase() === model.model_id.toLowerCase())
  if (index >= 0) form.models.splice(index, 1)
  else if (form.models.length < 100) form.models.push({ model_id: model.model_id })
}

async function submit() {
  if (!canSubmit.value) return
  try {
    await studentAPI.submit({ ...form })
    Object.assign(form, { organization_id: '', project_id: '', models: [], name: '', requested_monthly_credits: '200.000' })
    projects.value = []
    await load()
    toast.success('密钥申请已提交')
  } catch (caught) {
    error.value = msg(caught)
  }
}

async function show(id: string) {
  try {
    detail.value = await studentAPI.key(id)
  } catch (caught) {
    error.value = msg(caught)
  }
}

async function claim() {
  if (!claimConfirm.value) return
  const keyId = claimConfirm.value.id
  try {
    await studentAPI.claim(keyId)
    claimConfirm.value = null
    await load()
    detail.value = await studentAPI.key(keyId)
    toast.success('密钥已启用，可随时查看与复制')
  } catch (caught) {
    error.value = msg(caught)
  }
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
    <p v-if="error" class="error banner">{{ error }}</p>
    <section class="panel key-health-guide" data-testid="key-health-guide">
      <h2>Key 可用状态怎么看</h2>
      <div class="key-health-guide-grid">
        <div><strong>凭据</strong><p>学校发给你的那串 Key 是否已签发、能证明你的身份。</p></div>
        <div><strong>目录</strong><p>这个 Key 的白名单里有哪些模型；模型不在目录里，调用会被拒绝。</p></div>
        <div><strong>报价</strong><p>每个模型调用一次扣多少积分；没有报价的模型不会对学生开放。</p></div>
        <div><strong>探活</strong><p>网关确认请求能不能真正发到上游模型。学生端显示老师同步的线路状态，不是实时探测。</p></div>
      </div>
      <p class="muted">四项都正常才代表这把 Key 现在可以稳定调用；出现「需处理」时，按卡片里的建议找老师。</p>
    </section>
    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="split" label="正在加载申请">
      <div class="grid two">
        <section class="panel">
          <h2>新申请</h2>
          <form @submit.prevent="submit">
            <label>1. 组织<select v-model="form.organization_id" @change="chooseOrg"><option value="">请选择</option><option v-for="org in orgs" :key="org.id" :value="org.id">{{ org.name }}</option></select></label>
            <label>2. 项目<select v-model="form.project_id" :disabled="!form.organization_id"><option value="">请选择</option><option v-for="project in selectableProjects" :key="project.id" :value="project.id">{{ project.name }}</option></select></label>
            <fieldset :disabled="!form.project_id">
              <legend>3. 模型白名单（{{ form.models.length }}/100）</legend>
              <template v-if="activeLibrary.length">
                <input v-model="modelQuery" placeholder="搜索我的模型">
                <div class="model-quick-list">
                  <div v-for="model in filteredModels" :key="model.model_id" class="list-row">
                    <div><strong>{{ model.display_name }}</strong><p class="muted">{{ model.model_id }} · {{ modelCreditsLabel(model) }}</p></div>
                    <button type="button" class="button ghost" @click="toggleModel(model)">{{ selected(model.model_id) ? '取消' : '选择' }}</button>
                  </div>
                </div>
                <div class="chip-list"><span v-for="model in form.models" :key="model.model_id" class="chip">{{ model.model_id }}</span></div>
              </template>
            </fieldset>
            <label>4. Key 名称<input v-model="form.name" maxlength="128" required></label>
            <label>5. 个人申请月额度（credits）<input v-model="form.requested_monthly_credits" inputmode="decimal" required></label>
            <button class="button primary wide" :disabled="!canSubmit">提交申请</button>
          </form>
        </section>
        <section class="panel">
          <h2>我的申请</h2>
          <p v-if="!keys.length" class="muted">暂无进行中的申请，可在左侧提交新申请。</p>
          <article v-for="key in keys" :key="key.id" class="key-card">
            <div class="row">
              <div>
                <strong>{{ key.name }}</strong>
                <p class="muted">{{ key.project.name }} · 申请 {{ key.requested_monthly_credits }} credits</p>
              </div>
              <StatusBadge :status="keyDisplayStatus(key)" />
            </div>
            <StatusProgress :current="key.progress.current" :completed="key.progress.completed_steps" />
            <div class="key-protocols" data-testid="key-protocols">
              <span class="muted">支持协议：</span>
              <span v-for="value in keyProtocols(key)" :key="value" class="chip protocol-chip">{{ keyProtocolLabel(value) }}</span>
              <span v-if="!keyProtocols(key).length" class="muted">协议信息待同步，请联系老师确认。</span>
            </div>
            <p class="protocol-note">网关不做协议转换；客户端必须直接使用上述协议之一。</p>
            <dl class="key-health" data-testid="key-health">
              <div v-for="item in keyHealth(key)" :key="item.id" class="key-health-item" :class="item.state" :data-health="item.id">
                <dt>{{ item.title }}<span class="key-health-state">{{ keyHealthStateLabels[item.state] }}</span></dt>
                <dd>
                  <strong>{{ item.value }}</strong>
                  <span>{{ item.detail }}</span>
                  <span v-if="item.action" class="key-health-action">{{ item.action }}</span>
                </dd>
              </div>
            </dl>
            <div v-if="key.status === 'active' || keyModelList(key).length" class="key-model-protocols" data-testid="authorized-models">
              <strong>已授权模型</strong>
              <ul v-if="keyModelList(key).length">
                <li v-for="model in keyModelList(key)" :key="model.model_id" data-testid="key-model-protocol">
                  <span class="key-model-name">{{ model.model_id }}</span>
                  <small>支持协议：{{ keyModelProtocolLabel(model) }}</small>
                  <small>推荐协议：{{ keyModelRecommendedLabel(model) }}</small>
                </li>
              </ul>
              <span v-else class="muted">暂无</span>
            </div>
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
      <p>领取后密钥会保存在平台中，你随时可以在「我的申请」里查看、复制，并通过 CC-switch 一键导入到 Codex 或 Claude Code。</p>
      <button class="button primary" @click="claim">确认领取</button>
    </AppDialog>

    <AppDialog :open="Boolean(withdrawConfirm)" title="确认撤回申请" @update:open="withdrawConfirm = $event ? withdrawConfirm : null">
      <p>撤回后该申请将结束，你可以重新为同一项目提交新的申请。</p>
      <button class="button primary" @click="withdraw">确认撤回</button>
    </AppDialog>

    <AppDialog :open="Boolean(detail)" title="申请详情" @update:open="detail = $event ? detail : null">
      <template v-if="detail">
        <StatusProgress :current="detail.progress.current" :completed="detail.progress.completed_steps" />
        <div class="key-protocols" data-testid="key-detail-protocols">
          <span class="muted">支持协议：</span>
          <span v-for="value in keyProtocols(detail)" :key="value" class="chip protocol-chip">{{ keyProtocolLabel(value) }}</span>
          <span v-if="!keyProtocols(detail).length" class="muted">协议信息待同步，请联系老师确认。</span>
        </div>
        <p class="protocol-note">网关不做协议转换；客户端必须直接使用上述协议之一。</p>
        <p>学生申请 {{ detail.requested_monthly_credits }} · 导师审核 {{ detail.mentor_monthly_credits ?? '—' }} · 审批额度 {{ detail.monthly_credits ?? '—' }} 积分</p>
        <p v-if="memberBalance">当前月额度 {{ detail.member_monthly_credits }} · 学校已用 {{ detail.member_used_credits }} · {{ memberBalance.label }} {{ memberBalance.amount }} 积分</p>
        <p>{{ keyModelList(detail).map(model => `${model.model_id}（${keyModelProtocolLabel(model)} · 推荐 ${keyModelRecommendedLabel(model)} · ${modelCreditsLabel(model)}）`).join('、') }}</p>
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
      </template>
    </AppDialog>

    <AppDialog :open="Boolean(configKey)" :title="harnessTitle" :wide="harnessWide" @update:open="open => { if (!open) closeHarness() }">
      <StudentHarnessConfig
        v-if="configKey?.api_key"
        :api-key="configKey.api_key"
        :models="configKey.models"
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
.key-model-protocols { display: grid; gap: 6px; margin-top: 10px; }
.key-model-protocols ul { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.key-model-protocols li { display: grid; gap: 2px; padding: 8px 10px; border-radius: 8px; background: #f8f9fc; }
.key-model-protocols .key-model-name { font-weight: 650; overflow-wrap: anywhere; }
.key-model-protocols small { color: var(--ink-soft); font-size: 12px; line-height: 1.6; }
.protocol-note { margin: 6px 0 0; color: var(--ink-soft); font-size: 12px; line-height: 1.6; }
.key-health { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin: 12px 0 0; }
.key-health-item { padding: 10px 12px; border-left: 3px solid #cbd5e1; border-radius: 10px; background: #f8f9fc; }
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
