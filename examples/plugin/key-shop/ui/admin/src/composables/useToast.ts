import type { InjectionKey, Ref } from 'vue'
import { ref } from 'vue'

export const notifyKey: InjectionKey<(message: string) => void> = Symbol('notify')
export const refreshKey: InjectionKey<Ref<number>> = Symbol('refresh')

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
