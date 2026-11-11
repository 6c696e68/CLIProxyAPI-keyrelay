<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { createLiveAdminService } from './api/live'
import { MANAGEMENT_STORAGE, panelKey } from './api/http'
import LoginView from './views/LoginView.vue'
import WorkspaceView from './views/WorkspaceView.vue'
import { applyTheme, bootHost, resolveTheme, watchHostTheme, type ThemeName } from './lib/host'

const service = createLiveAdminService()
const storedKey = sessionStorage.getItem(MANAGEMENT_STORAGE) || panelKey()
const authed = ref(false)
const booting = ref(Boolean(storedKey))
const loading = ref(false)
const error = ref('')
const theme = ref<ThemeName>(resolveTheme())
const embedded = document.documentElement.dataset.embedded === 'true'

function setTheme(value: ThemeName) {
  theme.value = value
  applyTheme(value)
}
function toggleTheme() {
  // The panel owns the theme switch while embedded; this path is standalone only.
  const next: ThemeName = theme.value === 'dark' ? 'light' : 'dark'
  localStorage.setItem('keyshop.admin.theme', next)
  setTheme(next)
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

// Keep the panel theme applied before and after mount. The observer covers a
// panel switch that happens while this page is open.
setTheme(resolveTheme())
onMounted(() => {
  watchHostTheme(setTheme)
  void (async () => {
    if (!storedKey) return
    await submit(storedKey, true)
    booting.value = false
  })()
})
</script>

<template>
  <a class="skip-link" href="#content">Skip to content</a>
  <main v-if="booting" class="boot" role="status" aria-live="polite">
    <div class="boot-mark" aria-hidden="true" />
    <p>Checking access</p>
  </main>
  <LoginView v-else-if="!authed" :loading="loading" :error="error" :initial="storedKey" @submit="submit" />
  <WorkspaceView v-else :service="service" :theme="theme" :embedded="embedded" @logout="logout" @toggle-theme="toggleTheme" />
</template>
