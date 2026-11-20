<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import type { Provider, ProviderCredential } from '../../api/types'
import { APIError } from '../../api/client'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import BeichenSchoolModels from './BeichenSchoolModels.vue'
import BeichenReconcile from './BeichenReconcile.vue'
import { isBeichenProvider } from '../../utils/beichenModels'

type KeyDraft = {
  id?: string
  label: string
  key: string
  status: 'active' | 'inactive'
  configured: boolean
}

const rows = ref<Provider[]>([])
const schoolProvider = ref<Provider>()
const reconcileProviderIds = ref<string[]>([])
const query = ref('')
const status = ref('')
const drawer = ref(false)
const editing = ref<Provider | null>(null)
const activeKeyIndex = ref(0)
const revealKey = ref(false)
const saving = ref(false)
const deleting = ref('')
const resetting = ref('')
const error = ref('')
const toast = useToast()
const loadState = useLoadState()
const form = reactive({ name: '', base_url: '', status: 'active' as 'active' | 'inactive' })
const keyRows = ref<KeyDraft[]>([])
const deletedCredentialIds = ref<string[]>([])

const filtered = computed(() => rows.value.filter(p =>
  (!query.value || `${p.name} ${p.base_url}`.toLowerCase().includes(query.value.toLowerCase()))
  && (!status.value || p.status === status.value),
))

const activeKey = computed(() => keyRows.value[activeKeyIndex.value] ?? null)

function activeKeyCount(provider: Provider) {
  return (provider.credentials ?? []).filter(item => item.status === 'active').length
}

function toggleReconciliation(providerId: string) {
  reconcileProviderIds.value = reconcileProviderIds.value.includes(providerId)
    ? reconcileProviderIds.value.filter(id => id !== providerId)
    : [...reconcileProviderIds.value, providerId]
}

function catalogSyncTime(provider: Provider) {
  const date = provider.last_catalog_sync?.synced_at ? new Date(provider.last_catalog_sync.synced_at) : null
  return date && Number.isFinite(date.getTime())
    ? new Intl.DateTimeFormat('zh-CN', {
      timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
    }).format(date)
    : '—'
}

function emptyKeyRow(): KeyDraft {
  return { label: '', key: '', status: 'active', configured: false }
}

function keyTabLabel(row: KeyDraft, index: number) {
  const label = row.label.trim()
  return label || `Key ${index + 1}`
}

function keyRowsFromProvider(credentials: ProviderCredential[] = []): KeyDraft[] {
  if (!credentials.length) return [emptyKeyRow()]
  return credentials.map(item => ({
    id: item.id,
    label: item.label,
    key: '',
    status: item.status,
    configured: item.credential_configured,
  }))
}

async function load() {
  await loadState.run(async () => {
    try {
      rows.value = await teacherAPI.providers()
    } catch (e) {
      error.value = msg(e)
    }
  })
}

function open(p?: Provider) {
  editing.value = p ?? null
  deletedCredentialIds.value = []
  Object.assign(form, { name: p?.name ?? '', base_url: p?.base_url ?? '', status: p?.status ?? 'active' })
  keyRows.value = p ? keyRowsFromProvider(p.credentials) : [emptyKeyRow()]
  activeKeyIndex.value = 0
  revealKey.value = false
  drawer.value = true
}

function selectKey(index: number) {
  activeKeyIndex.value = index
  revealKey.value = false
}

function addKeyRow() {
  keyRows.value.push(emptyKeyRow())
  activeKeyIndex.value = keyRows.value.length - 1
  revealKey.value = false
}

function removeActiveKey() {
  if (keyRows.value.length <= 1) {
    toast.error('至少保留一个 Key')
    return
  }
  const row = keyRows.value[activeKeyIndex.value]
  if (row?.id && !confirm(`删除 Key「${row.label.trim() || '未命名'}」？将移除其上游目录与路由。`)) return
  if (row?.id) deletedCredentialIds.value.push(row.id)
  keyRows.value.splice(activeKeyIndex.value, 1)
  activeKeyIndex.value = Math.min(activeKeyIndex.value, keyRows.value.length - 1)
  revealKey.value = false
}

function validateKeyRows() {
  const labels = new Set<string>()
  for (const row of keyRows.value) {
    if (!row.label.trim()) {
      toast.error('每个 Key 都需要名称')
      return false
    }
    const label = row.label.trim().toLowerCase()
    if (labels.has(label)) {
      toast.error('Key 名称不能重复')
      return false
    }
    labels.add(label)
    if (!row.id && !row.key.trim()) {
      toast.error('新 Key 必须填写密钥')
      return false
    }
  }
  if (keyRows.value.filter(row => row.status === 'active').length === 0) {
    toast.error('至少启用一个 Key')
    return false
  }
  return true
}

async function saveKeys(providerId: string) {
  for (const credentialId of deletedCredentialIds.value) {
    await teacherAPI.deleteProviderCredential(providerId, credentialId)
  }
  for (const row of keyRows.value) {
    const payload = {
      label: row.label.trim(),
      credential: row.key.trim() || undefined,
      status: row.status,
    }
    if (row.id) await teacherAPI.updateProviderCredential(providerId, row.id, payload)
    else await teacherAPI.createProviderCredential(providerId, { ...payload, credential: row.key.trim() })
  }
}

async function save() {
  if (isBeichenProvider(form.base_url)) return
  if (!validateKeyRows()) return
  saving.value = true
  try {
    if (editing.value) {
      await teacherAPI.updateProvider(editing.value.id, { name: form.name, base_url: form.base_url, status: form.status })
      await saveKeys(editing.value.id)
      deletedCredentialIds.value = []
      toast.success('供应商与 Key 已保存（仅刷新上游目录，不改动模型配置）')
    } else {
      const first = keyRows.value[0]
      const result = await teacherAPI.createProvider({
        name: form.name,
        base_url: form.base_url,
        credential: first.key.trim(),
        status: form.status,
      })
      const defaultCred = result.credentials?.[0]
      if (defaultCred) {
        await teacherAPI.updateProviderCredential(result.id, defaultCred.id, {
          label: first.label.trim() || defaultCred.label,
          status: first.status,
        })
      }
      for (const row of keyRows.value.slice(1)) {
        await teacherAPI.createProviderCredential(result.id, {
          label: row.label.trim(),
          credential: row.key.trim(),
          status: row.status,
        })
      }
      toast.success(result.last_catalog_sync
        ? `供应商已创建，已同步 ${result.last_catalog_sync.synced} 个上游模型`
        : '供应商已创建')
    }
    drawer.value = false
    await load()
  } catch (e) {
    error.value = msg(e)
  } finally {
    saving.value = false
  }
}

async function resetCatalog(p: Provider) {
  if (p.status !== 'active') {
    toast.error('请先启用供应商')
    return
  }
  if (activeKeyCount(p) === 0) {
    toast.error('请先添加并启用至少一个 Key')
    return
  }
  if (!confirm('重新爬取上游模型、按 Key 分别导入并重置倍率/常用标记（不会在日常改 Key 或停启服务时自动执行），是否继续？')) return
  resetting.value = p.id
  try {
    const result = await teacherAPI.resetProviderCatalog(p.id)
    toast.success(`已重置，同步 ${result.synced} 个上游模型（${result.provider_name}）`)
    await load()
  } catch (e) {
    error.value = msg(e)
  } finally {
    resetting.value = ''
  }
}

async function toggle(p: Provider) {
  if (p.status === 'active' && !confirm('停用供应商将影响其模型路由，是否继续？')) return
  try {
    await teacherAPI.updateProvider(p.id, { status: p.status === 'active' ? 'inactive' : 'active' })
    toast.success(p.status === 'active' ? '供应商已停用' : '供应商已启用')
    await load()
  } catch (e) {
    error.value = msg(e)
  }
}

async function remove(p: Provider) {
  if (!confirm(`删除「${p.name}」？将同时移除 Key、上游目录及该供应商下的模型路由。`)) return
  deleting.value = p.id
  try {
    await teacherAPI.deleteProvider(p.id)
    toast.success('供应商已删除')
    await load()
  } catch (e) {
    error.value = msg(e)
  } finally {
    deleting.value = ''
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
        <h1>供应商管理</h1>
        <p class="muted">可同时启用多个供应商；每个供应商按 Key 独立同步上游目录。</p>
      </div>
      <button class="button primary" @click="open()">添加供应商</button>
    </div>
    <p v-if="error" class="error banner">{{ error }}</p>
    <LoadingRegion :initial-loading="loadState.initialLoading.value" :refreshing="loadState.refreshing.value" variant="list" label="正在加载供应商">
      <section class="panel">
        <div class="grid two">
          <label>搜索<input v-model="query" placeholder="名称或 Base URL"></label>
          <label>状态<select v-model="status"><option value="">全部状态</option><option value="active">启用</option><option value="inactive">停用</option></select></label>
        </div>
        <article v-for="p in filtered" :key="p.id" class="list-row provider-row">
          <div class="provider-row-head">
            <div>
              <strong>{{ p.name }}</strong>
              <p v-if="!isBeichenProvider(p.base_url)" class="muted">{{ p.base_url }} · {{ activeKeyCount(p) }} 个启用 Key / {{ (p.credentials ?? []).length }} 个 Key</p>
              <div v-if="!isBeichenProvider(p.base_url) && p.credentials?.length" class="chip-list">
                <span v-for="key in p.credentials" :key="key.id" class="chip">
                  {{ key.label }} · {{ key.status === 'active' ? '启用' : '停用' }}
                </span>
              </div>
              <div v-if="isBeichenProvider(p.base_url)" class="chip-list provider-overview" aria-label="北辰供应商概览">
                <span class="chip">Key 数 {{ p.credentials ? p.credentials.length : '—' }}</span>
                <span class="chip">启用 Key {{ p.credentials ? activeKeyCount(p) : '—' }}</span>
                <span class="chip">最近目录同步 {{ catalogSyncTime(p) }}<template v-if="catalogSyncTime(p) !== '—'">（北京时间）</template></span>
                <span class="chip">报价覆盖 —</span>
              </div>
            </div>
            <div class="actions">
              <StatusBadge :status="p.status" />
              <button v-if="isBeichenProvider(p.base_url)" type="button" class="button secondary" :aria-expanded="reconcileProviderIds.includes(p.id)" :aria-controls="`beichen-reconcile-${p.id}`" @click="toggleReconciliation(p.id)">北辰对账</button>
              <button v-if="isBeichenProvider(p.base_url)" class="button secondary" @click="schoolProvider = p">查看模型</button>
              <template v-else>
                <button class="button secondary" :disabled="resetting === p.id" @click="resetCatalog(p)">{{ resetting === p.id ? '重置中…' : '配置重置' }}</button>
                <button class="button ghost" @click="open(p)">编辑</button>
                <button class="button danger" :disabled="deleting === p.id" @click="remove(p)">{{ deleting === p.id ? '删除中…' : '删除' }}</button>
                <button class="button" :class="p.status === 'active' ? 'danger' : 'secondary'" @click="toggle(p)">{{ p.status === 'active' ? '停用' : '启用' }}</button>
              </template>
            </div>
          </div>
          <div v-if="isBeichenProvider(p.base_url)" :id="`beichen-reconcile-${p.id}`" class="provider-reconciliation">
            <BeichenReconcile v-if="reconcileProviderIds.includes(p.id)" :provider-id="p.id" />
          </div>
        </article>
      </section>
    </LoadingRegion>

    <AppDialog :open="Boolean(schoolProvider)" title="北辰模型" description="查看学校首选倍率并管理模型启用状态。" wide @update:open="open => { if (!open) schoolProvider = undefined }">
      <BeichenSchoolModels v-if="schoolProvider" :key="schoolProvider.id" :provider-id="schoolProvider.id" provider-maintenance />
    </AppDialog>
    <AppDialog :open="drawer" :title="editing ? '编辑供应商' : '创建供应商'" description="维护供应商地址、渠道凭据与启用状态。" @update:open="drawer = $event">
      <form class="provider-form" @submit.prevent="save">
        <div class="grid two">
          <label>名称<input v-model="form.name" required></label>
          <label>状态<select v-model="form.status"><option value="active">启用</option><option value="inactive">停用</option></select></label>
        </div>
        <label>Base URL<input v-model="form.base_url" type="url" required></label>

        <p v-if="isBeichenProvider(form.base_url)" class="muted">北辰由学校后台统一管理。</p>
        <section v-else class="key-panel">
          <div class="key-panel-head">
            <strong>Key</strong>
            <button v-if="keyRows.length > 1" type="button" class="button ghost danger-text" @click="removeActiveKey">删除当前 Key</button>
          </div>

          <div class="key-tabs" role="tablist" aria-label="Key 切换">
            <button
              v-for="(row, index) in keyRows"
              :key="row.id ?? `new-${index}`"
              type="button"
              class="key-tab"
              :class="{ active: activeKeyIndex === index, inactive: row.status === 'inactive' }"
              role="tab"
              :aria-selected="activeKeyIndex === index"
              @click="selectKey(index)"
            >
              {{ keyTabLabel(row, index) }}
            </button>
            <button type="button" class="key-tab add" aria-label="添加 Key" @click="addKeyRow">+</button>
          </div>

          <div v-if="activeKey" class="key-editor">
            <label>Key 名称<input v-model="activeKey.label" required placeholder="例如 codex、claude"></label>
            <label>
              {{ activeKey.id && activeKey.configured ? '替换 Key（留空则不改）' : 'Key' }}
              <input
                v-model="activeKey.key"
                :type="revealKey ? 'text' : 'password'"
                :required="!activeKey.id"
                :placeholder="activeKey.id && activeKey.configured ? '已配置，留空保持不变' : 'sk-...'"
                autocomplete="off"
              >
            </label>
            <div class="key-editor-row">
              <label class="grow">状态<select v-model="activeKey.status"><option value="active">启用</option><option value="inactive">停用</option></select></label>
              <button type="button" class="button ghost" @pointerdown="revealKey = true" @pointerup="revealKey = false" @pointerleave="revealKey = false">按住显示</button>
            </div>
          </div>
        </section>

        <div class="actions"><button class="button primary" :disabled="saving || isBeichenProvider(form.base_url)">{{ saving ? '保存中…' : '保存' }}</button></div>
      </form>
    </AppDialog>
  </section>
</template>

<style scoped>
.provider-row { display: block; min-width: 0; }
.provider-row-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; min-width: 0; }
.provider-row-head > div:first-child { min-width: 0; overflow-wrap: anywhere; }
.provider-row-head > .actions { flex-shrink: 0; flex-wrap: wrap; }
.provider-overview { margin-top: 10px; gap: 6px; }
.provider-overview .chip { white-space: normal; overflow-wrap: anywhere; }
.provider-reconciliation { width: 100%; min-width: 0; }
.provider-reconciliation:has(> *) { margin-top: 16px; border-top: 1px solid var(--line); }
@media (max-width: 900px) {
  .provider-row-head { flex-direction: column; }
  .provider-row-head > .actions { width: 100%; }
}
.provider-form { display: grid; gap: 1rem; }
.key-panel {
  display: grid;
  gap: 0.875rem;
  padding: 1rem;
  border: 1px solid var(--border-subtle, #dbe1ea);
  border-radius: 14px;
  background: var(--surface-muted, #f8fafc);
}
.key-panel-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.75rem;
}
.key-panel-head p { margin: 0.25rem 0 0; font-size: 0.875rem; }
.key-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.key-tab {
  border: 1px solid var(--border-subtle, #dbe1ea);
  background: #fff;
  color: var(--text-muted, #64748b);
  border-radius: 999px;
  padding: 0.35rem 0.85rem;
  font: inherit;
  cursor: pointer;
  transition: border-color 0.15s ease, color 0.15s ease, background 0.15s ease;
}
.key-tab.active {
  border-color: var(--accent, #7248f4);
  color: var(--accent, #7248f4);
  background: color-mix(in srgb, var(--accent, #7248f4) 8%, #fff);
}
.key-tab.inactive:not(.active) { opacity: 0.72; }
.key-tab.add {
  min-width: 2rem;
  padding-inline: 0.65rem;
  font-weight: 600;
}
.key-editor {
  display: grid;
  gap: 0.875rem;
  padding-top: 0.25rem;
}
.key-editor-row {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
}
.grow { flex: 1; }
.danger-text { color: #b42318; }
</style>
