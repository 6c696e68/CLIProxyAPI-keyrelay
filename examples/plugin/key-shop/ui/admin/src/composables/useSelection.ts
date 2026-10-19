import { computed, ref } from 'vue'

export function useSelection() {
  const selected = ref<string[]>([])
  const count = computed(() => selected.value.length)

  function has(id: string) {
    return selected.value.includes(id)
  }
  function toggle(id: string) {
    selected.value = has(id) ? selected.value.filter((item) => item !== id) : [...selected.value, id]
  }
  function pageChecked(ids: string[]) {
    return ids.length > 0 && ids.every((id) => has(id))
  }
  function togglePage(ids: string[]) {
    if (pageChecked(ids)) selected.value = selected.value.filter((id) => !ids.includes(id))
    else selected.value = [...new Set([...selected.value, ...ids])]
  }
  function clear() {
    selected.value = []
  }
  function drop(ids: string[]) {
    selected.value = selected.value.filter((id) => !ids.includes(id))
  }
  return { selected, count, has, toggle, pageChecked, togglePage, clear, drop }
}
