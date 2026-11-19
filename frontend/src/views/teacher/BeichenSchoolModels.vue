<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import { apiErrorMessage } from '../../api/client'
import type { BeichenOverview } from '../../api/types'
import { schoolCatalog } from '../../utils/beichenModels'
import { statusLabel } from '../../utils/format'

const props = defineProps<{ providerId: string; query?: string; commonIds?: Set<string> }>()
const overview = ref<BeichenOverview>()
const loading = ref(false)
const updating = ref('')
const error = ref('')
const rows = computed(() => overview.value ? schoolCatalog(overview.value, props.providerId).filter(row =>
  row.model.toLowerCase().includes((props.query ?? '').trim().toLowerCase()) &&
  (!props.commonIds || props.commonIds.has(row.model.toLowerCase()))) : [])
const loginNeeded = computed(() => overview.value?.web_login_required ?? overview.value?.datasets?.some(row =>
  /重新授权|重新登录|完成登录验证|(?:官网|网站).*(?:授权|会话)(?:已)?(?:失效|过期)/u.test(row.last_error ?? '')))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const day = new Intl.DateTimeFormat('sv-SE', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date())
    overview.value = await teacherAPI.beichenOverview(props.providerId, day)
  } catch (e) {
    overview.value = undefined
    error.value = apiErrorMessage(e, '读取北辰模型失败')
  } finally {
    loading.value = false
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
  <section aria-label="北辰模型目录">
    <button type="button" class="button ghost" :disabled="loading || Boolean(updating)" @click="load">刷新模型</button>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="loginNeeded" role="status">北辰官网需要学校后台重新授权。</p>
    <p v-if="loading" role="status">正在读取模型…</p>
    <div v-if="rows.length" class="model-scroll">
      <table class="usage-table">
        <thead><tr><th scope="col">模型</th><th scope="col">可用状态</th><th scope="col">学校首选倍率与单价</th></tr></thead>
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
              <div v-for="quote in row.quotes" :key="JSON.stringify([quote.multiplier, quote.price])">
                {{ quote.multiplier }}
                <details v-if="quote.price"><summary>单价</summary><small class="price-detail">{{ quote.price }}</small></details>
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
.price-detail { white-space: pre-line; }
summary { cursor: pointer; color: var(--ink-soft); }
th[scope="row"] { white-space: normal; overflow-wrap: anywhere; }
</style>
