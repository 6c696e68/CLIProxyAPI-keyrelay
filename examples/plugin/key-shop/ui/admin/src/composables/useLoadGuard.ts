import { ref } from 'vue'

// useLoadGuard keeps a single load in flight. A call that arrives while one
// is running is remembered and executed once, with the latest caller state.
export function useLoadGuard() {
  const loading = ref(false)
  let again = false
  let running: Promise<void> | null = null

  function load(run: () => Promise<void>): Promise<void> {
    if (running) {
      again = true
      return running
    }
    loading.value = true
    running = (async () => {
      try {
        do {
          again = false
          await run()
        } while (again)
      } finally {
        loading.value = false
        running = null
      }
    })()
    return running
  }

  return { loading, load }
}
