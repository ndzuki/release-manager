<script setup lang="ts">
import { computed, onMounted, reactive, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import AuditEventDetail from '@/components/audit/AuditEventDetail.vue';
import AuditEventTable from '@/components/audit/AuditEventTable.vue';
import AuditExportPanel from '@/components/audit/AuditExportPanel.vue';
import AuditFilters from '@/components/audit/AuditFilters.vue';
import { useAuthStore } from '@/stores/auth';
import {
  emptyAuditFilters,
  filtersFromQuery,
  filtersToQuery,
  useAuditStore,
  type AuditFilters as AuditFilterState,
} from '@/stores/audit';

const auth = useAuthStore();
const audit = useAuditStore();
const route = useRoute();
const router = useRouter();
let form = reactive<AuditFilterState>(emptyAuditFilters());
let initialized = false;

const organizationId = computed(() => auth.activeOrganization?.id ?? auth.user?.activeOrgId ?? '');
const canQuery = computed(() => organizationId.value.length > 0 && !audit.loading);

function replaceForm(filters: AuditFilterState): void {
  Object.assign(form, { ...filters });
}

async function syncQuery(): Promise<void> {
  await router.replace({ name: 'Audit', query: filtersToQuery(form) });
}

async function submit(): Promise<void> {
  audit.setFilters(form);
  await syncQuery();
  await audit.query(organizationId.value, 'first');
}

async function reset(): Promise<void> {
  replaceForm(emptyAuditFilters());
  audit.setFilters(form);
  audit.clearResults();
  await syncQuery();
  await audit.query(organizationId.value, 'first');
}

async function createExport(): Promise<void> {
  audit.setFilters(form);
  await audit.exportEvents(organizationId.value);
}


onMounted(async () => {
  const filters = filtersFromQuery(route.query);
  replaceForm(filters);
  audit.setFilters(filters);
  initialized = true;
  await audit.query(organizationId.value, 'first');
});

watch(
  form,
  () => {
    if (!initialized) return;
    audit.setFilters(form);
    void syncQuery();
  },
  { deep: true },
);

watch(organizationId, (next, previous) => {
  if (!initialized || !next || next === previous) return;
  audit.clearResults();
  void audit.query(next, 'first');
});
</script>

<template>
  <section class="audit-page">
    <header class="audit-page__heading">
      <div>
        <p class="audit-page__eyebrow">Organization audit trail</p>
        <h1>Audit events</h1>
        <p>Search server-redacted events for {{ auth.activeOrganization?.name ?? 'the active organization' }}.</p>
      </div>
      <button type="button" :disabled="!canQuery || audit.exporting" @click="createExport">
        {{ audit.exporting ? 'Creating export…' : 'Export current query' }}
      </button>
    </header>


    <AuditFilters v-model="form" @submit="submit" @reset="reset" />
    <AuditExportPanel :tasks="audit.exportTasks" />

    <ErrorState
      v-if="audit.error"
      :title="audit.error.reason === 'range_too_large' ? 'Narrow the time range' : 'Audit request failed'"
      :message="audit.error.message"
      action-label="Retry"
      @action="submit"
    />
    <LoadingState v-if="audit.loading && audit.events.length === 0" message="Loading audit events…" />
    <EmptyState
      v-else-if="!audit.loading && audit.events.length === 0"
      title="No audit events"
      message="No events matched the active organization and filters. Audit payloads are never cached in browser storage."
    />
    <AuditEventTable
      v-else
      :events="audit.events"
      :total-size="audit.totalSize"
      :loading="audit.loading"
      :has-previous="audit.hasPrevious"
      :has-more="audit.hasMore"
      @select="audit.selectEvent"
      @previous="audit.query(organizationId, 'previous')"
      @next="audit.query(organizationId, 'next')"
    />
    <AuditEventDetail v-if="audit.selectedEvent" :event="audit.selectedEvent" @close="audit.selectEvent(null)" />
  </section>
</template>

<style scoped>
.audit-page {
  display: grid;
  gap: 1.5rem;
}

.audit-page__heading {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 1rem;
}

.audit-page__heading h1,
.audit-page__heading p {
  margin: 0;
}

.audit-page__eyebrow {
  color: var(--color-muted, #64748b);
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.audit-page__heading button {
  padding: 0.55rem 0.85rem;
  border: 1px solid #1d4ed8;
  border-radius: 0.375rem;
  background: #2563eb;
  color: #fff;
  cursor: pointer;
  font: inherit;
  font-weight: 600;
}

.audit-page__heading button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}


@media (max-width: 48rem) {
  .audit-page__heading {
    flex-direction: column;
  }
}
</style>
