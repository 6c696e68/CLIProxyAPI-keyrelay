<script setup lang="ts">
import { statusLabel, statusTone } from '@/lib/format'

defineProps<{ value: string }>()
</script>

<template>
  <span class="chip" :class="`chip-${statusTone(value)}`" role="status"><i />{{ statusLabel(value) }}</span>
</template>
