<script setup lang="ts">
import { ref } from 'vue'
import Icon from './ui/Icon.vue'
import AppButton from './ui/AppButton.vue'
import { useRoute } from '@/composables/useRoute'
import type { ThemeName } from '@/types'

const props = defineProps<{ theme: ThemeName; busy?: boolean }>()
const emit = defineEmits<{ refresh: []; logout: []; toggleTheme: [] }>()
function refresh() {
  if (props.busy) return
  emit('refresh')
}
const { route } = useRoute()
const collapsed = ref(false)
const mobileOpen = ref(false)
const groups = [
  { label: 'Overview', items: [{ id: 'dashboard', href: '#/', label: 'Dashboard', icon: 'panel' }] },
  { label: 'Access', items: [{ id: 'groups', href: '#/groups', label: 'Model groups', icon: 'layers' }, { id: 'keys', href: '#/keys', label: 'Keys', icon: 'key' }] },
  { label: 'Activity', items: [{ id: 'requests', href: '#/requests', label: 'Request log', icon: 'activity' }, { id: 'tunnels', href: '#/tunnels', label: 'Tunnels', icon: 'layers' }] },
]
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
          <a
            v-for="item in group.items"
            :key="item.id"
            class="nav-item"
            :class="{ active: route === item.id }"
            :href="item.href"
            :aria-current="route === item.id ? 'page' : undefined"
            @click="mobileOpen = false"
          >
            <Icon :name="item.icon" />
            <span class="nav-text">{{ item.label }}</span>
          </a>
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
          <AppButton class="icon-btn" :aria-label="busy ? 'Refreshing' : 'Refresh'" :disabled="busy" @click="refresh"><Icon name="refresh" /></AppButton>
          <AppButton class="icon-btn" :aria-label="theme === 'dark' ? 'Use light theme' : 'Use dark theme'" @click="emit('toggleTheme')">
            <Icon :name="theme === 'dark' ? 'sun' : 'moon'" />
          </AppButton>
          <AppButton class="icon-btn" aria-label="Sign out" @click="emit('logout')"><Icon name="logout" /></AppButton>
        </div>
      </nav>
    </div>
  </div>
</template>
