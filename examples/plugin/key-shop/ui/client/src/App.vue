<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { CLIENT_STORAGE, createLiveClientService } from './api/live'
import LedgerView from './views/LedgerView.vue'
import LoginView from './views/LoginView.vue'
import type { ThemeName } from './types'

const service = createLiveClientService()
const authed = ref(false)
const loading = ref(false)
const error = ref('')
const theme = ref<ThemeName>('light')

function applyTheme(value: ThemeName) {
  theme.value = value
  document.documentElement.dataset.theme = value
  localStorage.setItem('keyshop.client.theme', value)
}
async function submit(value: string, remember: boolean) {
  loading.value = true
  error.value = ''
  try {
    await service.authenticate(value)
    if (remember) sessionStorage.setItem(CLIENT_STORAGE, value)
    else sessionStorage.removeItem(CLIENT_STORAGE)
    authed.value = true
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Authentication failed'
  } finally {
    loading.value = false
  }
}
function logout() {
  sessionStorage.removeItem(CLIENT_STORAGE)
  authed.value = false
}
onMounted(() => {
  const storedTheme = localStorage.getItem('keyshop.client.theme')
  if (storedTheme === 'dark' || storedTheme === 'light') applyTheme(storedTheme)
  const stored = sessionStorage.getItem(CLIENT_STORAGE)
  if (stored) void submit(stored, true)
})
</script>

<template>
  <a class="skip-link" href="#content">Skip to content</a>
  <LoginView v-if="!authed" :loading="loading" :error="error" @submit="submit" />
  <LedgerView v-else :service="service" :theme="theme" @logout="logout" @toggle-theme="applyTheme(theme === 'dark' ? 'light' : 'dark')" />
</template>
