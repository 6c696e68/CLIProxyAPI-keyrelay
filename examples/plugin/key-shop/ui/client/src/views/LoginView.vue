<script setup lang="ts">
import { ref } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppCheckbox from '@/components/ui/AppCheckbox.vue'
import AppPasswordInput from '@/components/ui/AppPasswordInput.vue'
import Icon from '@/components/ui/Icon.vue'

defineProps<{ loading: boolean; error: string }>()
const emit = defineEmits<{ submit: [string, boolean] }>()
const value = ref('')
const remember = ref(false)
</script>

<template>
  <main id="content" class="gate">
    <form class="gate-card panel" novalidate @submit.prevent="emit('submit', value.trim(), remember)">
      <div class="brand-mark gate-mark" aria-hidden="true"><Icon name="key" :size="20" /></div>
      <h1 class="gate-title">Key workspace</h1>
      <p class="gate-copy">Enter your customer key to see quota, models, and recent requests.</p>

      <div class="mt-6 grid gap-4">
        <label class="field">
          <span class="field-label">API key</span>
          <AppPasswordInput v-model="value" placeholder="ck_…" :invalid="Boolean(error)" />
          <span v-if="error" class="field-error" role="alert"><Icon name="alert" :size="14" />{{ error }}</span>
        </label>
        <AppCheckbox :checked="remember" label="Remember for this session" @toggle="remember = !remember" />
        <AppButton kind="primary" type="submit" block :disabled="loading || !value.trim()">
          <Icon v-if="loading" name="refresh" class="spin" />
          {{ loading ? 'Opening workspace' : 'Open workspace' }}
        </AppButton>
      </div>
      <p class="gate-foot"><Icon name="shield" :size="14" />Your key is only kept in this browser tab.</p>
    </form>
  </main>
</template>
