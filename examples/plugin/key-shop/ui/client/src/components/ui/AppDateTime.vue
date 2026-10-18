<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { addMonths, expiryPresets, monthCells, monthLabel, parseLocalInput, prettyDateTime, sameDay, toLocalInput, withTime } from '@/lib/datetime'
import AppButton from './AppButton.vue'

const props = defineProps<{ modelValue: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
const open = ref(false)
const view = ref(new Date())
const selected = computed(() => parseLocalInput(props.modelValue))
const hours = computed(() => selected.value?.getHours() ?? 9)
const minutes = computed(() => selected.value?.getMinutes() ?? 0)
const cells = computed(() => monthCells(view.value))
const weekdays = ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa']

watch(() => props.modelValue, (value) => {
  const date = parseLocalInput(value)
  if (date) view.value = new Date(date.getFullYear(), date.getMonth(), 1)
})

function pickDay(day: Date) {
  emit('update:modelValue', withTime(day, hours.value, minutes.value))
}
function setHour(value: number) {
  const base = selected.value ?? new Date()
  emit('update:modelValue', withTime(base, value, minutes.value))
}
function setMinute(value: number) {
  const base = selected.value ?? new Date()
  emit('update:modelValue', withTime(base, hours.value, value))
}
function preset(seconds: number) {
  emit('update:modelValue', toLocalInput(new Date(Date.now() + seconds * 1000)))
  open.value = true
}
</script>

<template>
  <div class="grid gap-2">
    <AppButton type="button" :disabled="disabled" :aria-expanded="open" @click="open = !open">
      {{ disabled ? 'Expiry is off' : prettyDateTime(modelValue) }}
    </AppButton>
    <div v-if="open && !disabled" class="calendar">
      <div class="flex items-center justify-between">
        <AppButton small aria-label="Previous month" @click="view = addMonths(view, -1)">Prev</AppButton>
        <strong class="text-sm">{{ monthLabel(view) }}</strong>
        <AppButton small aria-label="Next month" @click="view = addMonths(view, 1)">Next</AppButton>
      </div>
      <div class="cal-grid" aria-hidden="true">
        <span v-for="day in weekdays" :key="day" class="cal-dow">{{ day }}</span>
      </div>
      <div class="cal-grid" role="grid" aria-label="Choose a day">
        <span v-for="(day, index) in cells" :key="index">
          <button v-if="day" type="button" :aria-pressed="sameDay(day, selected)" :aria-label="day.toDateString()" @click="pickDay(day)">{{ day.getDate() }}</button>
        </span>
      </div>
      <div class="grid grid-cols-2 gap-2">
        <label class="field">
          <span class="field-label">Hour</span>
          <input class="control num" type="text" inputmode="numeric" :value="String(hours).padStart(2, '0')" @change="setHour(Math.min(23, Math.max(0, Number(($event.target as HTMLInputElement).value) || 0)))" />
        </label>
        <label class="field">
          <span class="field-label">Minute</span>
          <input class="control num" type="text" inputmode="numeric" :value="String(minutes).padStart(2, '0')" @change="setMinute(Math.min(59, Math.max(0, Number(($event.target as HTMLInputElement).value) || 0)))" />
        </label>
      </div>
    </div>
    <div class="flex flex-wrap gap-1.5">
      <button v-for="[label, seconds] in expiryPresets" :key="label" type="button" class="preset" :disabled="disabled" @click="preset(seconds)">{{ label }}</button>
    </div>
  </div>
</template>
