<script setup lang="ts">
import AppButton from '@/components/ui/AppButton.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import type { ModelGroup } from '@/types'

defineProps<{ count: number; groups: ModelGroup[]; groupId: string; disabled?: boolean; pending?: boolean }>()
const emit = defineEmits<{
  'update:groupId': [string]
  applyGroup: []
  enable: []
  disable: []
  expiry: []
  limits: []
  delete: []
  clear: []
}>()
</script>

<template>
  <div class="bulk-bar" role="toolbar" aria-label="Bulk key actions">
    <strong class="text-sm">{{ count }} selected</strong>
    <p v-if="count > 500" class="text-xs text-[var(--bad)]">Select at most 500 keys.</p>
    <div class="bulk-group">
      <GroupSelect :model-value="groupId" :groups="groups" label="Bulk group" :disabled="disabled || count > 500" @update:model-value="emit('update:groupId', $event)" />
      <AppButton small :disabled="disabled || !groupId || count > 500" @click="emit('applyGroup')">{{ pending ? 'Working...' : 'Apply group' }}</AppButton>
    </div>
    <AppButton small :disabled="disabled || count > 500" @click="emit('enable')">{{ pending ? 'Working...' : 'Enable' }}</AppButton>
    <AppButton small :disabled="disabled || count > 500" @click="emit('disable')">{{ pending ? 'Working...' : 'Disable' }}</AppButton>
    <AppButton small :disabled="disabled || count > 500" @click="emit('expiry')">Expiry</AppButton>
    <AppButton small :disabled="disabled || count > 500" @click="emit('limits')">Limits</AppButton>
    <AppButton small kind="danger" :disabled="disabled || count > 500" @click="emit('delete')">Delete</AppButton>
    <AppButton small kind="ghost" @click="emit('clear')">Clear</AppButton>
  </div>
</template>
