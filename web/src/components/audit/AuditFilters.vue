<script setup lang="ts">
import { actionOptions, resourceTypeOptions, statusOptions, type AuditFilters } from '@/stores/audit';

const model = defineModel<AuditFilters>({ required: true });
const emit = defineEmits<{
  submit: [];
  reset: [];
}>();
</script>

<template>
  <form class="audit-filters" aria-label="Audit filters" @submit.prevent="emit('submit')">
    <label class="audit-filters__field">
      <span>Actor</span>
      <input v-model.trim="model.actor" name="actor" autocomplete="off" />
      <small>Private filter; never added to the URL.</small>
    </label>
    <label class="audit-filters__field">
      <span>Resource type</span>
      <select v-model="model.resourceType" name="resource">
        <option value="">All resources</option>
        <option v-for="resource in resourceTypeOptions" :key="resource" :value="resource">{{ resource }}</option>
      </select>
    </label>
    <label class="audit-filters__field">
      <span>Resource ID</span>
      <input v-model.trim="model.resourceId" name="resource_id" autocomplete="off" />
    </label>
    <label class="audit-filters__field">
      <span>Action</span>
      <select v-model="model.action" name="action">
        <option value="">All actions</option>
        <option v-for="action in actionOptions" :key="action" :value="action">{{ action }}</option>
      </select>
    </label>
    <label class="audit-filters__field">
      <span>Status</span>
      <select v-model="model.status" name="status">
        <option value="">All statuses</option>
        <option v-for="status in statusOptions" :key="status" :value="status">{{ status }}</option>
      </select>
    </label>
    <label class="audit-filters__field">
      <span>From</span>
      <input v-model="model.from" name="from" type="datetime-local" />
    </label>
    <label class="audit-filters__field">
      <span>To</span>
      <input v-model="model.to" name="to" type="datetime-local" />
    </label>
    <div class="audit-filters__actions">
      <button type="submit">Search</button>
      <button type="button" class="audit-button audit-button--secondary" @click="emit('reset')">Reset</button>
    </div>
  </form>
</template>

<style scoped>
.audit-filters {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
  gap: 1rem;
  padding: 1rem;
  border: 1px solid var(--color-border, #e2e8f0);
  border-radius: 0.75rem;
  background: var(--color-surface, #fff);
}

.audit-filters__field {
  display: grid;
  gap: 0.35rem;
  color: #334155;
  font-size: 0.8rem;
  font-weight: 600;
}

.audit-filters__field input,
.audit-filters__field select {
  min-width: 0;
  padding: 0.55rem 0.65rem;
  border: 1px solid #cbd5e1;
  border-radius: 0.375rem;
  background: #fff;
  font: inherit;
}


.audit-filters__field small {
  color: var(--color-muted, #64748b);
  font-weight: 400;
}

.audit-filters__actions {
  display: flex;
  align-items: end;
  gap: 0.5rem;
}

.audit-filters__actions button {
  padding: 0.55rem 0.85rem;
  border: 1px solid #1d4ed8;
  border-radius: 0.375rem;
  background: #2563eb;
  color: #fff;
  cursor: pointer;
  font: inherit;
  font-weight: 600;
}

.audit-filters__actions .audit-button--secondary {
  border-color: #cbd5e1;
  background: #fff;
  color: #334155;
}
</style>
