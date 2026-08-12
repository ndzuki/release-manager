<script setup lang="ts">
import type { OperatorLifecycleStatus, OperatorListFilters, OperatorSessionStatus } from '@/types/operator';

const model = defineModel<OperatorListFilters>({ required: true });

function updateLifecycle(event: Event): void {
  const value = (event.target as HTMLSelectElement).value as Exclude<OperatorLifecycleStatus, 'unknown'> | '';
  model.value = { ...model.value, lifecycleStatus: value || null };
}

function updateSession(event: Event): void {
  const value = (event.target as HTMLSelectElement).value as Exclude<OperatorSessionStatus, null | 'unknown'> | 'none' | '';
  model.value = { ...model.value, sessionStatus: value || null };
}
</script>

<template>
  <fieldset class="filters">
    <legend>Filter operator history</legend>
    <label>
      Lifecycle
      <select :value="model.lifecycleStatus ?? ''" @change="updateLifecycle">
        <option value="">All</option>
        <option value="active">Active</option>
        <option value="superseded">Superseded</option>
        <option value="revoked">Revoked</option>
      </select>
    </label>
    <label>
      Session
      <select :value="model.sessionStatus ?? ''" @change="updateSession">
        <option value="">All</option>
        <option value="none">No session</option>
        <option value="online">Online</option>
        <option value="suspect">Suspect</option>
        <option value="offline">Offline</option>
        <option value="revoked">Revoked</option>
      </select>
    </label>
  </fieldset>
</template>

<style scoped>
.filters { display: flex; flex-wrap: wrap; gap: 1rem; padding: 1rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.filters legend { padding: 0 0.35rem; font-weight: 700; }
.filters label { display: grid; gap: 0.35rem; color: #475569; font-size: 0.875rem; }
.filters select { min-width: 10rem; padding: 0.5rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; }
</style>
