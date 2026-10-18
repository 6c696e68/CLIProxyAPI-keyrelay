<script setup lang="ts">
import { ref } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppCheckbox from '@/components/ui/AppCheckbox.vue'
import AppField from '@/components/ui/AppField.vue'
import AppPasswordInput from '@/components/ui/AppPasswordInput.vue'

defineProps<{ loading: boolean; error: string }>()
const emit = defineEmits<{ submit: [string, boolean] }>()
const value = ref('')
const remember = ref(false)
</script>

<template>
  <main class="gate">
    <form class="gate-card panel" @submit.prevent="emit('submit', value, remember)">
      <p class="page-kicker">Key workspace</p>
      <h1 class="page-title">Your API key, in one place.</h1>
      <p class="page-copy mt-3">Enter the customer key to see access, remaining quota, and recent requests. The key stays in this session only if you ask.</p>
      <div class="mt-6 grid gap-4">
        <AppField label="API key" :error="error">
          <AppPasswordInput v-model="value" placeholder="ck_············" />
        </AppField>
        <AppCheckbox :checked="remember" label="Remember for this session" @toggle="remember = !remember" />
        <AppButton kind="primary" type="submit" block :disabled="loading">{{ loading ? 'Opening workspace' : 'Open workspace' }}</AppButton>
      </div>
    </form>
  </main>
</template>
