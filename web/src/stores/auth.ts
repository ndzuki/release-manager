// 认证状态管理 — Pinia store

import { defineStore } from 'pinia'
import { shallowRef, computed } from 'vue'
import type { User } from '@/types'

export const useAuthStore = defineStore('auth', () => {
  const token = shallowRef<string | null>(localStorage.getItem('token'))
  const user = shallowRef<User | null>(null)
  const apiKey = shallowRef<string | null>(localStorage.getItem('apiKey'))

  const isLoggedIn = computed(() => !!token.value)
  const isAdmin = computed(() => user.value?.role === 'admin')
  const canWrite = computed(() => user.value?.role === 'admin' || user.value?.role === 'operator')

  function setSession(t: string, u: User) {
    token.value = t
    user.value = u
    localStorage.setItem('token', t)
  }

  function setApiKey(key: string) {
    apiKey.value = key
    localStorage.setItem('apiKey', key)
  }

  function logout() {
    token.value = null
    user.value = null
    localStorage.removeItem('token')
  }

  function getDingTalkLoginUrl() {
    // 调用后端获取钉钉扫码 URL
    return `/api/v1/auth/dingtalk/url`
  }

  return { token, user, apiKey, isLoggedIn, isAdmin, canWrite, setSession, setApiKey, logout, getDingTalkLoginUrl }
})
