<script setup lang="ts">
import { statusLabel, statusTone } from '@/lib/format'

defineProps<{ value: string; interactive?: boolean }>()
defineEmits<{ select: [] }>()
</script>

<template>
  <button v-if="interactive" type="button" class="chip chip-button" :class="`chip-${statusTone(value)}`" @click="$emit('select')">
    <i />{{ statusLabel(value) }}
  </button>
  <span v-else class="chip" :class="`chip-${statusTone(value)}`" role="status"><i />{{ statusLabel(value) }}</span>
</template>
