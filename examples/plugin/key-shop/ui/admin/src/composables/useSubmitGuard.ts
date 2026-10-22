import { ref } from 'vue'

// useSubmitGuard rejects a second call while the first promise is still running.
// The button stays disabled and the draft is left alone until the promise settles.
export function useSubmitGuard() {
  const pending = ref(false)

  async function guard<T>(action: () => Promise<T>): Promise<T | undefined> {
    if (pending.value) return undefined
    pending.value = true
    try {
      return await action()
    } finally {
      pending.value = false
    }
  }

  return { pending, guard }
}
