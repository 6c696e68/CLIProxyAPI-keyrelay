import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { AdminRoute } from '@/types'

const route = ref<AdminRoute>(readRoute())

function readRoute(): AdminRoute {
  const hash = window.location.hash.replace(/^#\/?/, '').split(/[?#]/)[0]
  if (hash === 'groups' || hash === 'keys' || hash === 'requests' || hash === 'tunnels') return hash
  return 'dashboard'
}

function sync() {
  route.value = readRoute()
}

export function useRoute() {
  onMounted(() => window.addEventListener('hashchange', sync))
  onBeforeUnmount(() => window.removeEventListener('hashchange', sync))
  return { route }
}
