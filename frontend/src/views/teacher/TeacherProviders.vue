<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { beichenSchoolChannels, teacherAPI } from '../../api/teacher'
import type { BeichenSchoolChannel } from '../../api/teacher'
import type { BeichenCatalogDifference, BeichenCredentialSummary, BeichenModelAlias, BeichenModelAliasUpdate, BeichenRebindPlan, BindingAdapter, Provider, ProviderCredential } from '../../api/types'
import { APIError, get, post } from '../../api/client'
import StatusBadge from '../../components/StatusBadge.vue'
import AppDialog from '../../components/AppDialog.vue'
import LoadingRegion from '../../components/LoadingRegion.vue'
import { useLoadState } from '../../composables/useLoadState'
import { useToast } from '../../composables/useToast'
import BeichenSchoolModels from './BeichenSchoolModels.vue'
import BeichenReconcile from './BeichenReconcile.vue'
import BeichenLoginPanel from './BeichenLoginPanel.vue'
import BeichenWebsiteKeys from './BeichenWebsiteKeys.vue'
import { isBeichenProvider } from '../../utils/beichenModels'
import { bindingAdapterMeta } from '../../utils/bindingAdapters'
import { schoolDay } from '../../utils/format'

type KeyDraft = {
  id?: string
  label: string
  key: string
  status: 'active' | 'inactive'
  configured: boolean
}

type ChannelSyncState = 'inactive' | 'pending' | 'syncing' | 'ready' | 'failed'
type ChannelKey = {
  credential_id: string
  label: string
  status: ProviderCredential['status']
  sync: Record<'catalog' | 'prices' | 'billing' | 'usage', ChannelSyncState>
  identity?: ChannelSyncState
  school_channel?: string
}
type ChannelSwitch = {
  credentials: ChannelKey[]
  counts: { enabled_keys: number; enabled_models: number; maintenance_models: number; stale_routes: number }
  models: Array<{
    model: string
    ready_count: number
    losing_route_count: number
    loses_last_route: boolean
    routes: Array<{ adapter: BindingAdapter; upstream_model: string; credential_label: string | null; credential_id: string | null; ready: boolean; stale: boolean }>
  }>
  preview: (BeichenRebindPlan & {
    blockers: Array<{ school_model: string; adapter: BindingAdapter; code: string; message: string }>
  }) | null
}
type HealthAction = 'sync' | 'probe' | 'replace'
type ChannelCreation = 'independent' | 'replacement'

const rows = ref<Provider[]>([])
const schoolProvider = ref<Provider>()
const aliasProvider = ref<Provider>()
const aliases = ref<BeichenModelAlias[]>([])
const aliasDraft = reactive<BeichenModelAlias>({ credential_id: null, upstream_model: '', school_model: '' })
const editingAlias = ref(false)
const readingAliases = ref(false)
const savingAlias = ref(false)
const aliasError = ref('')
let aliasRequest = 0
const reconcileProviderIds = ref<string[]>([])
const anomalies = ref<Record<string, { pendingCalls?: number; missingModels?: number; differences?: BeichenCatalogDifference[]; health?: BeichenCredentialSummary[] }>>({})
const channelProvider = ref<Provider>()
const channel = ref<ChannelSwitch>()
const channelCounts = ref<Record<string, ChannelSwitch['counts']>>({})
const channelFrom = ref('')
const channelTo = ref('')
const channelError = ref('')
const channelNotice = ref('')
const channelBusy = ref(false)
const channelReading = ref(false)
const channelCreation = ref<ChannelCreation>()
const newChannel = reactive({ label: '', key: '', schoolChannel: '' as BeichenSchoolChannel | '' })
let channelReadVersion = 0
let channelPoll: number | undefined
const healthDimensions = [['credential', '凭据'], ['catalog', '目录'], ['pricing', '报价'], ['probe', '探活']] as const
const healthActions: Record<typeof healthDimensions[number][0], HealthAction> = {
  credential: 'replace', catalog: 'sync', pricing: 'sync', probe: 'probe',
}
const healthActionLabels: Record<HealthAction, string> = { replace: '更换 Key', sync: '重新同步', probe: '重新探活' }
const syncDimensions = [['catalog', '目录'], ['prices', '报价'], ['billing', '倍率'], ['usage', '用量']] as const
const syncLabels: Record<ChannelSyncState, string> = {
  inactive: '已停用', pending: '等待同步', syncing: '同步中', ready: '已同步', failed: '同步失败',
}
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
let loadVersion = 0

const filtered = computed(() => rows.value.filter(p =>
  (!query.value || `${p.name} ${p.base_url}`.toLowerCase().includes(query.value.toLowerCase()))
  && (!status.value || p.status === status.value),
))

const activeKey = computed(() => keyRows.value[activeKeyIndex.value] ?? null)
const originalChannel = computed(() => channel.value?.credentials.find(key => key.credential_id === channelFrom.value))
const replacementChannel = computed(() => channel.value?.credentials.find(key => key.credential_id === channelTo.value))
const retirementImpact = computed(() => channel.value?.models.filter(model => model.losing_route_count > 0) ?? [])
const channelHealth = computed(() => (anomalies.value[channelProvider.value?.id ?? '']?.health ?? [])
  .filter(key => key.status === 'active' && key.health))
const healthyKeys = computed(() => channelHealth.value.filter(key => key.health?.available_dimensions === healthDimensions.length).length)

function activeKeyCount(provider: Provider) {
  return (provider.credentials ?? []).filter(item => item.status === 'active').length
}

function toggleReconciliation(providerId: string) {
  reconcileProviderIds.value = reconcileProviderIds.value.includes(providerId)
    ? reconcileProviderIds.value.filter(id => id !== providerId)
    : [...reconcileProviderIds.value, providerId]
}

async function jumpToReconciliation(providerId: string) {
  if (!reconcileProviderIds.value.includes(providerId)) toggleReconciliation(providerId)
  await nextTick()
  document.getElementById(`beichen-reconcile-${providerId}`)?.focus()
}

function catalogSyncTime(provider: Provider) {
  return beijingTime(provider.last_catalog_sync?.synced_at)
}

function beijingTime(value?: string | null) {
  const date = value ? new Date(value) : null
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

async function loadAnomalies(providerId: string, day: string, version: number) {
  try {
    const overview = await teacherAPI.beichenOverview(providerId, day)
    if (version !== loadVersion) return
    const counts = overview.comparison?.flatMap(row => row.pending_review_calls == null ? [] : [row.pending_review_calls]) ?? []
    const catalog = overview.model_catalog ?? []
    const listed = catalog.filter(model => !model.retired && model.credential_ids?.length !== 0)
    const missing = new Set(listed.filter(model => model.price_covered === false || (model.price_covered == null &&
      (model.price_state === 'missing' || model.blockers?.some(blocker => blocker.code === 'missing_price'))))
      .map(model => model.model.trim().toLowerCase()))
    const mixedModels = new Set(listed.filter(model => model.price_covered == null && model.price_state === 'mixed')
      .map(model => model.model.trim().toLowerCase()))
    for (const row of overview.models ?? []) {
      const name = row.model.trim().toLowerCase()
      if (mixedModels.has(name) && row.mode === 'follow' && row.price_status != null &&
        row.price_status !== 'confirmed' && row.price_status !== 'conditional') missing.add(name)
    }
    anomalies.value[providerId] = {
      pendingCalls: counts.length ? counts.reduce((sum, count) => sum + count, 0) : undefined,
      missingModels: missing.size || (listed.length && listed.every(model => model.price_state != null) ? 0 : undefined),
      differences: overview.catalog_differences ?? undefined,
      health: overview.credentials ?? undefined,
    }
  } catch {
    // 异常摘要可独立缺省，供应商操作和对账入口仍可用。
  }
}

function channelPath(providerID: string) {
  return `/api/v1/teacher/providers/${encodeURIComponent(providerID)}/beichen`
}

function closeChannelSwitch() {
  window.clearTimeout(channelPoll)
  channelReadVersion++
  channelProvider.value = undefined
  channel.value = undefined
  channelFrom.value = ''
  channelTo.value = ''
  channelReading.value = false
  showChannelCreation()
  channelError.value = ''
  channelNotice.value = ''
}

function openChannelSwitch(provider: Provider) {
  closeChannelSwitch()
  channelProvider.value = provider
  void readChannelSwitch(true)
}

async function readChannelSwitch(chooseChannels = false): Promise<ChannelSwitch | undefined> {
  const provider = channelProvider.value
  if (!provider) return
  window.clearTimeout(channelPoll)
  const version = ++channelReadVersion
  const query = new URLSearchParams()
  if (channelFrom.value) query.set('from_credential_id', channelFrom.value)
  if (channelTo.value) query.set('to_credential_id', channelTo.value)
  channelReading.value = true
  try {
    const view = await get<ChannelSwitch>(`${channelPath(provider.id)}/channel-switch?${query}`)
    if (version !== channelReadVersion) return
    const newPreview = view.preview && view.preview.preview_token !== channel.value?.preview?.preview_token
    channel.value = view
    channelCounts.value[provider.id] = view.counts
    if (newPreview) void loadAnomalies(provider.id, schoolDay(), loadVersion)
    if (chooseChannels) {
      const staleKeys = new Set(view.models.flatMap(model => model.routes.flatMap(route =>
        route.stale && route.credential_id ? [route.credential_id] : [])))
      const active = view.credentials.filter(key => key.status === 'active')
      if (staleKeys.size === 1) {
        channelFrom.value = Array.from(staleKeys)[0]
        const original = view.credentials.find(key => key.credential_id === channelFrom.value)
        const candidates = active.filter(key => key.credential_id !== channelFrom.value)
        const matches = candidates.filter(key => original?.label.startsWith(key.label + ' (archived ') ||
          (original?.school_channel && original.school_channel === key.school_channel))
        channelTo.value = matches.length === 1 ? matches[0].credential_id : candidates.length === 1 ? candidates[0].credential_id : ''
      } else if (active.length === 1 && staleKeys.size === 0) {
        channelFrom.value = active[0].credential_id
      }
      if (channelFrom.value) return await readChannelSwitch()
    }
    const sync = replacementChannel.value?.sync
    const identityPending = replacementChannel.value?.identity === 'pending' &&
      view.preview?.blockers.some(blocker => blocker.code === 'unverified_identity')
    if (identityPending || (sync && !view.preview && [sync.catalog, sync.billing, sync.usage].some(state => state === 'pending' || state === 'syncing'))) {
      channelPoll = window.setTimeout(() => { void readChannelSwitch() }, 5000)
    }
    return view
  } catch (e) {
    if (version === channelReadVersion) channelError.value = msg(e)
  } finally {
    if (version === channelReadVersion) channelReading.value = false
  }
}

function selectChannel(side: 'from' | 'to') {
  if (channelFrom.value === channelTo.value) {
    if (side === 'from') channelTo.value = ''
    else channelFrom.value = ''
  }
  channelNotice.value = ''
  channelError.value = ''
  if (channel.value) channel.value.preview = null
  void readChannelSwitch()
}

async function runChannelAction(action: (providerID: string) => Promise<void>) {
  const provider = channelProvider.value
  if (!provider || channelBusy.value || channelReading.value) return
  window.clearTimeout(channelPoll)
  channelBusy.value = true
  channelError.value = ''
  channelNotice.value = ''
  try {
    await action(provider.id)
  } catch (e) {
    channelError.value = msg(e)
  } finally {
    await readChannelSwitch()
    void loadAnomalies(provider.id, schoolDay(), loadVersion)
    channelBusy.value = false
  }
}

function showChannelCreation(mode?: ChannelCreation) {
  channelCreation.value = mode
  newChannel.label = ''
  newChannel.key = ''
  newChannel.schoolChannel = ''
}

async function addChannel() {
  const mode = channelCreation.value
  const label = newChannel.label.trim()
  const key = newChannel.key.trim()
  const schoolChannel = newChannel.schoolChannel
  const predecessor = channelFrom.value
  if (!label || !key || !mode || (mode === 'independent' && !schoolChannel) || (mode === 'replacement' && !predecessor)) return
  await runChannelAction(async providerID => {
    const input = { label, credential: key, status: 'active' as const }
    const saved = schoolChannel && mode === 'independent'
      ? await teacherAPI.createProviderCredential(providerID, { ...input, channel: schoolChannel })
      : await teacherAPI.createProviderCredential(providerID, { ...input, replaces_credential_id: predecessor })
    updateChannelRow(saved)
    if (mode === 'independent') channelFrom.value = ''
    channelTo.value = saved.id
    showChannelCreation()
    channelNotice.value = mode === 'independent'
      ? '学校渠道已登记，后台将同步目录、报价、倍率和用量。'
      : '接替 Key 已保存，后台将同步目录、报价、倍率和用量。'
  })
}

function updateChannelRow(credential: ProviderCredential) {
  const provider = rows.value.find(row => row.id === credential.provider_id)
  if (provider) provider.credentials = [...(provider.credentials ?? []).filter(key => key.id !== credential.id), credential]
}

async function syncChannel(key = channelTo.value) {
  await runChannelAction(async providerID => {
    await post<{ synced: boolean }>(`${channelPath(providerID)}/sync`, key ? { credential_id: key } : undefined)
    channelNotice.value = '同步完成。可查看最新报价、协议与重绑预览。'
  })
}

async function previewChannel() {
  channelError.value = ''
  await readChannelSwitch()
}

async function confirmChannel() {
  const preview = channel.value?.preview
  if (!preview?.changes.length) return
  await runChannelAction(async providerID => {
    const result = await post<BeichenRebindPlan>(`${channelPath(providerID)}/channel-switch`, {
      from_credential_id: channelFrom.value, to_credential_id: channelTo.value, preview_token: preview.preview_token,
    })
    channelNotice.value = `已更新 ${result.changes.length} 条线路。`
  })
}

async function setOriginalChannelStatus() {
  const key = originalChannel.value
  if (!key) return
  await runChannelAction(async providerID => {
    const disabling = key.status === 'active'
    if (disabling) {
      const current = await readChannelSwitch()
      if (!current) return
      const lost = current.models.filter(model => model.loses_last_route).map(model => model.model)
      const partlyLost = current.models.filter(model => model.losing_route_count > 0 && !model.loses_last_route).map(model => model.model)
      const warning = lost.length ? `停用后将失去最后一条可用线路并进入维护的模型\n${lost.join('、')}\n\n` : ''
      const partial = partlyLost.length ? `停用后将少一条或多条北辰线路的模型\n${partlyLost.join('、')}\n\n` : ''
      if (!window.confirm(`${warning}${partial}停用「${key.label}」？`)) return
    }
    const updated = await teacherAPI.updateProviderCredential(providerID, key.credential_id, { status: disabling ? 'inactive' : 'active' })
    updateChannelRow(updated)
    channelNotice.value = disabling ? '原 Key 已停用。' : '原 Key 已启用，等待同步。'
  })
}

async function probeChannels() {
  await runChannelAction(async providerID => {
    await teacherAPI.requestBeichenProbes(providerID)
    channelNotice.value = '探活请求已提交，可稍后刷新查看健康状态。'
  })
}

function runHealthAction(key: string, action: HealthAction) {
  if (action === 'probe') return probeChannels()
  if (action === 'sync') return syncChannel(key)
  channelFrom.value = key
  showChannelCreation('replacement')
  selectChannel('from')
}

function openChannelModels() {
  const provider = channelProvider.value
  closeChannelSwitch()
  schoolProvider.value = provider
}

function refreshSchoolCatalog() {
  if (schoolProvider.value) void loadAnomalies(schoolProvider.value.id, schoolDay(), loadVersion)
}

function resetAliasDraft() {
  aliasDraft.credential_id = aliasProvider.value?.credentials?.find(key => key.status === 'active')?.id ?? null
  aliasDraft.upstream_model = ''
  aliasDraft.school_model = ''
  editingAlias.value = false
}

async function loadAliases(providerId: string) {
  const request = ++aliasRequest
  readingAliases.value = true
  aliasError.value = ''
  try {
    const result = await teacherAPI.beichenAliases(providerId)
    if (request === aliasRequest) aliases.value = result
  } catch (e) {
    if (request === aliasRequest) aliasError.value = msg(e)
  } finally {
    if (request === aliasRequest) readingAliases.value = false
  }
}

function openAliases(provider: Provider) {
  aliasProvider.value = provider
  aliases.value = []
  resetAliasDraft()
  void loadAliases(provider.id)
}

function closeAliases() {
  if (savingAlias.value) return
  aliasProvider.value = undefined
  aliasRequest++
}

function editAlias(row: BeichenModelAlias) {
  Object.assign(aliasDraft, row)
  editingAlias.value = true
}

function aliasChannel(id: string | null) {
  if (id == null) return '所有渠道的默认别名'
  return aliasProvider.value?.credentials?.find(key => key.id === id)?.label ?? id
}

async function writeAlias(body: BeichenModelAliasUpdate) {
  const provider = aliasProvider.value
  if (!provider || savingAlias.value || readingAliases.value) return
  savingAlias.value = true
  aliasError.value = ''
  try {
    await teacherAPI.putBeichenAlias(provider.id, body)
    resetAliasDraft()
    toast.success(body.school_model == null ? '已移除模型别名' : '已保存模型别名')
    await loadAliases(provider.id)
  } catch (e) {
    aliasError.value = msg(e)
  } finally {
    savingAlias.value = false
  }
}

function saveAlias() {
  const upstream = aliasDraft.upstream_model.trim()
  const school = aliasDraft.school_model.trim()
  if (upstream && school) void writeAlias({ ...aliasDraft, upstream_model: upstream, school_model: school })
}

function removeAlias(row: BeichenModelAlias) {
  if (window.confirm(`移除「${aliasChannel(row.credential_id)}」的 ${row.upstream_model} 别名？`)) {
    void writeAlias({ ...row, school_model: null })
  }
}

async function load() {
  const version = ++loadVersion
  await loadState.run(async () => {
    try {
      const providers = await teacherAPI.providers()
      if (version !== loadVersion) return
      rows.value = providers
      anomalies.value = {}
      const day = schoolDay()
      for (const provider of providers) {
        if (isBeichenProvider(provider.base_url)) void loadAnomalies(provider.id, day, version)
      }
    } catch (e) {
      if (version === loadVersion) error.value = msg(e)
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
onBeforeUnmount(() => { loadVersion++; closeChannelSwitch() })
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
                <span v-if="anomalies[p.id]?.pendingCalls != null" class="chip">今日待核 {{ anomalies[p.id].pendingCalls }} 笔</span>
                <span v-if="anomalies[p.id]?.missingModels != null" class="chip">缺价模型 {{ anomalies[p.id].missingModels }} 个</span>
                <span v-if="channelCounts[p.id]?.maintenance_models" class="chip">维护中 {{ channelCounts[p.id].maintenance_models }} 个模型</span>
                <a :href="`#beichen-reconcile-${p.id}`" class="provider-compare" @click.prevent="jumpToReconciliation(p.id)">对账对比</a>
              </div>
            </div>
            <div class="actions">
              <StatusBadge :status="p.status" />
              <button v-if="isBeichenProvider(p.base_url)" type="button" class="button secondary" :aria-expanded="reconcileProviderIds.includes(p.id)" :aria-controls="`beichen-reconcile-${p.id}`" @click="toggleReconciliation(p.id)">北辰对账</button>
              <button v-if="isBeichenProvider(p.base_url)" class="button secondary" @click="schoolProvider = p">查看模型</button>
              <button v-if="isBeichenProvider(p.base_url)" type="button" class="button secondary" @click="openChannelSwitch(p)">渠道切换向导</button>
              <button v-if="isBeichenProvider(p.base_url)" type="button" class="button secondary" @click="openAliases(p)">模型别名</button>
              <BeichenLoginPanel v-if="isBeichenProvider(p.base_url)" :provider="p" />
              <template v-else>
                <button class="button secondary" :disabled="resetting === p.id" @click="resetCatalog(p)">{{ resetting === p.id ? '重置中…' : '配置重置' }}</button>
                <button class="button ghost" @click="open(p)">编辑</button>
                <button class="button danger" :disabled="deleting === p.id" @click="remove(p)">{{ deleting === p.id ? '删除中…' : '删除' }}</button>
                <button class="button" :class="p.status === 'active' ? 'danger' : 'secondary'" @click="toggle(p)">{{ p.status === 'active' ? '停用' : '启用' }}</button>
              </template>
            </div>
          </div>
          <BeichenWebsiteKeys v-if="isBeichenProvider(p.base_url)" :provider="p" />
          <details v-if="isBeichenProvider(p.base_url) && anomalies[p.id]?.differences" class="catalog-differences">
            <summary>目录差异</summary>
            <p v-if="!anomalies[p.id].differences?.length" class="muted">当前没有启用的渠道 Key。</p>
            <section v-for="channel in anomalies[p.id].differences" :key="channel.credential_id">
              <h3>{{ channel.label }}<template v-if="channel.group_id != null"> · 分组 {{ channel.group_id }}</template></h3>
              <p v-if="channel.state === 'unverified'" class="muted">待核对。请检查官网授权和分组，再同步目录。</p>
              <template v-else>
                <p v-if="channel.state === 'stale'" class="muted">官网目录待更新，以下为保留的分组清单。</p>
                <p class="muted">核对时间 {{ beijingTime(channel.checked_at) }}（北京时间）</p>
                <p v-if="!channel.website_only.length && !channel.key_only.length">网页和 Key 目录一致。</p>
                <div v-else class="catalog-difference-groups">
                  <div>
                    <h4>网页有 Key 无（{{ channel.website_only.length }}）</h4>
                    <p v-if="channel.website_only.length" class="muted">确认这把 Key 是否归属该分组，或更换为能访问这些模型的 Key。</p>
                    <ul><li v-for="model in channel.website_only" :key="model">{{ model }}</li></ul>
                  </div>
                  <div>
                    <h4>Key 有 网页无（{{ channel.key_only.length }}）</h4>
                    <p v-if="channel.key_only.length" class="muted">核对分组映射，并在官网补齐分组或模型配置后同步。</p>
                    <ul><li v-for="model in channel.key_only" :key="model">{{ model }}</li></ul>
                  </div>
                </div>
              </template>
            </section>
          </details>
          <div v-if="isBeichenProvider(p.base_url)" :id="`beichen-reconcile-${p.id}`" class="provider-reconciliation"
            role="region" aria-label="北辰对账" tabindex="-1">
            <BeichenReconcile v-if="reconcileProviderIds.includes(p.id)" :provider-id="p.id" />
          </div>
        </article>
      </section>
    </LoadingRegion>

    <AppDialog :open="Boolean(channelProvider)" title="渠道切换向导" description="登记学校渠道，或为已有线路准备接替 Key。" wide
      @update:open="open => { if (!open && !channelBusy) closeChannelSwitch() }">
      <section v-if="channelProvider" class="switch-panel" :aria-busy="channelBusy || channelReading">
        <p v-if="channelError" class="error" role="alert">{{ channelError }}</p>
        <p v-if="channelNotice" role="status">{{ channelNotice }}</p>
        <p v-if="channelReading && !channel" role="status">正在读取渠道状态…</p>
        <div v-if="channel" class="chip-list" aria-label="渠道状态统计">
          <span class="chip">启用 Key {{ channel.counts.enabled_keys }} 把</span>
          <span class="chip">已启用模型 {{ channel.counts.enabled_models }} 个</span>
          <span class="chip">维护中 {{ channel.counts.maintenance_models }} 个</span>
          <span class="chip">失效绑定 {{ channel.counts.stale_routes }} 条</span>
        </div>
        <div class="grid two">
          <label>原渠道<select v-model="channelFrom" :disabled="channelBusy || !channel" @change="selectChannel('from')">
            <option value="">请选择原 Key</option>
            <option v-for="key in channel?.credentials ?? []" :key="key.credential_id" :value="key.credential_id">
              {{ key.label }}{{ key.status === 'inactive' ? '（已停用）' : '' }}
            </option>
          </select></label>
          <label>接替渠道<select v-model="channelTo" :disabled="channelBusy || !channel" @change="selectChannel('to')">
            <option value="">请选择接替 Key</option>
            <option v-for="key in (channel?.credentials ?? []).filter(item => item.status === 'active' && item.credential_id !== channelFrom)"
              :key="key.credential_id" :value="key.credential_id">{{ key.label }}</option>
          </select></label>
        </div>
        <div class="actions">
          <button type="button" class="button secondary" :disabled="channelBusy || channelReading || !channel"
            @click="showChannelCreation(channelCreation === 'independent' ? undefined : 'independent')">
            {{ channelCreation === 'independent' ? '收起独立渠道表单' : '新建独立学校渠道' }}
          </button>
          <button type="button" class="button secondary" :disabled="channelBusy || channelReading || !channel || !channelFrom"
            @click="showChannelCreation(channelCreation === 'replacement' ? undefined : 'replacement')">
            {{ channelCreation === 'replacement' ? '收起接替 Key 表单' : '添加接替 Key' }}
          </button>
          <button type="button" class="button secondary" :disabled="channelBusy || channelReading || !channelTo" @click="syncChannel()">一键同步</button>
          <button type="button" class="button secondary" :disabled="channelBusy || channelReading || !channelFrom || !channelTo" @click="previewChannel">预览重绑</button>
        </div>
        <p v-if="channel?.credentials.length && !channelFrom && channelCreation !== 'independent'" class="muted">接替已有线路时，请先选择原 Key。</p>
        <form v-if="channelCreation" class="key-panel" :aria-label="channelCreation === 'independent' ? '新建独立学校渠道' : '添加接替 Key'" @submit.prevent="addChannel">
          <template v-if="channelCreation === 'independent'">
            <label>学校渠道<select v-model="newChannel.schoolChannel" required :disabled="channelBusy">
              <option value="">请选择学校渠道</option>
              <option v-for="name in beichenSchoolChannels" :key="name" :value="name">{{ name }}</option>
            </select></label>
            <p class="muted">保存后登记为学校渠道。通过该渠道发出的调用按学校系数 1.10 计算积分。</p>
          </template>
          <label>新渠道名称<input v-model="newChannel.label" maxlength="128" required :disabled="channelBusy"></label>
          <label>新 Key<input v-model="newChannel.key" type="password" autocomplete="new-password" required :disabled="channelBusy"></label>
          <div class="actions"><button type="submit" class="button primary"
            :disabled="channelBusy || channelReading || (channelCreation === 'replacement' && !channelFrom)">保存并同步</button></div>
        </form>
        <div v-if="replacementChannel" class="chip-list" :aria-label="channelFrom ? '接替渠道同步进度' : '新渠道同步进度'">
          <span v-for="[kind, title] in syncDimensions" :key="kind" class="chip">{{ title }} · {{ syncLabels[replacementChannel.sync[kind]] }}</span>
          <span v-if="replacementChannel.identity" class="chip">官网账单归属 · {{ syncLabels[replacementChannel.identity] }}</span>
        </div>
        <p v-if="channelFrom && channelTo && channel && !channel.preview && !channelError" class="muted">
          等待接替渠道完成同步。目录可用后，这里会自动显示重绑预览。同步失败时可点「一键同步」重试。
        </p>
        <section v-if="channel?.preview" class="switch-section" aria-label="重绑预览">
          <h3>重绑预览</h3>
          <ul v-if="channel.preview.changes.length" class="switch-list">
            <li v-for="change in channel.preview.changes" :key="change.binding_id">
              <strong>{{ change.school_model }}</strong> · {{ bindingAdapterMeta(change.adapter as BindingAdapter).label }}
              <div>{{ change.before?.credential_label }} → {{ change.after.credential_label }} · {{ change.after.upstream_model }}</div>
            </li>
          </ul>
          <p v-else class="muted">当前没有可以迁移的线路。</p>
          <ul v-if="channel.preview.blockers.length" class="switch-list">
            <li v-for="item in channel.preview.blockers" :key="item.school_model + '/' + item.adapter">
              <strong>{{ item.school_model }}</strong> · {{ bindingAdapterMeta(item.adapter).label }}
              <div>{{ item.message }}</div>
            </li>
          </ul>
          <div class="actions">
            <button type="button" class="button primary" :disabled="channelBusy || channelReading || !channel.preview.changes.length" @click="confirmChannel">确认重绑</button>
            <button v-if="channel.preview.blockers.some(item => item.code === 'missing_price')" type="button" class="button secondary" @click="openChannelModels">查看模型定价</button>
            <button v-if="channel.preview.blockers.some(item => item.code === 'missing_protocol')" type="button" class="button secondary" :disabled="channelBusy || channelReading" @click="probeChannels">重新探活</button>
          </div>
        </section>
        <section v-if="channel?.models.length" class="switch-section" aria-label="受影响模型">
          <h3>受影响模型</h3>
          <ul class="switch-list">
            <li v-for="model in channel.models" :key="model.model">
              <strong>{{ model.model }}</strong> · {{ model.ready_count }} 条可用线路
              <strong v-if="model.ready_count === 0" class="danger-text"> · 维护中</strong>
              <span v-if="model.loses_last_route" class="danger-text"> · 停用原 Key 后将进入维护</span>
              <span v-else-if="model.losing_route_count" class="danger-text"> · 停用原 Key 会失去 {{ model.losing_route_count }} 条北辰线路</span>
              <div v-for="route in model.routes" :key="route.adapter">
                {{ route.credential_label ?? '自动选 Key' }} · {{ route.upstream_model }} · {{ bindingAdapterMeta(route.adapter).label }}
                <span v-if="route.stale" class="danger-text"> · 绑定失效</span>
                <span v-else-if="!route.ready"> · 未启用</span>
              </div>
              <small v-if="model.ready_count === 0">选择原 Key 和接替 Key，依次同步、核对报价与协议，然后确认重绑。</small>
            </li>
          </ul>
        </section>
        <div v-if="originalChannel" class="actions">
          <button type="button" class="button secondary" :disabled="channelBusy || channelReading" @click="setOriginalChannelStatus">
            {{ originalChannel.status === 'active' ? '停用原 Key' : '重新启用原 Key' }}
          </button>
          <span v-if="retirementImpact.length" class="danger-text">停用会影响 {{ retirementImpact.length }} 个模型</span>
        </div>
        <details v-if="channelHealth.length" class="switch-section">
          <summary>Key 健康 {{ healthyKeys }}/{{ channel?.counts.enabled_keys ?? channelHealth.length }} 把</summary>
          <section v-for="key in channelHealth" :key="key.credential_id">
            <h3>{{ key.label }} · {{ key.health?.available_dimensions }}/{{ healthDimensions.length }} 项可用</h3>
            <dl v-if="key.health" class="switch-health">
              <div v-for="[dimension, title] in healthDimensions" :key="dimension">
                <dt>{{ title }}</dt>
                <dd>{{ key.health[dimension].message }}</dd>
                <dd v-if="key.health[dimension].state !== 'available'">
                  <button type="button" class="button ghost" :disabled="channelBusy || channelReading"
                    @click="runHealthAction(key.credential_id, healthActions[dimension])">
                    {{ healthActionLabels[healthActions[dimension]] }}
                  </button>
                </dd>
              </div>
            </dl>
          </section>
        </details>
      </section>
    </AppDialog>
    <AppDialog :open="Boolean(schoolProvider)" title="北辰模型" description="查看学校首选倍率并管理模型启用状态。" wide @update:open="open => { if (!open) schoolProvider = undefined }">
      <BeichenSchoolModels v-if="schoolProvider" :key="schoolProvider.id" :provider-id="schoolProvider.id" provider-maintenance @catalog-synced="refreshSchoolCatalog" />
    </AppDialog>
    <AppDialog :open="Boolean(aliasProvider)" title="北辰模型别名" description="把指定渠道的上游模型归到已有校内模型。" wide
      @update:open="open => { if (!open) closeAliases() }">
      <section v-if="aliasProvider" class="alias-panel" :aria-busy="readingAliases || savingAlias">
        <p class="muted">保存后，对账按校内名称汇总。要更新调用线路，请在「查看模型」中预览并确认重建绑定。</p>
        <ul v-if="aliases.length" class="alias-list" aria-label="模型别名">
          <li v-for="row in aliases" :key="(row.credential_id ?? '') + '/' + row.upstream_model">
            <div><small>{{ aliasChannel(row.credential_id) }}</small><div>{{ row.upstream_model }} → <strong>{{ row.school_model }}</strong></div></div>
            <div class="actions">
              <button type="button" class="button ghost" :disabled="savingAlias || readingAliases" @click="editAlias(row)">编辑</button>
              <button type="button" class="button ghost" :disabled="savingAlias || readingAliases" @click="removeAlias(row)">移除</button>
            </div>
          </li>
        </ul>
        <p v-else-if="!readingAliases && !aliasError" class="muted">还没有模型别名。</p>
        <p v-if="aliasError" class="error" role="alert">{{ aliasError }}</p>
        <form class="provider-form" @submit.prevent="saveAlias">
          <div class="grid two">
            <label>渠道<select v-model="aliasDraft.credential_id" :disabled="editingAlias || savingAlias || readingAliases">
              <option :value="null">所有渠道的默认别名</option>
              <option v-for="key in aliasProvider.credentials ?? []" :key="key.id" :value="key.id">{{ key.label }}</option>
            </select></label>
            <label>上游模型名称<input v-model="aliasDraft.upstream_model" required maxlength="256" :disabled="editingAlias || savingAlias || readingAliases"></label>
          </div>
          <label>校内模型编号<input v-model="aliasDraft.school_model" required maxlength="256" placeholder="填写模型管理中的模型编号" :disabled="savingAlias || readingAliases"></label>
          <div class="actions">
            <button v-if="editingAlias" type="button" class="button ghost" :disabled="savingAlias" @click="resetAliasDraft">取消编辑</button>
            <button type="submit" class="button primary" :disabled="savingAlias || readingAliases">{{ savingAlias ? '保存中…' : '保存别名' }}</button>
          </div>
        </form>
      </section>
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
.provider-compare { align-self: center; padding: 4px 6px; color: var(--primary); text-decoration: underline; }
.catalog-differences { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); overflow-wrap: anywhere; }
.catalog-differences summary { width: fit-content; cursor: pointer; font-weight: 600; }
.catalog-differences section { padding: 10px 0; }
.catalog-differences section + section { border-top: 1px solid var(--line); }
.catalog-differences h3, .catalog-differences h4 { margin: 0; font-size: 13px; }
.catalog-differences p, .catalog-differences ul { margin: 4px 0 0; font-size: 12px; }
.catalog-differences ul { padding-inline-start: 18px; }
.catalog-difference-groups { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr)); gap: 12px; margin-top: 8px; }
.provider-reconciliation { width: 100%; min-width: 0; }
.provider-reconciliation:has(> *) { margin-top: 16px; border-top: 1px solid var(--line); }
@media (max-width: 900px) {
  .provider-row-head { flex-direction: column; }
  .provider-row-head > .actions { width: 100%; }
}
.provider-form { display: grid; gap: 1rem; }
.switch-panel { display: grid; gap: 12px; overflow-wrap: anywhere; }
.switch-panel p, .switch-panel h3 { margin: 0; }
.switch-section { border-top: 1px solid var(--line); padding-top: 10px; }
.switch-section h3 { font-size: 14px; }
.switch-section summary { cursor: pointer; }
.switch-list { display: grid; gap: 8px; margin: 8px 0; padding-inline-start: 20px; }
.switch-health { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 140px), 1fr)); gap: 8px; margin: 8px 0; }
.switch-health dt { font-weight: 600; }
.switch-health dd { margin: 4px 0; }
.alias-panel { display: grid; gap: 1rem; }
.alias-panel p, .alias-list { margin: 0; }
.alias-list { padding: 0; list-style: none; }
.alias-list li { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; justify-content: space-between; padding: 8px 0; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }
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
