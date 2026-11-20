<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { studentAPI } from '../../api/student'
import type { ApiKey, Model, Organization, RequestedModel, StudentProject } from '../../api/types'
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
import { harnessDialogTitle, keyHarnessSupport } from '../../utils/studentHarnessConfig'

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
const canWithdraw = (key: ApiKey) => ['pending_mentor', 'pending_teacher'].includes(key.status)
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
            <div v-if="['active', 'inactive'].includes(key.status)" class="key-material">
              <p class="secret">{{ maskedKey(key) }}</p>
            </div>
            <p v-if="['active', 'inactive'].includes(key.status) && !key.key_prefix" class="muted">
              当前密钥是旧版记录，重新生成后才能查看。
              <button class="button ghost" :disabled="regeneratingId === key.id" @click="regenerate(key)">重新生成密钥</button>
            </p>
            <div class="actions key-card-actions">
              <button v-if="['active', 'inactive'].includes(key.status)" class="button secondary" @click="show(key.id)">查看密钥</button>
              <button class="button ghost" @click="show(key.id)">审核状态</button>
              <button v-if="canWithdraw(key)" class="button ghost" @click="withdrawConfirm = key">撤回申请</button>
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
        <p>学生申请 {{ detail.requested_monthly_credits }} · 导师审核 {{ detail.mentor_monthly_credits ?? '—' }} · 审批额度 {{ detail.monthly_credits ?? '—' }} 积分</p>
        <p v-if="memberBalance">当前月额度 {{ detail.member_monthly_credits }} · 学校已用 {{ detail.member_used_credits }} · {{ memberBalance.label }} {{ memberBalance.amount }} 积分</p>
        <p>{{ detail.models.map(model => `${model.model_id} (${modelCreditsLabel(model)})`).join('、') }}</p>
        <div v-if="canWithdraw(detail)" class="actions key-card-actions">
          <button class="button ghost" @click="withdrawConfirm = detail">撤回申请</button>
        </div>
        <div v-if="detail.api_key" class="key-material">
          <p class="secret">{{ detail.api_key }}</p>
          <div class="actions key-card-actions">
            <button class="button secondary" @click="copy(detail.api_key!)">复制密钥</button>
            <button v-if="keySupportsHarness(detail)" class="button secondary" @click="openHarness(detail)">一键配置</button>
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
