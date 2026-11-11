<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import Icon from './ui/Icon.vue'
import AppButton from './ui/AppButton.vue'
import { useRoute } from '@/composables/useRoute'
import type { ThemeName } from '@/types'

const props = defineProps<{ theme: ThemeName; busy?: boolean; embedded?: boolean }>()
const emit = defineEmits<{ refresh: []; logout: []; toggleTheme: [] }>()
function refresh() {
  if (props.busy) return
  emit('refresh')
}
const { route } = useRoute()
const tabsEl = ref<HTMLElement | null>(null)
const tabs = [
  { id: 'dashboard', href: '#/', label: 'Dashboard' },
  { id: 'groups', href: '#/groups', label: 'Model groups' },
  { id: 'keys', href: '#/keys', label: 'Keys' },
  { id: 'plans', href: '#/plans', label: 'Plans' },
  { id: 'partners', href: '#/partners', label: 'Partners' },
  { id: 'requests', href: '#/requests', label: 'Request log' },
  { id: 'tunnels', href: '#/tunnels', label: 'Tunnels' },
]

// On narrow screens the tab strip scrolls; keep the selected tab visible.
function revealActive() {
  void nextTick(() => {
    tabsEl.value?.querySelector<HTMLElement>('.tab.active')?.scrollIntoView({ block: 'nearest', inline: 'center' })
  })
}
onMounted(revealActive)
watch(route, revealActive)
</script>

<template>
  <div class="shell">
    <header class="appbar">
      <div class="appbar-inner">
        <nav ref="tabsEl" class="tabs" aria-label="Key shop sections">
          <a
            v-for="tab in tabs"
            :key="tab.id"
            class="tab"
            :class="{ active: route === tab.id }"
            :href="tab.href"
            :aria-current="route === tab.id ? 'page' : undefined"
          >{{ tab.label }}</a>
        </nav>
        <div class="appbar-actions">
          <slot name="tools" />
          <AppButton class="icon-btn" :aria-label="busy ? 'Refreshing' : 'Refresh'" :disabled="busy" @click="refresh">
            <Icon name="refresh" />
          </AppButton>
          <AppButton
            v-if="!embedded"
            class="icon-btn"
            :aria-label="theme === 'dark' ? 'Use light theme' : 'Use dark theme'"
            @click="emit('toggleTheme')"
          >
            <Icon :name="theme === 'dark' ? 'sun' : 'moon'" />
          </AppButton>
          <AppButton v-if="!embedded" class="icon-btn" aria-label="Sign out" @click="emit('logout')">
            <Icon name="logout" />
          </AppButton>
        </div>
      </div>
    </header>
    <slot />
  </div>
</template>
