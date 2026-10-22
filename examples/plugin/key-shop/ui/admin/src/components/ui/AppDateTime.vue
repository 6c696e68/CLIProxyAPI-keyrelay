<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { expiryPresets, toLocalInput } from '@/lib/datetime'
import { prettyDateTime } from '@/lib/datetime'
import Icon from './Icon.vue'

const props = defineProps<{ modelValue: string; disabled?: boolean; label?: string }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
const uid = useId()
const open = ref(false)
const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const selected = ref(new Date())
const month = ref(new Date())
const focusedDay = ref('')
const hours = ref('09')
const minutes = ref('00')
const place = ref({ top: 8, left: 8, maxHeight: 560 })
const weekdays = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
const pad = (value: number) => String(value).padStart(2, '0')
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone

function keyOf(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}
function read(value: string): Date {
  const parsed = value ? new Date(value) : new Date()
  return Number.isNaN(parsed.valueOf()) ? new Date() : parsed
}
const days = computed(() => {
  const first = new Date(month.value.getFullYear(), month.value.getMonth(), 1)
  const offset = (first.getDay() + 6) % 7
  return Array.from({ length: 42 }, (_, index) => {
    const date = new Date(first.getFullYear(), first.getMonth(), index - offset + 1)
    return { date, key: keyOf(date), outside: date.getMonth() !== first.getMonth() }
  })
})
const monthName = computed(() => month.value.toLocaleDateString('en-US', { month: 'long', year: 'numeric' }))
const draft = computed(() => `${keyOf(selected.value)}T${hours.value.padStart(2, '0')}:${minutes.value.padStart(2, '0')}`)
const valid = computed(() => {
  const hour = Number(hours.value)
  const minute = Number(minutes.value)
  return /^\d{1,2}$/.test(hours.value) && /^\d{1,2}$/.test(minutes.value) && hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59
})

function anchorBox() {
  return trigger.value?.getBoundingClientRect() ?? null
}
function anchorVisible(rect: DOMRect) {
  const modal = trigger.value?.closest('.modal')?.getBoundingClientRect()
  const top = modal ? Math.max(0, modal.top) : 0
  const bottom = modal ? Math.min(window.innerHeight, modal.bottom) : window.innerHeight
  const left = modal ? Math.max(0, modal.left) : 0
  const right = modal ? Math.min(window.innerWidth, modal.right) : window.innerWidth
  return rect.bottom > top + 4 && rect.top < bottom - 4 && rect.right > left && rect.left < right
}
function position() {
  const rect = anchorBox()
  if (!rect) return
  if (!anchorVisible(rect)) {
    open.value = false
    return
  }
  const margin = 8
  const gap = 6
  const width = Math.min(336, window.innerWidth - margin * 2)
  const height = panel.value?.scrollHeight || panel.value?.offsetHeight || 520
  const spaceBelow = window.innerHeight - rect.bottom - margin
  const spaceAbove = rect.top - margin
  let top = rect.bottom + gap
  if (spaceBelow < height + gap && spaceAbove >= spaceBelow) top = rect.top - gap - height
  top = Math.max(margin, Math.min(top, window.innerHeight - margin - height))
  const left = Math.max(margin, Math.min(rect.left, window.innerWidth - margin - width))
  place.value = { top, left, maxHeight: 0 }
}
function track() {
  if (!open.value) return
  position()
}
function show() {
  if (props.disabled) return
  const date = read(props.modelValue)
  selected.value = date
  month.value = new Date(date.getFullYear(), date.getMonth(), 1)
  focusedDay.value = keyOf(date)
  hours.value = pad(date.getHours())
  minutes.value = pad(date.getMinutes())
  open.value = true
}
function choose(date: Date) {
  selected.value = date
  focusedDay.value = keyOf(date)
  if (date.getMonth() !== month.value.getMonth()) month.value = new Date(date.getFullYear(), date.getMonth(), 1)
}
function apply() {
  if (props.disabled || !valid.value) return
  const [year, monthPart, day] = keyOf(selected.value).split('-').map(Number)
  const next = new Date(year, (monthPart ?? 1) - 1, day, Number(hours.value), Number(minutes.value))
  emit('update:modelValue', toLocalInput(next))
  open.value = false
}
function clear() {
  emit('update:modelValue', '')
  open.value = false
}
function today() {
  const date = new Date()
  choose(date)
  hours.value = pad(date.getHours())
  minutes.value = pad(date.getMinutes())
}
function preset(seconds: number) {
  emit('update:modelValue', toLocalInput(new Date(Date.now() + seconds * 1000)))
  open.value = false
}
function outside(event: PointerEvent) {
  const target = event.target as Node
  if (root.value?.contains(target) || panel.value?.contains(target)) return
  open.value = false
}
watch(open, async (value) => {
  if (!value) {
    document.removeEventListener('pointerdown', outside)
    window.removeEventListener('scroll', track, true)
    window.removeEventListener('resize', track)
    return
  }
  document.addEventListener('pointerdown', outside)
  window.addEventListener('scroll', track, true)
  window.addEventListener('resize', track)
  await nextTick()
  position()
  requestAnimationFrame(position)
  panel.value?.querySelector<HTMLButtonElement>(`[data-day="${focusedDay.value}"]`)?.focus()
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', outside)
  window.removeEventListener('scroll', track, true)
  window.removeEventListener('resize', track)
})
</script>

<template>
  <div ref="root" class="datetime">
    <button ref="trigger" type="button" class="control datetime-trigger" :disabled="disabled" aria-haspopup="dialog" :aria-expanded="open" :aria-controls="open ? `${uid}-picker` : undefined" :aria-label="`${label || 'Expires at'}: ${disabled ? 'Expiry is off' : prettyDateTime(modelValue)}`" @click="open ? (open = false) : show()">
      <span>{{ disabled ? 'Expiry is off' : prettyDateTime(modelValue) }}</span>
      <Icon name="calendar" />
    </button>
    <div class="datetime-presets">
      <button v-for="[name, seconds] in expiryPresets" :key="name" type="button" class="preset" :disabled="disabled" @click="preset(seconds)">{{ name }}</button>
    </div>
    <Teleport to="body">
      <div v-if="open" :id="`${uid}-picker`" ref="panel" class="datetime-pop" role="dialog" :aria-label="label || 'Choose date and time'" :style="{ top: `${place.top}px`, left: `${place.left}px` }">
        <div class="datetime-head">
          <strong>{{ monthName }}</strong>
          <div class="flex gap-1">
            <button type="button" class="icon-btn btn btn-ghost" aria-label="Previous month" @click="month = new Date(month.getFullYear(), month.getMonth() - 1, 1)"><Icon name="left" /></button>
            <button type="button" class="icon-btn btn btn-ghost" aria-label="Next month" @click="month = new Date(month.getFullYear(), month.getMonth() + 1, 1)"><Icon name="right" /></button>
          </div>
        </div>
        <table class="datetime-cal">
          <thead><tr><th v-for="day in weekdays" :key="day" scope="col">{{ day }}</th></tr></thead>
          <tbody>
            <tr v-for="week in 6" :key="week">
              <td v-for="day in days.slice((week - 1) * 7, week * 7)" :key="day.key">
                <button type="button" class="datetime-day" :class="{ outside: day.outside, selected: day.key === keyOf(selected) }" :data-day="day.key" :aria-pressed="day.key === keyOf(selected)" @click="choose(day.date)">{{ day.date.getDate() }}</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div class="datetime-time">
          <span>Time <small>24 hour</small></span>
          <div class="datetime-clock">
            <input v-model="hours" class="control num clock" maxlength="2" inputmode="numeric" aria-label="Hours" @keydown.enter.prevent="apply" />
            <span aria-hidden="true">:</span>
            <input v-model="minutes" class="control num clock" maxlength="2" inputmode="numeric" aria-label="Minutes" @keydown.enter.prevent="apply" />
          </div>
        </div>
        <p v-if="!valid" class="field-error" role="alert">Enter a valid local time from 00:00 to 23:59.</p>
        <p class="datetime-zone">Local time · {{ timezone }}</p>
        <div class="datetime-foot">
          <button type="button" class="btn btn-ghost btn-sm" @click="clear">Clear</button>
          <button type="button" class="btn btn-ghost btn-sm" @click="today">Today</button>
          <span class="flex-1" />
          <button type="button" class="btn btn-secondary btn-sm" @click="open = false">Cancel</button>
          <button type="button" class="btn btn-primary btn-sm" :disabled="!valid" @click="apply">Apply</button>
        </div>
      </div>
    </Teleport>
  </div>
</template>
