<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ArrowLeft, ChevronRight, ClipboardList, Eye, EyeOff, GraduationCap, Shield } from 'lucide-vue-next'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../../store/auth'
import { APIError } from '../../api/client'
import type { Role } from '../../api/types'
import AuthShell from '../../components/LoginShell.vue'
import LoadingButton from '../../components/LoadingButton.vue'

const form = reactive({ email: '', password: '' })
const error = ref('')
const show = ref(false)
const picker = ref<{ name: string; identities: Role[] } | null>(null)
const selecting = ref<Role | null>(null)
const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const identityMeta: Record<Role, { label: string; desc: string; icon: typeof GraduationCap; tone: string }> = {
  student: {
    label: '学生',
    desc: '申请密钥、浏览模型广场、查看个人用量',
    icon: GraduationCap,
    tone: 'student',
  },
  mentor: {
    label: '导师',
    desc: '审核密钥申请、管理项目、查看调用日志',
    icon: ClipboardList,
    tone: 'mentor',
  },
  teacher: {
    label: '老师',
    desc: '组织与项目治理、供应商和模型管理',
    icon: Shield,
    tone: 'teacher',
  },
}

async function submit() {
  error.value = ''
  picker.value = null
  try {
    const challenge = await auth.login(form.email, form.password)
    if (challenge) {
      picker.value = { name: challenge.user.name, identities: challenge.identities }
      return
    }
    await router.replace(String(route.query.redirect || '/'))
  } catch (e) {
    error.value = e instanceof APIError ? e.message : '登录失败，请稍后重试'
  }
}

async function chooseIdentity(identity: Role) {
  if (auth.loading) return
  error.value = ''
  selecting.value = identity
  try {
    await auth.completeSession(identity)
    await router.replace(String(route.query.redirect || '/'))
  } catch (e) {
    error.value = e instanceof APIError ? e.message : '登录失败，请稍后重试'
  } finally {
    selecting.value = null
  }
}

function resetPicker() {
  picker.value = null
  error.value = ''
  selecting.value = null
}
</script>

<template>
  <AuthShell>
    <section class="auth-card panel" :class="{ 'auth-card-picker': picker }">
      <Transition name="content-fade" mode="out-in">
        <div v-if="picker" key="picker" class="identity-stage">
          <div class="identity-intro">
            <p class="eyebrow">IDENTITY</p>
            <h1>选择登录身份</h1>
            <p class="muted">仅显示已开通的身份；导师身份需由老师在「学生管理」中授予。</p>
          </div>

          <div class="identity-user">
            <span class="identity-avatar" aria-hidden="true">{{ picker.name.slice(0, 1) }}</span>
            <div>
              <strong>{{ picker.name }}</strong>
              <span class="muted">账号已验证，请选择本次登录身份</span>
            </div>
          </div>

          <div class="identity-options">
            <button
              v-for="item in picker.identities"
              :key="item"
              type="button"
              class="identity-option"
              :class="identityMeta[item].tone"
              :disabled="auth.loading"
              :aria-busy="selecting === item"
              @click="chooseIdentity(item)"
            >
              <component :is="identityMeta[item].icon" :size="20" aria-hidden="true" />
              <div class="identity-option-copy">
                <strong>{{ identityMeta[item].label }}</strong>
                <span>{{ identityMeta[item].desc }}</span>
              </div>
              <span v-if="selecting === item" class="loading-spinner loading-spinner-sm" aria-hidden="true" />
              <ChevronRight v-else :size="18" aria-hidden="true" />
            </button>
          </div>

          <p v-if="error" class="error">{{ error }}</p>

          <button type="button" class="identity-back" :disabled="auth.loading" @click="resetPicker">
            <ArrowLeft :size="16" aria-hidden="true" />
            返回重新登录
          </button>
        </div>

        <div v-else key="login">
          <h1>登录控制台</h1>
          <p class="login-description">连接模型，让下一个灵感成为可能。</p>
          <form @submit.prevent="submit">
            <fieldset :disabled="auth.loading">
              <label>邮箱<input v-model="form.email" type="email" autocomplete="email" placeholder="输入你的邮箱" required /></label>
              <label>密码
                <div class="row">
                  <input v-model="form.password" :type="show ? 'text' : 'password'" autocomplete="current-password" placeholder="输入你的密码" required />
                  <button type="button" class="icon-button" :aria-label="show ? '隐藏密码' : '显示密码'" @click="show = !show">
                    <EyeOff v-if="show" :size="18" />
                    <Eye v-else :size="18" />
                  </button>
                </div>
              </label>
            </fieldset>
            <p v-if="error" class="error" role="alert">{{ error }}</p>
            <LoadingButton class="button primary wide" type="submit" :pending="auth.loading" pending-label="登录中…">进入 Nebula <ChevronRight :size="17" aria-hidden="true" /></LoadingButton>
          </form>
          <div class="actions">
            <RouterLink to="/register">注册学生账号</RouterLink>
            <RouterLink to="/recovery">忘记密码</RouterLink>
          </div>
        </div>
      </Transition>
    </section>
  </AuthShell>
</template>
