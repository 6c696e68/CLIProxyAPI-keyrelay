<script setup lang="ts">
import { provide, ref } from 'vue'
import AdminShell from '@/components/AdminShell.vue'
import { useRoute } from '@/composables/useRoute'
import { notifyKey, refreshKey, useToast } from '@/composables/useToast'
import DashboardView from '@/views/DashboardView.vue'
import GroupsView from '@/views/GroupsView.vue'
import KeysView from '@/views/KeysView.vue'
import RequestsView from '@/views/RequestsView.vue'
import type { AdminService, Scenario, ThemeName } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario; theme: ThemeName }>(), { scenario: 'ready' })
const emit = defineEmits<{ logout: []; toggleTheme: [] }>()
const { route } = useRoute()
const { toast, notify } = useToast()
const refreshTick = ref(0)
provide(notifyKey, notify)
provide(refreshKey, refreshTick)
</script>

<template>
  <AdminShell :theme="theme" @refresh="refreshTick += 1" @logout="emit('logout')" @toggle-theme="emit('toggleTheme')">
    <main id="content" class="content">
      <DashboardView v-if="route === 'dashboard'" :service="service" :scenario="props.scenario" />
      <GroupsView v-else-if="route === 'groups'" :service="service" :scenario="props.scenario" />
      <KeysView v-else-if="route === 'keys'" :service="service" :scenario="props.scenario" />
      <RequestsView v-else :service="service" :scenario="props.scenario" />
    </main>
    <div v-if="toast" class="toast" role="status">{{ toast }}</div>
  </AdminShell>
</template>
