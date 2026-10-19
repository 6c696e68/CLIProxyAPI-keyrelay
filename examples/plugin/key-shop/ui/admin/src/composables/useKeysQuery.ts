import { ref, watch } from 'vue'

export function useKeysQuery(reload: () => void) {
  const query = ref('')
  const status = ref('')
  const group = ref('')
  const page = ref(1)
  let timer = 0

  function resetPage() {
    if (page.value !== 1) page.value = 1
    else reload()
  }

  watch([status, group], resetPage)
  watch(query, () => {
    window.clearTimeout(timer)
    timer = window.setTimeout(resetPage, 250)
  })
  watch(page, () => reload())

  return { query, status, group, page }
}
