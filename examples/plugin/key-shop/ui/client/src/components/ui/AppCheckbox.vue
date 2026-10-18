<script setup lang="ts">
import Icon from './Icon.vue'

defineProps<{ checked: boolean; label: string; disabled?: boolean }>()
defineEmits<{ toggle: [] }>()
</script>

<template>
  <button type="button" class="check" role="checkbox" :aria-checked="checked" :disabled="disabled" @click="$emit('toggle')">
    <span class="check-box"><Icon v-if="checked" name="check" :size="12" /></span>
    <span>{{ label }}</span>
  </button>
</template>
