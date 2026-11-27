<script setup lang="ts">
import { computed, ref } from 'vue'
import { teacherAPI } from '../../api/teacher'
import type { BeichenWebsiteKey, BeichenWebsiteKeyInventory, Provider } from '../../api/types'

const { provider } = defineProps<{ provider: Provider }>()
const inventory = ref<BeichenWebsiteKeyInventory>()
const busy = ref(false)
const error = ref('')

const states: Record<BeichenWebsiteKey['status'], string> = {
  active: '已启用',
  inactive: '已停用',
  disabled: '已停用',
  expired: 'Key 已过期',
  quota_exhausted: '额度用尽',
  unknown: '待核实',
}
const messages: Partial<Record<BeichenWebsiteKeyInventory['status'], string>> = {
  unsynced: '尚未同步北辰官网 Key。可在北辰网页登录后刷新。',
  stale: '清单已超过 30 分钟未更新，可刷新官网 Key。',
  session_expired: '北辰网页会话已过期，请重新登录后刷新。',
  unavailable: '本次未能读取北辰官网 Key，请核对网页登录状态后重试。',
}
const message = computed(() => inventory.value ? messages[inventory.value.status] : undefined)
const dateTime = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium', timeStyle: 'short', timeZone: 'Asia/Shanghai',
})

function membership(key: BeichenWebsiteKey) {
  if (key.match_status === 'ambiguous') return '匹配不唯一'
  if (key.match_status === 'unmatched') {
    return key.status === 'active' ? '未匹配校内 Key（官网仍启用）' : '未匹配校内 Key'
  }
  return provider.credentials?.find(credential => credential.id === key.credential_id)?.label ?? '已匹配'
}

async function load(refresh = false) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    inventory.value = await (refresh ? teacherAPI.refreshBeichenWebsiteKeys(provider.id) : teacherAPI.beichenWebsiteKeys(provider.id))
  } catch {
    error.value = '读取 Key 清单失败，请稍后重试。'
  } finally {
    busy.value = false
  }
}

function toggled(event: Event) {
  if ((event.currentTarget as HTMLDetailsElement).open) void load()
}
</script>

<template>
  <details class="website-keys" @toggle="toggled">
    <summary>北辰官网 Key 清单</summary>
    <div class="key-status" :aria-busy="busy">
      <button type="button" class="button secondary" :disabled="busy" @click="load(true)">
        {{ busy ? '读取中…' : '刷新官网 Key' }}
      </button>
      <span v-if="inventory?.last_seen" class="muted">
        最近成功读取 {{ dateTime.format(new Date(inventory.last_seen)) }}（北京时间）
      </span>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="message" role="status" class="muted">{{ message }}</p>
    <p v-if="inventory?.status === 'current' && inventory.keys.length === 0" class="muted">
      此北辰账号的 API 密钥列表为空。
    </p>
    <div v-if="inventory?.keys.length" class="key-table">
      <table aria-label="北辰官网 Key 与学校渠道">
        <thead>
          <tr><th scope="col">Key</th><th scope="col">分组</th><th scope="col">分组倍率</th><th scope="col">官网状态</th><th scope="col">学校渠道</th></tr>
        </thead>
        <tbody>
          <tr v-for="key in inventory.keys" :key="key.id">
            <th scope="row">
              {{ key.name || `Key #${key.id}` }}
              <span v-if="key.key_prefix" class="muted"> {{ key.key_prefix }}…</span>
            </th>
            <td>{{ key.group_name ?? (key.group_id == null ? '待核实' : `#${key.group_id}`) }}</td>
            <td>{{ key.rate == null ? '待核实' : `${key.rate}x` }}</td>
            <td>{{ states[key.status] }}</td>
            <td>{{ membership(key) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </details>
</template>

<style scoped>
.website-keys { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); }
summary { width: fit-content; cursor: pointer; font-weight: 600; }
.key-status { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; margin-top: 12px; }
.key-table { overflow-x: auto; margin-top: 12px; }
table { width: 100%; border-collapse: collapse; text-align: left; }
th, td { padding: 8px; border-bottom: 1px solid var(--line); }
</style>
