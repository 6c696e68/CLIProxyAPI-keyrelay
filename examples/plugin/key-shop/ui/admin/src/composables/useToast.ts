import type { InjectionKey, Ref, WatchSource } from 'vue'
import { inject, onBeforeUnmount, ref, watch } from 'vue'

export const notifyKey: InjectionKey<(message: string) => void> = Symbol('notify')
export const refreshKey: InjectionKey<Ref<number>> = Symbol('refresh')
export const viewBusyKey: InjectionKey<Ref<boolean>> = Symbol('viewBusy')

export function useToast() {
  const toast = ref('')
  let timer = 0
  function notify(message: string) {
    toast.value = message
    window.clearTimeout(timer)
    timer = window.setTimeout(() => { toast.value = '' }, 2200)
  }
  return { toast, notify }
}

// bindViewBusy publishes the view's pending state before any immediate load
// watch runs, and clears it when the view unmounts.
export function bindViewBusy(source: WatchSource<boolean>) {
  const viewBusy = inject(viewBusyKey, ref(false))
  watch(source, (value) => { viewBusy.value = value }, { immediate: true })
  onBeforeUnmount(() => { viewBusy.value = false })
}
