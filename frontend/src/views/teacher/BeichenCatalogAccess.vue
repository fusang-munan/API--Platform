<script setup lang="ts">
import { ref, watch } from 'vue'
import { apiErrorMessage } from '../../api/client'
import { teacherAPI } from '../../api/teacher'
import type { ProviderCredential } from '../../api/types'
import LoadingButton from '../../components/LoadingButton.vue'

const props = defineProps<{ providerId: string; credentials: ProviderCredential[] }>()
const secure = window.location.protocol === 'https:'
const credential = ref('')
const group = ref('')
const upstreamKeyID = ref('')
const token = ref('')
const configured = ref(false)
const busy = ref(false)
const message = ref('')

watch(credential, async id => {
  group.value = upstreamKeyID.value = token.value = message.value = ''
  configured.value = false
  if (!id) return
  busy.value = true
  try {
    const state = await teacherAPI.beichenPanelStatus(props.providerId, id)
    if (id === credential.value) {
      configured.value = state.configured
      group.value = state.group_id ? String(state.group_id) : ''
      upstreamKeyID.value = state.upstream_key_id ? String(state.upstream_key_id) : ''
    }
  } catch (e) {
    if (id === credential.value) message.value = apiErrorMessage(e)
  } finally {
    if (id === credential.value) busy.value = false
  }
})

async function save(enabled: boolean) {
  busy.value = true
  message.value = ''
  try {
    const result = await teacherAPI.setBeichenPanelAccess(props.providerId, credential.value,
      enabled ? { enabled, group_id: Number(group.value), upstream_key_id: upstreamKeyID.value ? Number(upstreamKeyID.value) : undefined, token: token.value } : { enabled })
    configured.value = result.configured
    group.value = result.group_id ? String(result.group_id) : ''
    upstreamKeyID.value = result.upstream_key_id ? String(result.upstream_key_id) : ''
    message.value = enabled ? '已保存，后台将重新同步报价。' : '已停用此凭据的面板采价。'
  } catch (e) {
    message.value = apiErrorMessage(e)
  } finally {
    token.value = ''
    busy.value = false
  }
}
</script>

<template>
  <details class="beichen-section">
    <summary>自动采价授权</summary>
    <p v-if="!secure">请从学校 HTTPS 管理入口配置采价授权。</p>
    <form v-else class="beichen-body" @submit.prevent="save(true)">
      <label>北辰推理凭据
        <select v-model="credential" required :disabled="busy">
          <option disabled value="">请选择凭据</option>
          <option v-for="item in credentials" :key="item.id" :value="item.id">{{ item.label }}</option>
        </select>
      </label>
      <template v-if="credential">
        <p v-if="configured">本凭据已配置自动采价。更新授权时请填写新令牌。</p>
        <label>北辰分组 ID<input v-model="group" required type="number" min="1" max="9007199254740991" step="1" :disabled="busy"></label>
        <label>北辰 Key 数字 ID（逐笔核账时填写）<input v-model="upstreamKeyID" type="number" min="1" max="9007199254740991" step="1" :disabled="busy"></label>
        <p>填写北辰用量记录中的 api_key_id。这里仅需数字，留空时只同步报价。</p>
        <label>北辰面板读取令牌<input v-model="token" type="password" required maxlength="4096" autocomplete="off" :disabled="busy"></label>
        <LoadingButton class="button primary" type="submit" :pending="busy">保存采价授权</LoadingButton>
        <button v-if="configured" class="button secondary" type="button" :disabled="busy" @click="save(false)">停用采价</button>
      </template>
      <p v-if="message" role="status">{{ message }}</p>
    </form>
  </details>
</template>
