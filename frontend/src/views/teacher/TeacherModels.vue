<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { teacherAPI } from '../../api/teacher'
import type { Binding, BindingAdapter, BindingRouteProbeEndpoint, BindingRouteProbeStep, Model, ModelStatus, Provider } from '../../api/types'
import { catalogDisplayName, catalogFieldEntries } from '../../utils/catalogEntry'
import { BINDING_ADAPTER_OPTIONS, bindingAdapterMeta, defaultBindingAdapter, harnessConfigSnippet } from '../../utils/bindingAdapters'
import { sortModelsByRecency } from '../../utils/modelSort'
import { APIError } from '../../api/client'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import BeichenSchoolModels from './BeichenSchoolModels.vue'
import { isBeichenProvider } from '../../utils/beichenModels'

const DEFAULT_CONTEXT_WINDOW = 128_000
const DEFAULT_MAX_INPUT_TOKENS = 128_000
const DEFAULT_MAX_OUTPUT_TOKENS = 4_000
const adapterOptions = BINDING_ADAPTER_OPTIONS
function isImageGenerationModel(modelId: string) {
  const normalized = modelId.trim().toLowerCase()
  return ['gpt-image', 'dall-e', 'dalle'].some(token => normalized.includes(token))
}

function bindingRouteProbePlanFor(modelId: string): { endpoint: BindingRouteProbeEndpoint; label: string }[] {
  if (isImageGenerationModel(modelId)) {
    return [{ endpoint: 'images_generations', label: 'Images' }]
  }
  return [
    { endpoint: 'chat_completions', label: 'Chat Completions' },
    { endpoint: 'responses', label: 'Responses (Codex)' },
    { endpoint: 'messages', label: 'Messages (Claude Code)' },
  ]
}

const rows = ref<Model[]>([])
const providers = ref<Provider[]>([])
const tab = ref<'common' | 'all'>('all')
const query = ref('')
const selected = ref<{ model: Model; bindings: Binding[] } | null>(null)
const selectedLine = ref<Model | null>(null)
const dialogOpen = ref(false)
const step = ref(1)
const error = ref('')
const catalog = ref<any[]>([])
const catalogPage = ref(1)
const catalogTotal = ref(0)
const bindingOpen = ref(false)
const editingBinding = ref<Binding | null>(null)
const bindingProbing = ref(false)
const bindingProbeProgress = ref('')
const bindingProbeSteps = ref<BindingRouteProbeStep[]>([])
const toast = useToast()
const loadState = useLoadState()
const route = useRoute()
const router = useRouter()
const modelForm = reactive<any>({
  model_id: '',
  display_name: '',
  description: '',
  category: 'text',
  capabilities: [],
  input_modalities: ['text'],
  output_modalities: ['text'],
  context_window: DEFAULT_CONTEXT_WINDOW,
  max_input_tokens: DEFAULT_MAX_INPUT_TOKENS,
  max_output_tokens: DEFAULT_MAX_OUTPUT_TOKENS,
  is_common: false,
  status: 'pending_configuration' as ModelStatus,
  credit_multiplier: '0.00',
})
const bindingForm = reactive({
  provider_id: '',
  upstream_model_name: '',
  adapter: 'openai_compatible' as BindingAdapter,
  priority: 100,
  status: 'active' as 'active' | 'inactive',
})

const schoolProviders = computed(() => providers.value.filter(provider => isBeichenProvider(provider.base_url)))
const schoolProviderIds = computed(() => new Set(schoolProviders.value.map(provider => provider.id)))
const commonIds = computed(() => new Set(rows.value.filter(model => model.is_common).map(model => model.model_id.toLowerCase())))
const otherModels = computed(() => rows.value.filter(model => !schoolProviderIds.value.has(model.provider_id ?? '')))
const filtered = computed(() => sortModelsByRecency(otherModels.value
  .filter(model => (tab.value === 'all' || model.is_common) && (!query.value || `${model.display_name} ${model.model_id}`.toLowerCase().includes(query.value.toLowerCase())))))
const activeProviders = computed(() => providers.value.filter(provider => provider.status === 'active' && !isBeichenProvider(provider.base_url)))

function harnessConfig(binding: Binding) {
  return harnessConfigSnippet(modelForm.model_id, binding.adapter, `${window.location.origin}/v1`)
}
const canEnableModel = computed(() => {
  const multiplier = Number(modelForm.credit_multiplier)
  return Boolean(selected.value?.model.route_ready) && Number.isFinite(multiplier) && multiplier > 0
})
const visibleBindings = computed(() => selected.value?.bindings ?? [])
const maxCatalogPage = computed(() => Math.max(1, Math.ceil(catalogTotal.value / 20)))

function lineKey(model: Model) {
  return `${model.model_id}:${model.credential_id || 'manual'}`
}

function detailCredentialId() {
  return selectedLine.value?.credential_id
}

async function load() {
  await loadState.run(async () => {
    try {
      [rows.value, providers.value] = await Promise.all([teacherAPI.models(), teacherAPI.providers()])
      if (dialogOpen.value) return
      const target = String(route.query.model || '')
      const credentialId = String(route.query.credential_id || '')
      if (target) {
        const row = rows.value.find(item => item.model_id === target && (!credentialId || item.credential_id === credentialId))
        if (row && !schoolProviderIds.value.has(row.provider_id ?? '')) await open(row)
      }
    } catch (e) {
      error.value = msg(e)
    }
  })
}

function defaultDescription() {
  const modelID = String(modelForm.model_id).trim()
  const name = String(modelForm.display_name || modelID).trim()
  if (!name) return modelID
  if (name.toLowerCase() === modelID.toLowerCase()) return name
  return `${name}（${modelID}）`
}

function normalizeMultiplier(value: string) {
  const parsed = Number(String(value).trim())
  if (Number.isNaN(parsed) || parsed < 0) return '0.00'
  return parsed.toFixed(2)
}

function ensureBaseDefaults() {
  if (!String(modelForm.description || '').trim()) modelForm.description = defaultDescription()
  if (!modelForm.category) modelForm.category = 'text'
  if (!Array.isArray(modelForm.capabilities)) modelForm.capabilities = []
  if (!Array.isArray(modelForm.input_modalities) || !modelForm.input_modalities.length) modelForm.input_modalities = ['text']
  if (!Array.isArray(modelForm.output_modalities) || !modelForm.output_modalities.length) modelForm.output_modalities = ['text']
  if (!modelForm.context_window) modelForm.context_window = DEFAULT_CONTEXT_WINDOW
  if (!modelForm.max_input_tokens) modelForm.max_input_tokens = DEFAULT_MAX_INPUT_TOKENS
  if (!modelForm.max_output_tokens) modelForm.max_output_tokens = DEFAULT_MAX_OUTPUT_TOKENS
  if (!modelForm.credit_multiplier) modelForm.credit_multiplier = '0.00'
  modelForm.credit_multiplier = normalizeMultiplier(modelForm.credit_multiplier)
  if (typeof modelForm.is_common !== 'boolean') modelForm.is_common = false
}

function reset() {
  Object.assign(modelForm, {
    model_id: '', display_name: '', description: '', category: 'text', capabilities: [],
    input_modalities: ['text'], output_modalities: ['text'],
    context_window: DEFAULT_CONTEXT_WINDOW, max_input_tokens: DEFAULT_MAX_INPUT_TOKENS, max_output_tokens: DEFAULT_MAX_OUTPUT_TOKENS,
    is_common: false, status: 'pending_configuration', credit_multiplier: '0.00',
  })
  selected.value = null
  step.value = 1
  dialogOpen.value = true
  catalog.value = []
  catalogPage.value = 1
  catalogTotal.value = 0
  void searchCatalog()
}

async function open(model: Model) {
  selectedLine.value = model
  selected.value = await teacherAPI.model(model.model_id, model.credential_id)
  Object.assign(modelForm, selected.value.model)
  step.value = 2
  dialogOpen.value = true
  const nextQuery: Record<string, string> = { model: model.model_id }
  if (model.credential_id) nextQuery.credential_id = model.credential_id
  await router.replace({ query: nextQuery })
}

function close() {
  dialogOpen.value = false
  selected.value = null
  selectedLine.value = null
  bindingOpen.value = false
  void router.replace({ query: {} })
}

function canOpenStep(target: number) {
  if (target === 1) return true
  if (selected.value) return true
  return target === 2 && Boolean(String(modelForm.model_id).trim() && String(modelForm.display_name).trim())
}

function goToStep(target: number) {
  if (!canOpenStep(target)) return
  if (target === 2) ensureBaseDefaults()
  step.value = target
}

async function searchCatalog() {
  const activeProvider = activeProviders.value[0]
  if (!activeProvider) {
    catalog.value = []
    catalogTotal.value = 0
    error.value = '请先配置并启用供应商'
    return
  }
  try {
    const page = await teacherAPI.providerCatalogCandidates(activeProvider.id, modelForm.model_id, catalogPage.value, 20)
    catalog.value = page.items
    catalogTotal.value = page.total
    error.value = ''
  } catch (e) {
    catalog.value = []
    catalogTotal.value = 0
    error.value = msg(e)
  }
}

function chooseCandidate(item: any) {
  modelForm.model_id = item.model_id
  modelForm.display_name = catalogDisplayName(item)
  modelForm.description = Array.from(String(item.description || '')).slice(0, 2048).join('') || defaultDescription()
  catalog.value = []
  ensureBaseDefaults()
  step.value = 2
}

async function saveBase() {
  try {
    ensureBaseDefaults()
    const body: any = {
      model_id: modelForm.model_id,
      display_name: modelForm.display_name,
      description: modelForm.description,
      category: modelForm.category,
      capabilities: modelForm.capabilities,
      input_modalities: modelForm.input_modalities,
      output_modalities: modelForm.output_modalities,
      context_window: modelForm.context_window,
      max_input_tokens: modelForm.max_input_tokens,
      max_output_tokens: modelForm.max_output_tokens,
      is_common: modelForm.is_common,
    }
    if (selected.value) {
      if (modelForm.credit_multiplier !== selected.value.model.credit_multiplier) {
        body.credit_multiplier = modelForm.credit_multiplier
      }
      await teacherAPI.updateModel(body)
    } else {
      await teacherAPI.createModel({ ...body, credit_multiplier: modelForm.credit_multiplier, status: 'pending_configuration' })
    }
    selected.value = await teacherAPI.model(modelForm.model_id, detailCredentialId())
    Object.assign(modelForm, selected.value.model)
    toast.success('配置已保存')
    step.value = 3
    const nextQuery: Record<string, string> = { model: modelForm.model_id }
    const credentialId = detailCredentialId()
    if (credentialId) nextQuery.credential_id = credentialId
    await router.replace({ query: nextQuery })
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

function openBinding(binding?: Binding) {
  editingBinding.value = binding || null
  bindingProbing.value = false
  bindingProbeProgress.value = ''
  bindingProbeSteps.value = []
  Object.assign(bindingForm, {
    provider_id: binding?.provider_id || selectedLine.value?.provider_id || activeProviders.value[0]?.id || '',
    upstream_model_name: binding?.upstream_model_name || modelForm.model_id,
    adapter: binding?.adapter || defaultBindingAdapter(modelForm.model_id),
    priority: binding?.priority || 100,
    status: binding?.status || 'active',
  })
  bindingOpen.value = true
}

async function saveBinding() {
  try {
    if (editingBinding.value) {
      await teacherAPI.updateBinding(editingBinding.value.id, bindingForm)
      selected.value = await teacherAPI.model(modelForm.model_id, detailCredentialId())
      bindingOpen.value = false
      toast.success('路由已保存')
      await load()
      return
    }
    bindingProbing.value = true
    bindingProbeSteps.value = []
    const probePlan = bindingRouteProbePlanFor(bindingForm.upstream_model_name || modelForm.model_id)
    const probedAdapters: BindingAdapter[] = []
    for (let index = 0; index < probePlan.length; index += 1) {
      const item = probePlan[index]
      bindingProbeProgress.value = `正在探测 ${item.label}（${index + 1}/${probePlan.length}）…`
      const step = await teacherAPI.probeBindingRoute({
        provider_id: bindingForm.provider_id,
        upstream_model_name: bindingForm.upstream_model_name,
        endpoint: item.endpoint,
      })
      bindingProbeSteps.value = [...bindingProbeSteps.value, step]
      if (step.ok) probedAdapters.push(step.adapter)
    }
    bindingProbeProgress.value = ''
    if (!probedAdapters.length) {
      error.value = '上游未探测到可用端点，请检查供应商 Key 与上游模型名'
      return
    }
    await teacherAPI.createBinding(modelForm.model_id, {
      ...bindingForm,
      probed_adapters: probedAdapters,
    })
    selected.value = await teacherAPI.model(modelForm.model_id, detailCredentialId())
    bindingOpen.value = false
    toast.success(`路由已保存，保留 ${probedAdapters.length} 条可用端点`)
    await load()
  } catch (e) {
    error.value = msg(e)
  } finally {
    bindingProbing.value = false
    bindingProbeProgress.value = ''
  }
}

async function toggleStatus() {
  try {
    const next = modelForm.status === 'active' ? 'inactive' : 'active'
    const body: { model_id: string; status: ModelStatus; credit_multiplier?: string } = {
      model_id: modelForm.model_id,
      status: next,
    }
    if (next === 'active') body.credit_multiplier = modelForm.credit_multiplier
    await teacherAPI.updateModel(body)
    toast.success(next === 'active' ? '模型已启用' : '模型已停用')
    await load()
    close()
  } catch (e) {
    error.value = msg(e)
  }
}

function msg(e: unknown) { return e instanceof APIError ? e.message : '请求失败' }

onMounted(load)
</script>

<template>
  <section class="page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">TEACHER</p>
        <h1>模型管理</h1>
      </div>
      <button class="button primary" @click="reset">新增模型</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <section class="panel">
      <div class="segmented">
        <button :class="{ active: tab === 'common' }" @click="tab = 'common'">常用模型</button>
        <button :class="{ active: tab === 'all' }" @click="tab = 'all'">全部模型</button>
      </div>
      <label>搜索<input v-model="query" placeholder="名称或 model_id"></label>
    </section>
    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="cards" label="正在加载模型" style="margin-top:20px">
      <section v-for="provider in schoolProviders" :key="provider.id" class="panel">
        <h2>{{ provider.name }}</h2>
        <BeichenSchoolModels :provider-id="provider.id" :query="query" :common-ids="tab === 'common' ? commonIds : undefined" />
      </section>
      <div v-if="filtered.length" class="grid two">
        <article v-for="model in filtered" :key="lineKey(model)" class="panel">
          <div class="row">
            <div><strong>{{ model.display_name }}</strong><p class="muted">{{ model.model_id }}</p></div>
            <StatusBadge :status="model.status" />
          </div>
          <div class="chip-list">
            <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
            <span v-if="model.credential_label" class="chip">{{ model.credential_label }}</span>
            <span class="chip">{{ model.credit_multiplier ?? '未配置' }}x</span>
            <span v-if="model.is_common" class="chip">常用</span>
            <span v-if="model.route_ready === false" class="chip warn">路由不可用</span>
          </div>
          <div class="actions"><button class="button ghost" @click="open(model)">配置</button></div>
        </article>
      </div>
    </LoadingRegion>

    <AppDialog :open="dialogOpen" :title="selected ? selected.model.display_name : '新增模型'" wide @update:open="$event ? null : close()">
      <div class="wizard-steps">
        <button v-for="item in [{ n: 1, t: '选择模型' }, { n: 2, t: '治理配置' }, { n: 3, t: '启用' }]" :key="item.n" class="wizard-step" :class="{ active: step === item.n, done: step > item.n }" :disabled="!canOpenStep(item.n)" @click="goToStep(item.n)"><span>{{ item.n }}</span>{{ item.t }}</button>
      </div>

      <section v-if="step === 1">
        <div class="grid two">
          <label>模型 ID<input v-model="modelForm.model_id" :readonly="Boolean(selected)" required @input="searchCatalog"></label>
          <label>显示名称<input v-model="modelForm.display_name" required></label>
        </div>
        <div v-if="!selected && catalog.length">
          <div v-for="item in catalog" :key="`${item.credential_id || 'catalog'}-${item.model_id}`" class="list-row catalog-row">
            <div>
              <strong>{{ catalogDisplayName(item) }}</strong>
              <p v-if="item.credential_label" class="muted">Key：{{ item.credential_label }}</p>
              <p class="muted mono">{{ item.model_id }}</p>
              <dl class="catalog-fields">
                <template v-for="field in catalogFieldEntries(item.raw_entry)" :key="field.key">
                  <dt>{{ field.key }}</dt>
                  <dd>{{ field.value }}</dd>
                </template>
              </dl>
            </div>
            <button class="button ghost" @click="chooseCandidate(item)">选择</button>
          </div>
          <div class="pagination">
            <button class="button ghost" :disabled="catalogPage <= 1" @click="catalogPage--; searchCatalog()">←</button>
            <span>{{ catalogPage }} / {{ maxCatalogPage }}</span>
            <button class="button ghost" :disabled="catalogPage >= maxCatalogPage" @click="catalogPage++; searchCatalog()">→</button>
          </div>
        </div>
        <div class="actions"><button class="button primary" :disabled="!modelForm.model_id || !modelForm.display_name" @click="goToStep(2)">下一步</button></div>
      </section>

      <section v-else-if="step === 2">
        <dl class="readonly-meta">
          <dt>模型 ID</dt><dd>{{ modelForm.model_id }}</dd>
          <dt>上游描述</dt><dd>{{ modelForm.description || '—' }}</dd>
        </dl>
        <label>显示名称<input v-model="modelForm.display_name" required></label>
        <label>计费倍率<input v-model="modelForm.credit_multiplier" inputmode="decimal" step="0.01" min="0" @blur="modelForm.credit_multiplier = normalizeMultiplier(modelForm.credit_multiplier)"></label>
        <label class="checkbox-option"><input v-model="modelForm.is_common" type="checkbox">全局常用模型</label>
        <section v-if="selected" class="route-panel">
          <div class="row">
            <h3>路由</h3>
            <button v-if="visibleBindings.length" class="button ghost" type="button" @click="openBinding()">编辑路由</button>
          </div>
          <article v-for="binding in visibleBindings" :key="binding.id" class="list-row route-row">
            <div>
              <strong>{{ providers.find(provider => provider.id === binding.provider_id)?.name || '供应商' }}</strong>
              <p v-if="binding.credential_label" class="muted">Key：{{ binding.credential_label }}</p>
              <p class="muted">{{ bindingAdapterMeta(binding.adapter).gatewayPath }}</p>
            </div>
            <StatusBadge :status="binding.status" />
          </article>
        </section>
        <div class="actions"><button class="button primary" @click="saveBase">保存并继续</button></div>
      </section>

      <section v-else>
        <section v-if="visibleBindings.length" class="route-panel">
          <h3>Gateway</h3>
          <article v-for="binding in visibleBindings" :key="binding.id" class="list-row route-row">
            <div>
              <strong>{{ providers.find(provider => provider.id === binding.provider_id)?.name || '供应商' }}</strong>
              <p v-if="binding.credential_label" class="muted">Key：{{ binding.credential_label }}</p>
              <p class="muted">{{ bindingAdapterMeta(binding.adapter).gatewayPath }}</p>
              <dl class="harness-config">
                <dt>Codex / Claude Code · base_url</dt><dd class="mono">{{ harnessConfig(binding).base_url }}</dd>
                <dt>model</dt><dd class="mono">{{ harnessConfig(binding).model }}</dd>
              </dl>
            </div>
            <StatusBadge :status="binding.status" />
          </article>
        </section>
        <div class="actions">
          <button class="button" :class="modelForm.status === 'active' ? 'danger' : 'primary'" :disabled="modelForm.status !== 'active' && !canEnableModel" @click="toggleStatus">{{ modelForm.status === 'active' ? '停用模型' : '启用模型' }}</button>
        </div>
      </section>
    </AppDialog>

    <AppDialog :open="bindingOpen" :title="editingBinding ? '编辑路由' : '新增路由'" wide @update:open="bindingOpen = $event">
      <form @submit.prevent="saveBinding">
        <label>供应商<select v-model="bindingForm.provider_id" required><option value="">请选择</option><option v-for="provider in activeProviders" :key="provider.id" :value="provider.id">{{ provider.name }}</option></select></label>
        <label>上游模型名<input v-model="bindingForm.upstream_model_name" required></label>
        <template v-if="!editingBinding">
          <p class="muted">保存路由时会逐步探测上游端点，仅创建探测成功的 binding。</p>
          <p v-if="bindingProbeProgress" class="probe-progress">{{ bindingProbeProgress }}</p>
          <ul v-if="bindingProbeSteps.length" class="probe-steps">
            <li v-for="step in bindingProbeSteps" :key="step.endpoint" :class="{ ok: step.ok, fail: !step.ok }">
              <strong>{{ bindingAdapterMeta(step.adapter).label }}</strong>
              <span>{{ step.ok ? '可用' : '不可用' }}</span>
              <span class="muted">{{ step.path }} · HTTP {{ step.http_status || '—' }}</span>
            </li>
          </ul>
        </template>
        <label v-if="editingBinding">端点类型<select v-model="bindingForm.adapter">
          <option v-for="item in adapterOptions" :key="item.value" :value="item.value">{{ item.label }} — {{ item.gatewayPath }}</option>
        </select></label>
        <label>优先级<input v-model.number="bindingForm.priority" type="number" min="0"></label>
        <div class="actions"><button class="button primary" :disabled="bindingProbing">{{ bindingProbing ? '探测中…' : '保存' }}</button></div>
      </form>
    </AppDialog>
  </section>
</template>

<style scoped>
.route-panel { margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--border, #e5e7eb); }
.route-panel h3 { margin: 0; font-size: 14px; }
.route-row { align-items: flex-start; }
.harness-config { display: grid; grid-template-columns: max-content 1fr; gap: 4px 12px; margin: 10px 0 0; font-size: 12px; }
.harness-config dt { color: var(--muted); }
.harness-config dd { margin: 0; word-break: break-all; }
.readonly-meta { margin: 0 0 16px; }
.readonly-meta dt { font-size: 12px; color: var(--muted); margin-top: 8px; }
.readonly-meta dd { margin: 4px 0 0; }
.catalog-fields { display: grid; grid-template-columns: max-content 1fr; gap: 4px 12px; margin: 8px 0 0; font-size: 13px; }
.catalog-fields dt { color: var(--muted); }
.catalog-fields dd { margin: 0; word-break: break-word; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.probe-progress { margin: 8px 0 0; color: var(--accent, #2563eb); }
.probe-steps { list-style: none; padding: 0; margin: 12px 0 0; display: grid; gap: 8px; }
.probe-steps li { display: grid; grid-template-columns: 1fr auto; gap: 4px 12px; padding: 8px 10px; border-radius: 8px; background: var(--surface-muted, #f8fafc); }
.probe-steps li.ok { border-left: 3px solid #16a34a; }
.probe-steps li.fail { border-left: 3px solid #dc2626; }
.probe-steps .muted { grid-column: 1 / -1; font-size: 12px; }
</style>
