<script setup lang="ts">
import { t } from '@/i18n/messages';
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
    <legend>{{ t('operator.filters.title') }}</legend>
    <label>
      {{ t('operator.filters.lifecycle') }}
      <select :value="model.lifecycleStatus ?? ''" @change="updateLifecycle">
        <option value="">{{ t('operator.filters.all') }}</option>
        <option value="active">{{ t('operator.lifecycle.active') }}</option>
        <option value="superseded">{{ t('operator.lifecycle.superseded') }}</option>
        <option value="revoked">{{ t('operator.lifecycle.revoked') }}</option>
      </select>
    </label>
    <label>
      {{ t('operator.filters.session') }}
      <select :value="model.sessionStatus ?? ''" @change="updateSession">
        <option value="">{{ t('operator.filters.all') }}</option>
        <option value="none">{{ t('operator.filters.noSession') }}</option>
        <option value="online">{{ t('operator.session.online') }}</option>
        <option value="suspect">{{ t('operator.session.suspect') }}</option>
        <option value="offline">{{ t('operator.session.offline') }}</option>
        <option value="revoked">{{ t('operator.lifecycle.revoked') }}</option>
      </select>
    </label>
  </fieldset>
</template>

<style scoped>
.filters { display: flex; flex-wrap: wrap; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.filters legend { padding: 0 0.35rem; font-weight: 700; }
.filters label { display: grid; gap: 0.35rem; color: var(--color-muted-strong); font-size: var(--font-size-md); }
.filters select { min-width: 10rem; padding: 0.5rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); }
</style>
