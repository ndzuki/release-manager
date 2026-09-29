<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, reactive, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
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
        <p class="audit-page__eyebrow">{{ t('audit.page.title') }}</p>
        <h1>{{ t('audit.page.subtitle') }}</h1>
        <p>{{ t('audit.page.description') }} {{ auth.activeOrganization?.name ?? t('shell.organization.choose') }}.</p>
      </div>
      <button type="button" :disabled="!canQuery || audit.exporting" @click="createExport">
        {{ audit.exporting ? t('audit.page.creatingExport') : t('audit.page.export') }}
      </button>
    </header>


    <AuditFilters v-model="form" @submit="submit" @reset="reset" />
    <AuditExportPanel :tasks="audit.exportTasks" />

    <!-- One chain, so a failed request can never be rendered as an empty
         result: the previous shape chained LoadingState/EmptyState to each
         other and left ErrorState outside the chain, which printed
         "Audit request failed" and "No audit events" at the same time (B5). -->
    <ForbiddenState
      v-if="audit.error && audit.error.reason === 'permission_denied'"
      :message="audit.error.message"
    />
    <ErrorState
      v-else-if="audit.error && audit.events.length === 0"
      :title="audit.error.reason === 'range_too_large' ? t('audit.page.narrowRange') : t('audit.page.failedTitle')"
      :message="audit.error.message"
      :action-label="t('action.retry')"
      @action="submit"
    />
    <LoadingState v-else-if="audit.loading && audit.events.length === 0" :message="t('audit.page.loading')" />
    <EmptyState
      v-else-if="!audit.loading && audit.events.length === 0"
      :title="t('audit.page.empty')"
      :message="t('audit.page.emptyHint')"
    />
    <template v-else>
      <!-- AC-059-08: a failed refresh keeps the loaded page, so the failure is
           reported inline instead of replacing the table. -->
      <div v-if="audit.error" class="notice notice--warning" role="alert">
        {{ audit.error.message }}
        <button type="button" @click="submit">{{ t('action.retry') }}</button>
      </div>
      <AuditEventTable
        :events="audit.events"
        :total-size="audit.totalSize"
        :loading="audit.loading"
        :has-previous="audit.hasPrevious"
        :has-more="audit.hasMore"
        @select="audit.selectEvent"
        @previous="audit.query(organizationId, 'previous')"
        @next="audit.query(organizationId, 'next')"
      />
    </template>
    <AuditEventDetail v-if="audit.selectedEvent" :event="audit.selectedEvent" @close="audit.selectEvent(null)" />
  </section>
</template>

<style scoped>
.audit-page {
  display: grid;
  gap: 1.5rem;
}

/* Inline partial-failure notice: the loaded page stays, the failure is
   reported next to it (same pattern as ReleaseInventoryPage). */
.notice {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.8rem 1rem;
  border: 1px solid var(--color-info-border);
  border-radius: 0.5rem;
  background: var(--color-info-soft);
  color: var(--color-info-ink);
}

.notice--warning {
  border-color: var(--color-warning-border);
  background: var(--color-warning-soft);
  color: var(--color-warning-ink);
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
  color: var(--color-muted);
  font-size: var(--font-size-xs);
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.audit-page__heading button {
  padding: 0.55rem 0.85rem;
  border: 1px solid var(--color-primary-hover);
  border-radius: 0.375rem;
  background: var(--color-primary);
  color: var(--color-on-accent);
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
