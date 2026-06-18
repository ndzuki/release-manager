<script setup lang="ts">
import { shallowRef, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()
const mode = shallowRef<'login' | 'init' | 'apikey' | 'dingtalk'>('login')
const apiKey = shallowRef('')
const error = shallowRef('')
const loading = shallowRef(true)

// 初始化表单
const initForm = shallowRef({ username: '', password: '', email: '' })

async function checkInitStatus() {
  try {
    const r = await fetch('/api/v1/init')
    const data = await r.json()
    if (!data.initialized) {
      mode.value = 'init'
    }
  } finally {
    loading.value = false
  }
}

async function handleInit() {
  error.value = ''
  const f = initForm.value
  if (!f.username || f.username.length < 3) { error.value = '用户名至少 3 个字符'; return }
  if (!f.password || f.password.length < 6) { error.value = '密码至少 6 个字符'; return }
  if (!f.email || !f.email.includes('@')) { error.value = '请输入有效的邮箱地址'; return }

  const r = await fetch('/api/v1/init', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(f),
  })
  if (!r.ok) {
    const d = await r.json().catch(() => ({}))
    error.value = d.error || '初始化失败'
    return
  }
  // 初始化成功，切换到登录页
  mode.value = 'login'
}

async function handleLogin() {
  error.value = ''
  try {
    const r = await fetch('/api/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: initForm.value.username, password: initForm.value.password }),
    })
    if (!r.ok) { error.value = '用户名或密码错误'; return }
    const data = await r.json()
    auth.setSession(data.token, { id: 'admin', org_id: 'default', name: data.username, email: data.email, role: 'admin', auth_provider: 'local' })
    router.push('/')
  } catch { error.value = '登录失败' }
}

function loginWithApiKey() {
  if (!apiKey.value) { error.value = '请输入 API Key'; return }
  auth.setApiKey(apiKey.value)
  router.push('/')
}
function loginWithDingTalk() { window.location.href = auth.getDingTalkLoginUrl() }

onMounted(checkInitStatus)
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <h1>Release Manager</h1>
      <p class="subtitle">运维管理平台</p>

      <div v-if="loading" class="loading">检查系统状态...</div>

      <!-- 初始化表单 -->
      <div v-else-if="mode === 'init'">
        <p style="text-align:center;margin-bottom:16px;color:var(--color-warning)">
          首次使用，请创建管理员账号
        </p>
        <div class="form-group"><label>用户名</label><input v-model="initForm.username" placeholder="至少 3 个字符" /></div>
        <div class="form-group"><label>密码</label><input v-model="initForm.password" type="password" placeholder="至少 6 个字符" /></div>
        <div class="form-group"><label>邮箱</label><input v-model="initForm.email" type="email" placeholder="用于验证和找回密码" /></div>
        <p v-if="error" class="error">{{ error }}</p>
        <button class="btn-primary" style="width:100%" @click="handleInit">创建管理员账号</button>
      </div>

      <!-- 登录表单 -->
      <div v-else>
        <div class="tabs">
          <button :class="{ active: mode === 'login' }" @click="mode = 'login'">账号登录</button>
          <button :class="{ active: mode === 'apikey' }" @click="mode = 'apikey'">API Key</button>
          <button :class="{ active: mode === 'dingtalk' }" @click="mode = 'dingtalk'">钉钉扫码</button>
        </div>

        <div v-if="mode === 'login'" class="login-form">
          <div class="form-group"><label>用户名</label><input v-model="initForm.username" placeholder="用户名" @keyup.enter="handleLogin" /></div>
          <div class="form-group"><label>密码</label><input v-model="initForm.password" type="password" placeholder="密码" @keyup.enter="handleLogin" /></div>
          <p v-if="error" class="error">{{ error }}</p>
          <button class="btn-primary" style="width:100%" @click="handleLogin">登录</button>
        </div>

        <div v-else-if="mode === 'apikey'" class="login-form">
          <div class="form-group"><label>API Key</label><input v-model="apiKey" type="password" placeholder="输入 API Key" @keyup.enter="loginWithApiKey" /></div>
          <p v-if="error" class="error">{{ error }}</p>
          <button class="btn-primary" style="width:100%" @click="loginWithApiKey">登录</button>
        </div>

        <div v-else class="login-form">
          <p style="text-align:center;color:var(--color-text-secondary);margin-bottom:16px">点击下方按钮跳转钉钉扫码登录</p>
          <button class="btn-primary" style="width:100%;background:#0089ff" @click="loginWithDingTalk">钉钉扫码登录</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-page { min-height: 100vh; display: flex; align-items: center; justify-content: center; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); }
.login-card { background: #fff; padding: 40px; border-radius: 12px; width: 420px; box-shadow: 0 20px 60px rgba(0,0,0,.15); }
.login-card h1 { text-align: center; font-size: 24px; }
.subtitle { text-align: center; color: var(--color-text-secondary); margin-bottom: 24px; }
.tabs { display: flex; margin-bottom: 20px; border-bottom: 1px solid var(--color-border); }
.tabs button { flex: 1; background: none; border-radius: 0; padding: 10px; color: var(--color-text-secondary); border-bottom: 2px solid transparent; }
.tabs button.active { color: var(--color-primary); border-bottom-color: var(--color-primary); }
.error { color: var(--color-danger); font-size: 13px; margin-bottom: 8px; }
.loading { text-align: center; padding: 20px; color: var(--color-text-secondary); }
</style>
