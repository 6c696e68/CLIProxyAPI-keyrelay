<script setup lang="ts">
withDefaults(defineProps<{ kind?: 'primary' | 'secondary' | 'ghost' | 'danger'; small?: boolean; block?: boolean; type?: 'button' | 'submit' }>(), {
  kind: 'secondary',
  type: 'button',
})
</script>

<template>
  <button :type="type" class="btn" :class="[`btn-${kind}`, { 'btn-sm': small, 'btn-block': block }]">
    <slot />
  </button>
</template>
