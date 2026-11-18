import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { authApi, type User } from '@/api'

export const useAuthStore = defineStore('auth', () => {
  const token = ref(localStorage.getItem('packrun_token') || '')
  const user = ref<User | null>(null)

  const isAdmin = computed(() => user.value?.role === 'admin')
  const isAuditor = computed(() => user.value?.role === 'auditor')
  const canDeploy = computed(() => !!user.value && user.value.role !== 'auditor')
  const displayName = computed(() => user.value?.displayName || user.value?.username || '')

  async function login(payload: { username: string; password: string; otp?: string }) {
    const data = await authApi.login(payload)
    token.value = data.token
    user.value = data.user
    localStorage.setItem('packrun_token', data.token)
  }

  async function fetchMe() {
    if (!token.value) return null
    user.value = await authApi.me()
    return user.value
  }

  function logout() {
    token.value = ''
    user.value = null
    localStorage.removeItem('packrun_token')
  }

  return { token, user, isAdmin, isAuditor, canDeploy, displayName, login, fetchMe, logout }
})
