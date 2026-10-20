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
  <!-- TASK-394 (direction C): the filter bar is a dense auto-fit grid whose controls
       share one baseline (`align-items: end`). Every field maps to a real filter field
       on audit.v1.AuditQueryFilter — there is no free-text search because the wire
       contract has none. -->
  <form class="audit-filters" :aria-label="t('audit.filters.title')" @submit.prevent="emit('submit')">
    <p class="audit-filters__intro">
      {{ t('audit.page.description') }}
      <span v-if="maxRangeDays" class="audit-filters__hint">{{ t('audit.filters.windowHint', { days: maxRangeDays }) }}</span>
    </p>
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
    <div class="audit-filters__actions">
      <button type="submit" class="audit-filters__submit">{{ t('audit.filters.search') }}</button>
      <button type="button" class="audit-filters__reset" @click="emit('reset')">{{ t('audit.filters.reset') }}</button>
    </div>
  </form>
</template>

<style scoped>
.audit-filters {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  align-items: end;
  gap: 12px;
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

/* The intro is a sentence, not a field: it spans the whole first row so the grid
   stays a control matrix. It also carries the role's allowed query window, which
   needs room the 150px fields do not have. */
.audit-filters__intro {
  grid-column: 1 / -1;
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-sm);
}

.audit-filters__hint {
  display: block;
  margin-top: 4px;
}

.audit-filters__field {
  display: grid;
  gap: 6px;
  min-width: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
  font-weight: 600;
}

.audit-filters__field input,
.audit-filters__field select {
  width: 100%;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--color-border-strong);
  border-radius: 6px;
  background: var(--bg);
  color: var(--tx);
}

/* A control that is focused gets the shared ring from base.css; the border change is
   the second cue for a dark surface where a 2px outline can read as decoration. */
.audit-filters__field input:focus-visible,
.audit-filters__field select:focus-visible {
  border-color: var(--tx);
}

.audit-filters__field small {
  color: var(--mu);
  font-size: var(--font-size-xs);
  font-weight: 400;
}

.audit-filters__actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
  grid-column: 1 / -1;
}

.audit-filters__actions button {
  padding: 8px 14px;
  border-radius: 6px;
  cursor: pointer;
  font-weight: 600;
}

.audit-filters__submit {
  border: 1px solid var(--color-primary-hover);
  background: var(--pr);
  color: var(--on);
}

.audit-filters__reset {
  border: 1px solid var(--color-border-strong);
  background: var(--sf2);
  color: var(--tx);
}
</style>
