<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import {
  actionOptions,
  resourceTypeOptions,
  statusOptions,
  systemOrganizationId,
  type AuditFilters,
} from '@/stores/audit';

const model = defineModel<AuditFilters>({ required: true });
// TASK-339: the organization scope selector is rendered for platform_admin only;
// for every other role the scope stays the active organization.
const scope = defineModel<string>('scope', { default: '' });
defineProps<{
  platformAdmin?: boolean;
  organizations?: ReadonlyArray<{ id: string; name: string }>;
  // TASK-344: the policy window release-auth applies to this caller (31 days, or
  // 366 for platform_admin). Shown as a hint because a min/max on one date input
  // cannot express a span constraint.
  maxRangeDays?: number;
}>();
const emit = defineEmits<{
  submit: [];
  reset: [];
}>();
</script>

<template>
  <form class="audit-filters" :aria-label="t('audit.filters.title')" @submit.prevent="emit('submit')">
    <label v-if="platformAdmin" class="audit-filters__field">
      <span>{{ t('audit.filters.organization') }}</span>
      <select v-model="scope" name="organization">
        <option value="">{{ t('audit.filters.allOrganizations') }}</option>
        <option v-for="organization in organizations ?? []" :key="organization.id" :value="organization.id">
          {{ organization.name }}
        </option>
        <option :value="systemOrganizationId">{{ t('audit.filters.systemScope') }}</option>
      </select>
      <small>{{ t('audit.filters.systemScopeHint') }}</small>
    </label>
    <label class="audit-filters__field">
      <span>{{ t('audit.filters.actor') }}</span>
      <input v-model.trim="model.actor" name="actor" autocomplete="off" />
      <small>{{ t('audit.filters.actorPrivate') }}</small>
    </label>
    <label class="audit-filters__field">
      <span>{{ t('audit.filters.resourceType') }}</span>
      <select v-model="model.resourceType" name="resource">
        <option value="">{{ t('audit.filters.allResources') }}</option>
        <option v-for="resource in resourceTypeOptions" :key="resource" :value="resource">{{ resource }}</option>
      </select>
    </label>
    <label class="audit-filters__field">
      <span>{{ t('audit.filters.resourceId') }}</span>
      <input v-model.trim="model.resourceId" name="resource_id" autocomplete="off" />
    </label>
    <label class="audit-filters__field">
      <span>{{ t('audit.filters.action') }}</span>
      <select v-model="model.action" name="action">
        <option value="">{{ t('audit.filters.allActions') }}</option>
        <option v-for="action in actionOptions" :key="action" :value="action">{{ action }}</option>
      </select>
    </label>
    <label class="audit-filters__field">
      <span>{{ t('audit.filters.status') }}</span>
      <select v-model="model.status" name="status">
        <option value="">{{ t('audit.filters.allStatuses') }}</option>
        <option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel('audit', status) }}</option>
      </select>
    </label>
    <label class="audit-filters__field">
      <span>{{ t('audit.filters.from') }}</span>
      <input v-model="model.from" name="from" type="datetime-local" />
    </label>
    <label class="audit-filters__field">
      <span>{{ t('audit.filters.to') }}</span>
      <input v-model="model.to" name="to" type="datetime-local" />
    </label>
    <p v-if="maxRangeDays" class="audit-filters__hint">
      {{ t('audit.filters.windowHint', { days: maxRangeDays }) }}
    </p>
    <div class="audit-filters__actions">
      <button type="submit">{{ t('audit.filters.search') }}</button>
      <button type="button" class="audit-button audit-button--secondary" @click="emit('reset')">{{ t('audit.filters.reset') }}</button>
    </div>
  </form>
</template>

<style scoped>
.audit-filters {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
  gap: 1rem;
  padding: 1rem;
  border: 1px solid var(--color-border);
  border-radius: 0.75rem;
  background: var(--color-surface);
}

.audit-filters__field {
  display: grid;
  gap: 0.35rem;
  color: var(--color-text-secondary);
  font-size: var(--font-size-sm);
  font-weight: 600;
}

.audit-filters__field input,
.audit-filters__field select {
  min-width: 0;
  padding: 0.55rem 0.65rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 0.375rem;
  background: var(--color-surface);
  font: inherit;
}


.audit-filters__field small {
  color: var(--color-muted);
  font-weight: 400;
}

/* The allowed span applies to the pair, not to one input, so the hint spans the
   whole grid row instead of sitting under a single date field. */
.audit-filters__hint {
  grid-column: 1 / -1;
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
  font-weight: 400;
}

.audit-filters__actions {
  display: flex;
  align-items: end;
  gap: 0.5rem;
}

.audit-filters__actions button {
  padding: 0.55rem 0.85rem;
  border: 1px solid var(--color-primary-hover);
  border-radius: 0.375rem;
  background: var(--color-primary);
  color: var(--color-on-accent);
  cursor: pointer;
  font: inherit;
  font-weight: 600;
}

.audit-filters__actions .audit-button--secondary {
  border-color: var(--color-border-strong);
  background: var(--color-surface);
  color: var(--color-text-secondary);
}
</style>
