<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { createLiveAdminService } from './api/live'
import { MANAGEMENT_STORAGE, panelKey } from './api/http'
import LoginView from './views/LoginView.vue'
import WorkspaceView from './views/WorkspaceView.vue'
import type { ThemeName } from './types'

const service = createLiveAdminService()
const authed = ref(false)
const loading = ref(false)
const error = ref('')
const theme = ref<ThemeName>('light')
const initialKey = panelKey()

function applyTheme(value: ThemeName) {
  theme.value = value
  document.documentElement.dataset.theme = value
  localStorage.setItem('keyshop.admin.theme', value)
}
async function submit(value: string, remember: boolean) {
  loading.value = true
  error.value = ''
  try {
    await service.authenticate(value)
    if (remember) sessionStorage.setItem(MANAGEMENT_STORAGE, value)
    else sessionStorage.removeItem(MANAGEMENT_STORAGE)
    authed.value = true
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Authentication failed'
  } finally {
    loading.value = false
  }
}
function logout() {
  sessionStorage.removeItem(MANAGEMENT_STORAGE)
  authed.value = false
}
onMounted(() => {
  const storedTheme = localStorage.getItem('keyshop.admin.theme')
  if (storedTheme === 'dark' || storedTheme === 'light') applyTheme(storedTheme)
  const stored = sessionStorage.getItem(MANAGEMENT_STORAGE) || panelKey()
  if (stored) void submit(stored, true)
})
</script>

<template>
  <a class="skip-link" href="#content">Skip to content</a>
  <LoginView v-if="!authed" :loading="loading" :error="error" :initial="initialKey" @submit="submit" />
  <WorkspaceView v-else :service="service" :theme="theme" @logout="logout" @toggle-theme="applyTheme(theme === 'dark' ? 'light' : 'dark')" />
</template>
