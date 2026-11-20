<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenOverview } from '../../api/types'
import { schoolCatalog } from '../../utils/beichenModels'
import { schoolDay, statusLabel } from '../../utils/format'
import BeichenRecentReceipts from './BeichenRecentReceipts.vue'

const props = defineProps<{ providerId: string; query?: string; commonIds?: Set<string>; providerMaintenance?: boolean }>()
const overview = ref<BeichenOverview>()
const loading = ref(false)
const updating = ref('')
const syncing = ref(false)
const error = ref('')
const rows = computed(() => overview.value ? schoolCatalog(overview.value, props.providerId).filter(row =>
  row.model.toLowerCase().includes((props.query ?? '').trim().toLowerCase()) &&
  (!props.commonIds || props.commonIds.has(row.model.toLowerCase()))) : [])
const loginNeeded = computed(() => overview.value?.web_login_required ?? overview.value?.datasets?.some(row =>
  /重新授权|重新登录|完成登录验证|(?:官网|网站).*(?:授权|会话)(?:已)?(?:失效|过期)/u.test(row.last_error ?? '')))
const priceErrors = computed(() => [...new Set((overview.value?.datasets ?? [])
  .filter(row => row.kind === 'prices' && row.last_error)
  .map(row => row.last_error as string))])
const workspaceUrl = computed(() => {
  const raw = overview.value?.maintenance_workspace?.launch_url
  if (typeof raw !== 'string' || !/^https:\/\/[^/@]+(?:\/|$)/i.test(raw)) return null
  // 只接受原始的主机和路径，拒绝参数、片段及浏览器会自动修正的分隔符。
  if (/[?#\\\s\u0000-\u001f\u007f]/u.test(raw)) return null
  try {
    const target = new URL(raw)
    // 同一主机即使端口不同，也可能收到学校的会话 Cookie。
    return target.hostname === window.location.hostname ? null : target.href
  } catch {
    return null
  }
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    overview.value = await teacherAPI.beichenOverview(props.providerId, schoolDay())
  } catch (e) {
    overview.value = undefined
    error.value = apiErrorMessage(e, '读取北辰模型失败')
  } finally {
    loading.value = false
  }
}

function refreshOverview() {
  if (!loading.value && !updating.value) void load()
}

async function syncPrices() {
  if (syncing.value || loading.value || updating.value) return
  syncing.value = true
  error.value = ''
  try {
    await teacherAPI.syncBeichen(props.providerId)
    await load()
  } catch (e) {
    error.value = apiErrorMessage(e, '立即同步失败，请稍后重试')
  } finally {
    syncing.value = false
  }
}

async function changeStatus(row: (typeof rows.value)[number], enable: boolean) {
  if (updating.value || loading.value) return
  updating.value = row.model
  error.value = ''
  try {
    if (enable) {
      await teacherAPI.enableBeichenModel(props.providerId, row.model, row.school)
    } else {
      await teacherAPI.updateModel({ model_id: row.school, status: 'inactive' })
    }
    await load()
  } catch (e) {
    error.value = apiErrorMessage(e, '模型状态更新失败')
  } finally {
    updating.value = ''
  }
}
onMounted(load)
</script>

<template>
  <section v-if="providerMaintenance && overview?.maintenance_workspace" aria-label="校方北辰工作区">
    <a v-if="workspaceUrl" class="button secondary" :href="workspaceUrl" target="_blank"
      rel="noopener noreferrer" referrerpolicy="no-referrer">打开校方北辰工作区</a>
    <p v-else class="muted">校方尚未配置远程应用地址</p>
  </section>
  <BeichenRecentReceipts v-if="providerMaintenance" :provider-id="providerId" :website-sync="overview?.website_sync"
    @refresh-overview="refreshOverview" />
  <section aria-label="北辰模型目录">
    <div class="model-toolbar">
      <button type="button" class="button ghost" :disabled="loading || Boolean(updating)" @click="load">刷新模型</button>
      <button type="button" class="button secondary" :disabled="loading || syncing || Boolean(updating)" @click="syncPrices">
        {{ syncing ? '同步中…' : '立即同步价格' }}
      </button>
    </div>
    <p class="muted">单价按官网计费口径展示。官网标准价可独立于账单当前报价，倍率后金额按当前倍率计算。</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="providerMaintenance && loginNeeded" role="status">北辰官网需要学校后台重新授权。</p>
    <p v-if="priceErrors.length" class="error" role="alert">价格同步失败：{{ priceErrors.join('；') }}</p>
    <p v-if="loading" role="status">正在读取模型…</p>
    <div v-if="rows.length" class="model-scroll">
      <table class="usage-table">
        <thead><tr><th scope="col">模型</th><th scope="col">可用状态</th><th scope="col">学校首选倍率与当前报价</th></tr></thead>
        <tbody>
          <tr v-for="row in rows" :key="row.model">
            <th scope="row">{{ row.model }}</th>
            <td>
              <span>{{ row.status === 'not_configured' ? '未配置' : statusLabel(row.status ?? undefined) || '待同步' }}</span>
              <div class="model-actions">
                <button v-if="row.canEnable" type="button" class="button secondary" :disabled="loading || Boolean(updating)"
                  :aria-label="(row.status === 'active' ? '重新启用 ' : '启用 ') + row.model" @click="changeStatus(row, true)">
                  {{ row.status === 'active' ? '重新启用' : '启用' }}
                </button>
                <button v-if="row.canDisable" type="button" class="button secondary" :disabled="loading || Boolean(updating)"
                  :aria-label="'禁用 ' + row.model" @click="changeStatus(row, false)">禁用</button>
                <small v-else-if="row.status !== 'active' && !row.canEnable" class="muted">{{ row.canEnable === false ? '暂不可启用' : '启用条件待同步' }}</small>
              </div>
            </td>
            <td>
              <div v-for="quote in row.quotes" :key="JSON.stringify([quote.multiplier, quote.price])" class="quote-row">
                <span class="quote-multiplier">{{ quote.multiplier }}</span>
                <span v-if="quote.price" class="price-detail">{{ quote.price }}</span>
                <small v-else class="muted">单价待同步</small>
              </div>
              <span v-if="!row.quotes.length">待同步</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else-if="!loading && !error" class="muted">暂无模型记录。</p>
  </section>
</template>

<style scoped>
.model-scroll { overflow-x: auto; }
.model-actions { display: flex; gap: .5rem; flex-wrap: wrap; align-items: center; margin-top: .35rem; }
.model-toolbar { display: flex; gap: .5rem; flex-wrap: wrap; margin-bottom: .5rem; }
.quote-row { display: grid; gap: .2rem; }
.quote-row + .quote-row { margin-top: .7rem; padding-top: .7rem; border-top: 1px solid var(--line, #e5e7eb); }
.quote-multiplier { font-variant-numeric: tabular-nums; }
.price-detail { display: block; white-space: pre-line; color: var(--ink-soft); }
th[scope="row"] { white-space: normal; overflow-wrap: anywhere; }
</style>
