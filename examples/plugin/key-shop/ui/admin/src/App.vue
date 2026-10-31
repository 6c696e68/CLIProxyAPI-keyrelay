<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { createLiveAdminService } from './api/live'
import { MANAGEMENT_STORAGE, panelKey } from './api/http'
import LoginView from './views/LoginView.vue'
import WorkspaceView from './views/WorkspaceView.vue'
import type { ThemeName } from './types'

const service = createLiveAdminService()
const storedKey = sessionStorage.getItem(MANAGEMENT_STORAGE) || panelKey()
const authed = ref(false)
const booting = ref(Boolean(storedKey))
const loading = ref(false)
const error = ref('')
const theme = ref<ThemeName>('light')

function applyTheme(value: ThemeName) {
  theme.value = value
  document.documentElement.dataset.theme = value
  localStorage.setItem('keyshop.admin.theme', value)
}
async function submit(value: string, remember: boolean) {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    await service.authenticate(value)
    if (remember) sessionStorage.setItem(MANAGEMENT_STORAGE, value.trim())
    else sessionStorage.removeItem(MANAGEMENT_STORAGE)
    authed.value = true
  } catch (err) {
    sessionStorage.removeItem(MANAGEMENT_STORAGE)
    error.value = err instanceof Error ? err.message : 'Authentication failed'
  } finally {
    loading.value = false
  }
}
function logout() {
  sessionStorage.removeItem(MANAGEMENT_STORAGE)
  authed.value = false
}
const storedTheme = localStorage.getItem('keyshop.admin.theme')
if (storedTheme === 'dark' || storedTheme === 'light') applyTheme(storedTheme)
onMounted(async () => {
  if (!storedKey) return
  await submit(storedKey, true)
  booting.value = false
})
</script>

<template>
  <a class="skip-link" href="#content">Skip to content</a>
  <main v-if="booting" class="boot" role="status" aria-live="polite">
    <div class="boot-mark" aria-hidden="true" />
    <p>Checking access</p>
  </main>
  <LoginView v-else-if="!authed" :loading="loading" :error="error" :initial="storedKey" @submit="submit" />
  <WorkspaceView v-else :service="service" :theme="theme" @logout="logout" @toggle-theme="applyTheme(theme === 'dark' ? 'light' : 'dark')" />
</template>
