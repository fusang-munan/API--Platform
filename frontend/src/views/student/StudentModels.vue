<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { studentAPI } from '../../api/student'
import type { Model, ModelCatalogCandidate, ModelCategory, ModelStatus } from '../../api/types'
import { capabilityLabel, formatTokenK } from '../../utils/modelCatalog'
import { sortModelsByRecency } from '../../utils/modelSort'
import { filterStudentModels } from '../../utils/studentModels'
import { catalogCreditsLabel, catalogProviderLabel, modelCreditsLabel, modelDescriptionText, modelProviderLabel } from '../../utils/modelPlazaDisplay'
import StatusBadge from '../../components/StatusBadge.vue'
import EmptyState from '../../components/EmptyState.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import { APIError } from '../../api/client'

const commonRows = ref<Model[]>([])
const libraryRows = ref<Model[]>([])
const catalogItems = ref<ModelCatalogCandidate[]>([])
const query = ref('')
const category = ref<ModelCategory | ''>('')
const status = ref<ModelStatus | ''>('')
const expanded = ref('')
const addOpen = ref(false)
const catalogQuery = ref('')
const catalogPage = ref(1)
const catalogTotal = ref(0)
const error = ref('')
const loadState = useLoadState()
const toast = useToast()

const libraryIDs = computed(() => new Set([
  ...commonRows.value.map(model => model.model_id.toLowerCase()),
  ...libraryRows.value.map(model => model.model_id.toLowerCase()),
]))
const filteredCommon = computed(() => sortModelsByRecency(filterStudentModels(commonRows.value, query.value, category.value, status.value)))
const filteredLibrary = computed(() => sortModelsByRecency(filterStudentModels(libraryRows.value, query.value, category.value, status.value)))
const hasAnyModels = computed(() => filteredCommon.value.length > 0 || filteredLibrary.value.length > 0)
const catalogPages = computed(() => Math.max(1, Math.ceil(catalogTotal.value / 20)))

watch(catalogQuery, () => { catalogPage.value = 1 })

async function load() {
  await loadState.run(async () => {
    try {
      const plaza = await studentAPI.models()
      commonRows.value = plaza.common_models
      libraryRows.value = plaza.library_models
      error.value = ''
    } catch (caught) {
      error.value = caught instanceof APIError ? caught.message : '加载模型失败'
    }
  })
}

async function openAdd() {
  addOpen.value = true
  catalogQuery.value = ''
  catalogPage.value = 1
  try {
    await searchCatalog()
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '加载模型目录失败'
  }
}

async function searchCatalog() {
  try {
    const page = await studentAPI.modelCatalog(catalogQuery.value, catalogPage.value, 20)
    catalogItems.value = page.items
    catalogTotal.value = page.total
    error.value = ''
  } catch (caught) {
    catalogItems.value = []
    catalogTotal.value = 0
    error.value = caught instanceof APIError ? caught.message : '搜索模型目录失败'
  }
}

async function addModel(modelID: string) {
  try {
    const model = await studentAPI.addModel(modelID)
    if (!libraryRows.value.some(item => item.model_id.toLowerCase() === model.model_id.toLowerCase())
      && !commonRows.value.some(item => item.model_id.toLowerCase() === model.model_id.toLowerCase())) {
      libraryRows.value = sortModelsByRecency([...libraryRows.value, model])
    }
    toast.success('已加入我的模型广场')
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '添加模型失败'
  }
}

async function removeModel(modelID: string) {
  if (!confirm(`从模型广场移除 ${modelID}？`)) return
  try {
    await studentAPI.removeModel(modelID)
    libraryRows.value = libraryRows.value.filter(model => model.model_id !== modelID)
    toast.success('已从模型广场移除')
  } catch (caught) {
    error.value = caught instanceof APIError ? caught.message : '移除模型失败'
  }
}

function refresh() { void load() }

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
        <h1>模型广场</h1>
      </div>
      <button class="button primary" @click="openAdd">检索添加</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <section class="panel">
      <div class="grid two">
        <label>搜索<input v-model="query" placeholder="名称、模型 ID 或能力"></label>
        <div class="row">
          <label>类别<select v-model="category"><option value="">全部类别</option><option v-for="value in ['text','image','audio','video','multimodal','embedding','rerank']" :key="value">{{ value }}</option></select></label>
          <label>状态<select v-model="status"><option value="">全部状态</option><option value="active">已启用</option><option value="inactive">已停用</option><option value="pending_configuration">待配置</option></select></label>
        </div>
      </div>
    </section>

    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="cards" label="正在加载模型" style="margin-top:20px">
      <div v-if="hasAnyModels" class="plaza-layout">
        <section class="plaza-column">
          <div class="plaza-heading">
            <h2>常用模型</h2>
          </div>
          <div v-if="filteredCommon.length" class="plaza-cards">
            <article v-for="model in filteredCommon" :key="model.model_id" class="panel is-interactive">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
                <StatusBadge :status="model.status" />
              </div>
              <div class="chip-list">
                <span class="chip preset">预置</span>
                <span class="chip">{{ modelCreditsLabel(model) }}</span>
                <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
                <span v-for="capability in model.capabilities" :key="capability" class="chip">{{ capabilityLabel(capability) }}</span>
              </div>
              <p v-if="modelDescriptionText(model)" class="muted">{{ modelDescriptionText(model) }}</p>
              <div class="actions">
                <button class="button ghost" @click="expanded = expanded === model.model_id ? '' : model.model_id">{{ expanded === model.model_id ? '收起' : '查看详情' }}</button>
              </div>
              <Transition name="content-fade">
                <dl v-if="expanded === model.model_id">
                  <dt>模型名</dt><dd>{{ model.model_id }}</dd>
                  <dt>中转厂商</dt><dd>{{ modelProviderLabel(model) }}</dd>
                  <dt>消耗点数</dt><dd>{{ modelCreditsLabel(model) }}</dd>
                  <dt>能力</dt><dd>{{ model.capabilities.map(capabilityLabel).join('、') || '—' }}</dd>
                  <dt>输入 / 输出</dt><dd>{{ model.input_modalities.join('、') || '—' }} / {{ model.output_modalities.join('、') || '—' }}</dd>
                  <dt>上下文 / 最大输入 / 最大输出</dt><dd>{{ formatTokenK(model.context_window) }} / {{ formatTokenK(model.max_input_tokens) }} / {{ formatTokenK(model.max_output_tokens) }}</dd>
                </dl>
              </Transition>
            </article>
          </div>
          <EmptyState v-else title="暂无常用模型" />
        </section>

        <section class="plaza-column">
          <div class="plaza-heading">
            <h2>我添加的模型</h2>
          </div>
          <div v-if="filteredLibrary.length" class="plaza-cards">
            <article v-for="model in filteredLibrary" :key="model.model_id" class="panel is-interactive">
              <div class="row">
                <div>
                  <strong>{{ model.display_name }}</strong>
                  <p class="muted">{{ model.model_id }} · {{ model.category }}</p>
                </div>
                <StatusBadge :status="model.status" />
              </div>
              <div class="chip-list">
                <span class="chip">{{ modelCreditsLabel(model) }}</span>
                <span v-if="model.provider_name" class="chip">{{ model.provider_name }}</span>
                <span v-for="capability in model.capabilities" :key="capability" class="chip">{{ capabilityLabel(capability) }}</span>
              </div>
              <p v-if="modelDescriptionText(model)" class="muted">{{ modelDescriptionText(model) }}</p>
              <div class="actions">
                <button class="button ghost" @click="expanded = expanded === model.model_id ? '' : model.model_id">{{ expanded === model.model_id ? '收起' : '查看详情' }}</button>
                <button class="button secondary" @click="removeModel(model.model_id)">移除</button>
              </div>
              <Transition name="content-fade">
                <dl v-if="expanded === model.model_id">
                  <dt>模型名</dt><dd>{{ model.model_id }}</dd>
                  <dt>中转厂商</dt><dd>{{ modelProviderLabel(model) }}</dd>
                  <dt>消耗点数</dt><dd>{{ modelCreditsLabel(model) }}</dd>
                  <dt>能力</dt><dd>{{ model.capabilities.map(capabilityLabel).join('、') || '—' }}</dd>
                  <dt>输入 / 输出</dt><dd>{{ model.input_modalities.join('、') || '—' }} / {{ model.output_modalities.join('、') || '—' }}</dd>
                  <dt>上下文 / 最大输入 / 最大输出</dt><dd>{{ formatTokenK(model.context_window) }} / {{ formatTokenK(model.max_input_tokens) }} / {{ formatTokenK(model.max_output_tokens) }}</dd>
                </dl>
              </Transition>
            </article>
          </div>
          <EmptyState v-else title="还没有自行添加的模型" />
        </section>
      </div>
      <EmptyState v-else title="还没有可用模型" />
    </LoadingRegion>

    <AppDialog :open="addOpen" title="检索添加模型" wide @update:open="addOpen = $event">
      <section>
        <input v-model="catalogQuery" placeholder="搜索模型名" @input="searchCatalog">
        <div class="model-table compact">
          <div class="model-table-head">
            <span>模型名</span>
            <span>中转厂商</span>
            <span>消耗点数</span>
            <span />
          </div>
          <div v-for="item in catalogItems" :key="item.model_id" class="model-table-row">
            <span class="model-name">{{ item.model_id }}</span>
            <span>{{ catalogProviderLabel(item) }}</span>
            <span>{{ catalogCreditsLabel(item) }}</span>
            <button class="button ghost" :disabled="libraryIDs.has(item.model_id.toLowerCase())" @click="addModel(item.model_id)">
              {{ libraryIDs.has(item.model_id.toLowerCase()) ? '已添加' : '添加' }}
            </button>
          </div>
        </div>
        <EmptyState v-if="!catalogItems.length" title="暂无匹配模型" />
        <div class="pagination">
          <button class="button ghost" :disabled="catalogPage <= 1" @click="catalogPage--; searchCatalog()">←</button>
          <span>{{ catalogPage }} / {{ catalogPages }}</span>
          <button class="button ghost" :disabled="catalogPage >= catalogPages" @click="catalogPage++; searchCatalog()">→</button>
        </div>
      </section>
    </AppDialog>
  </section>
</template>

<style scoped>
.plaza-layout { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; }
.plaza-heading { margin-bottom: 16px; }
.plaza-heading h2 { margin: 0; font-size: 18px; }
.plaza-cards { display: grid; gap: 16px; }
.chip.preset { background: #eef3ff; color: #3b5bdb; }
.model-table { display: grid; gap: 10px; }
.model-table.compact { margin-top: 12px; }
.model-table-head,
.model-table-row {
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) minmax(0, 0.8fr) auto;
  gap: 12px;
  align-items: center;
}
.model-table-head {
  padding: 0 14px;
  font-size: 12px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.model-table-row { padding: 8px 14px; }
.model-name { font-weight: 650; word-break: break-word; }
@media (max-width: 960px) {
  .plaza-layout { grid-template-columns: 1fr; }
  .model-table-head { display: none; }
  .model-table-row {
    grid-template-columns: 1fr;
    gap: 6px;
  }
}
</style>
