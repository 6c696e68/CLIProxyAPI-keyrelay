<script setup lang="ts">
import { ref } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppCheckbox from '@/components/ui/AppCheckbox.vue'
import AppField from '@/components/ui/AppField.vue'
import AppPasswordInput from '@/components/ui/AppPasswordInput.vue'

const props = defineProps<{ loading: boolean; error: string; initial?: string }>()
const emit = defineEmits<{ submit: [value: string, remember: boolean] }>()
const value = ref(props.initial || '')
const remember = ref(true)
</script>

<template>
  <main class="gate">
    <form class="gate-card panel" @submit.prevent="emit('submit', value, remember)">
      <p class="page-kicker">CLI Proxy API</p>
      <h1 class="page-title">API keys</h1>
      <p class="page-copy">Use your panel management key to issue and manage customer API keys.</p>
      <div class="mt-6 grid gap-4">
        <AppField label="Management key" :error="error">
          <AppPasswordInput v-model="value" placeholder="Management key" />
        </AppField>
        <AppCheckbox :checked="remember" label="Remember for this session" @toggle="remember = !remember" />
        <AppButton kind="primary" type="submit" block :disabled="loading">{{ loading ? 'Checking key' : 'Open workspace' }}</AppButton>
      </div>
    </form>
  </main>
</template>
