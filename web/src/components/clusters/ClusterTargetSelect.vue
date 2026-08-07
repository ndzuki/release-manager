<script setup lang="ts">
import type { ClusterSummary } from '@/types/cluster';

defineProps<{
  clusters: ClusterSummary[];
  modelValue: string;
}>();

const emit = defineEmits<{ 'update:modelValue': [value: string] }>();
</script>

<template>
  <label class="target-field">
    Target cluster
    <select :value="modelValue" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)">
      <option value="">Select a cluster</option>
      <option v-for="cluster in clusters" :key="cluster.id" :value="cluster.id" :disabled="!cluster.enabled">
        {{ cluster.name }}{{ cluster.enabled ? '' : ' — disabled' }}
      </option>
    </select>
  </label>
</template>

<style scoped>
.target-field { display: grid; gap: 0.5rem; font-weight: 600; }
select { padding: 0.625rem; border: 1px solid #cbd5e1; border-radius: 0.375rem; font: inherit; }
</style>
