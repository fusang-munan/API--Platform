<script setup lang="ts">
import { onBeforeUnmount, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { authAPI } from '../../api/auth'
import { APIError } from '../../api/client'
import AuthShell from '../../components/AuthShell.vue'
import LoadingButton from '../../components/LoadingButton.vue'
import { useToast } from '../../composables/useToast'

const form = reactive({ name: '', email: '', password: '', confirm: '', verification_code: '' })
const error = ref('')
const seconds = ref(0)
const sentFor = ref('')
const sending = ref(false)
const submitting = ref(false)
const router = useRouter()
const toast = useToast()
let timer = 0

onBeforeUnmount(() => clearInterval(timer))

async function send() {
  if (sending.value) return
  error.value = ''
  sending.value = true
  try {
    await authAPI.sendCode(form.email, 'student_registration')
    sentFor.value = form.email
    seconds.value = 60
    clearInterval(timer)
    timer = window.setInterval(() => {
      seconds.value--
      if (seconds.value <= 0) clearInterval(timer)
    }, 1000)
  } catch (e) {
    error.value = e instanceof APIError ? e.message : '验证码发送失败'
  } finally {
    sending.value = false
  }
}

async function submit() {
  if (submitting.value) return
  error.value = ''
  if (form.password.length < 12 || form.password.length > 128) {
    error.value = '密码长度必须为 12–128 个字符'
    return
  }
  if (form.password !== form.confirm) {
    error.value = '两次输入的密码不一致'
    return
  }
  if (sentFor.value !== form.email) {
    error.value = '请为当前邮箱重新发送验证码'
    return
  }
  submitting.value = true
  try {
    await authAPI.register(form)
    toast.success('注册成功，请登录')
    await router.replace('/login')
  } catch (e) {
    error.value = e instanceof APIError ? e.message : '注册失败'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <AuthShell>
    <section class="auth-card panel">
      <RouterLink to="/login">← 返回登录</RouterLink>
      <h1>注册学生账号</h1>
      <p class="muted">导师身份需由老师授予，注册后仅创建学生身份。</p>
      <form @submit.prevent="submit">
        <fieldset :disabled="submitting">
          <label>姓名<input v-model="form.name" maxlength="64" required /></label>
          <label>邮箱<input v-model="form.email" type="email" required /></label>
          <label>密码<input v-model="form.password" type="password" minlength="12" maxlength="128" required /></label>
          <label>确认密码<input v-model="form.confirm" type="password" minlength="12" maxlength="128" required /></label>
          <label>邮箱验证码
            <div class="row">
              <input v-model="form.verification_code" required />
              <LoadingButton class="button secondary" :pending="sending" pending-label="发送中…" :disabled="seconds > 0" @click="send">
                {{ seconds > 0 ? `${seconds}s 后重发` : '发送验证码' }}
              </LoadingButton>
            </div>
          </label>
        </fieldset>
        <p v-if="error" class="error">{{ error }}</p>
        <LoadingButton class="button primary wide" type="submit" :pending="submitting" pending-label="注册中…">注册</LoadingButton>
      </form>
    </section>
  </AuthShell>
</template>
