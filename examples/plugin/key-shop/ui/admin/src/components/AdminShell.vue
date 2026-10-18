<script setup lang="ts">
import { ref } from 'vue'
import Icon from './ui/Icon.vue'
import AppButton from './ui/AppButton.vue'
import type { ThemeName } from '@/types'

defineProps<{ theme: ThemeName }>()
const emit = defineEmits<{ refresh: []; logout: []; toggleTheme: [] }>()
const collapsed = ref(false)
const mobileOpen = ref(false)
const groups = [
  { label: 'Overview', items: [{ id: 'overview', label: 'Health', icon: 'panel' }] },
  { label: 'Access', items: [{ id: 'policy', label: 'Models', icon: 'key' }, { id: 'inventory', label: 'Keys', icon: 'search' }] },
  { label: 'Activity', items: [{ id: 'requests', label: 'Requests', icon: 'refresh' }] },
]

function go(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  mobileOpen.value = false
}
</script>

<template>
  <div class="shell">
    <button v-if="mobileOpen" class="only-mobile nav-scrim" aria-label="Close navigation" @click="mobileOpen = false" />
    <aside class="sidebar" :class="{ collapsed, open: mobileOpen }">
      <div class="brand">
        <span class="brand-mark">K</span>
        <span class="brand-copy min-w-0">
          <span class="brand-title block truncate">API keys</span>
          <span class="brand-sub block truncate">Key shop</span>
        </span>
      </div>
      <nav class="grid gap-3" aria-label="Sections">
        <div v-for="group in groups" :key="group.label" class="nav-group">
          <div class="nav-label">{{ group.label }}</div>
          <button v-for="item in group.items" :key="item.id" type="button" class="nav-item" @click="go(item.id)">
            <Icon :name="item.icon" />
            <span class="nav-text">{{ item.label }}</span>
          </button>
        </div>
      </nav>
      <AppButton class="mt-auto only-desktop-flex" :aria-label="collapsed ? 'Expand sidebar' : 'Collapse sidebar'" @click="collapsed = !collapsed">
        <Icon name="panel" />
      </AppButton>
    </aside>
    <div class="workspace">
      <header class="topbar only-mobile">
        <div class="float-cluster">
          <AppButton class="icon-btn" aria-label="Open navigation" @click="mobileOpen = true"><Icon name="menu" /></AppButton>
        </div>
      </header>
      <slot />
      <nav class="dock" aria-label="Workspace actions">
        <div class="float-cluster">
          <AppButton class="icon-btn" aria-label="Refresh" @click="emit('refresh')"><Icon name="refresh" /></AppButton>
          <AppButton class="icon-btn" :aria-label="theme === 'dark' ? 'Use light theme' : 'Use dark theme'" @click="emit('toggleTheme')">
            <Icon :name="theme === 'dark' ? 'sun' : 'moon'" />
          </AppButton>
          <AppButton class="icon-btn" aria-label="Sign out" @click="emit('logout')"><Icon name="logout" /></AppButton>
        </div>
      </nav>
    </div>
  </div>
</template>
