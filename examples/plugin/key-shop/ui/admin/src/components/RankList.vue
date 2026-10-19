<script setup lang="ts">
import { formatNumber } from '@/lib/format'
import type { UsageRank } from '@/types'

defineProps<{ title: string; rows: UsageRank[] }>()
</script>

<template>
  <section class="panel">
    <h2 class="section-title">{{ title }}</h2>
    <div v-if="rows.length" class="mt-2">
      <div v-for="row in rows" :key="row.id" class="rank-row">
        <div class="min-w-0">
          <strong class="block truncate text-sm">{{ row.label || row.id }}</strong>
          <span v-if="row.detail" class="sub block truncate">{{ row.detail }}</span>
        </div>
        <span class="num text-xs">{{ formatNumber(row.requests) }}</span>
        <span class="num text-xs text-[var(--ink-soft)]">{{ formatNumber(row.billed) }}</span>
      </div>
    </div>
    <p v-else class="section-copy">No data in this range.</p>
  </section>
</template>
