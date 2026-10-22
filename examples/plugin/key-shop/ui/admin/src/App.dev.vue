<script setup lang="ts">
import { ref } from 'vue'
import PrototypeBar from './components/PrototypeBar.vue'
import LoginView from './views/LoginView.vue'
import WorkspaceView from './views/WorkspaceView.vue'
import { createAdminService } from './mock/service'
import type { Scenario, ThemeName } from './types'

const service = createAdminService()
const authed = ref(Boolean(sessionStorage.getItem('keyshop.prototype.admin')))
const loading = ref(false)
const error = ref('')
const scenario = ref<Scenario>('ready')
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
    if (remember) sessionStorage.setItem('keyshop.prototype.admin', '1')
    else sessionStorage.removeItem('keyshop.prototype.admin')
    authed.value = true
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Authentication failed'
  } finally {
    loading.value = false
  }
}
function logout() {
  sessionStorage.removeItem('keyshop.prototype.admin')
  authed.value = false
}
const storedTheme = localStorage.getItem('keyshop.admin.theme')
if (storedTheme === 'dark' || storedTheme === 'light') applyTheme(storedTheme)
</script>

<template>
  <a class="skip-link" href="#content">Skip to content</a>
  <LoginView v-if="!authed" :loading="loading" :error="error" @submit="submit" />
  <WorkspaceView v-else :service="service" :scenario="scenario" :theme="theme" @logout="logout" @toggle-theme="applyTheme(theme === 'dark' ? 'light' : 'dark')" />
  <PrototypeBar :scenario="scenario" @update:scenario="scenario = $event" />
</template>
