<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { ClusterSummary } from '@/types/cluster';

defineProps<{
  clusters: ClusterSummary[];
  modelValue: string;
}>();

const emit = defineEmits<{ 'update:modelValue': [value: string] }>();
</script>

<template>
  <label class="target-field">
    {{ t('cluster.target.select') }}
    <!-- Direction C: the UA select paints its own arrow outside the token palette, so the
         control is drawn from tokens and the caret is a CSS shape. The wrapper is a
         plain span because the whole thing has to stay inside the field's <label>. -->
    <span class="select-wrap">
      <select :value="modelValue" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)">
        <option value="">{{ t('cluster.target.placeholder') }}</option>
        <option v-for="cluster in clusters" :key="cluster.id" :value="cluster.id" :disabled="!cluster.enabled">
          {{ cluster.name }}{{ cluster.enabled ? '' : ' — disabled' }}
        </option>
      </select>
      <span class="select-wrap__caret" aria-hidden="true" />
    </span>
  </label>
</template>

<style scoped>
.target-field { display: grid; gap: 0.5rem; font-weight: 600; }
.select-wrap { position: relative; display: block; }
select {
  width: 100%;
  padding: 7px 30px 7px 11px;
  appearance: none;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--color-bg);
  color: var(--color-text);
  font: inherit;
}
.select-wrap__caret {
  position: absolute;
  top: 50%;
  right: 12px;
  width: 6px;
  height: 6px;
  border-right: 1px solid var(--color-muted);
  border-bottom: 1px solid var(--color-muted);
  transform: translateY(-70%) rotate(45deg);
  pointer-events: none;
}
</style>
