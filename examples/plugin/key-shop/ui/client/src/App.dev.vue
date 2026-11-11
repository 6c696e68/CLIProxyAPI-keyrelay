<script setup lang="ts">
import { ref } from 'vue'
import PrototypeBar from './components/PrototypeBar.vue'
import LedgerView from './views/LedgerView.vue'
import LoginView from './views/LoginView.vue'
import { createClientService } from './mock/service'
import type { Scenario, ThemeName } from './types'

const service = createClientService()
const scenarios: Scenario[] = ['ready', 'loading', 'empty', 'error', 'limited']
const params = new URLSearchParams(window.location.search)
const paramScenario = params.get('scenario') as Scenario | null
const authed = ref(Boolean(sessionStorage.getItem('keyshop.prototype.client')) || params.has('scenario') || params.has('preview'))
const loading = ref(false)
const error = ref('')
const scenario = ref<Scenario>(paramScenario && scenarios.includes(paramScenario) ? paramScenario : 'ready')
const theme = ref<ThemeName>('light')

function applyTheme(value: ThemeName, persist = true) {
  theme.value = value
  document.documentElement.dataset.theme = value
  if (persist) localStorage.setItem('keyshop.client.theme', value)
}
async function submit(value: string, remember: boolean) {
  loading.value = true
  error.value = ''
  try {
    await service.authenticate(value)
    if (remember) sessionStorage.setItem('keyshop.prototype.client', '1')
    else sessionStorage.removeItem('keyshop.prototype.client')
    authed.value = true
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Authentication failed'
  } finally {
    loading.value = false
  }
}
function logout() {
  sessionStorage.removeItem('keyshop.prototype.client')
  authed.value = false
}
const storedTheme = localStorage.getItem('keyshop.client.theme')
const paramTheme = params.get('theme')
const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
if (paramTheme === 'dark' || paramTheme === 'light') applyTheme(paramTheme, false)
else if (storedTheme === 'dark' || storedTheme === 'light') applyTheme(storedTheme)
else applyTheme(prefersDark ? 'dark' : 'light', false)
</script>

<template>
  <a class="skip-link" href="#content">Skip to content</a>
  <LoginView v-if="!authed" :loading="loading" :error="error" @submit="submit" />
  <LedgerView v-else :service="service" :scenario="scenario" :theme="theme" @logout="logout" @toggle-theme="applyTheme(theme === 'dark' ? 'light' : 'dark')" />
  <PrototypeBar :scenario="scenario" @update:scenario="scenario = $event" />
</template>
