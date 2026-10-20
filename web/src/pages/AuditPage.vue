<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import Alert from '@/components/common/Alert.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import AuditEventDetail from '@/components/audit/AuditEventDetail.vue';
import AuditEventTable from '@/components/audit/AuditEventTable.vue';
import AuditExportPanel from '@/components/audit/AuditExportPanel.vue';
import AuditFilters from '@/components/audit/AuditFilters.vue';
import { useAuthStore } from '@/stores/auth';
import {
  emptyAuditFilters,
  filtersFromQuery,
  filtersToQuery,
  systemOrganizationId,
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
const isPlatformAdmin = computed(() => auth.isPlatformAdmin);
// TASK-339: platform_admin may widen the read to every organization. An empty
// scope means "all organizations" (the all_organizations flag); a value is a
// concrete organization id, including the static "system" scope. Other roles are
// always pinned to their active organization, so the widening is never offered
// to them.
const organizationScope = ref('');
const queryOrganizationId = computed(() => (isPlatformAdmin.value ? organizationScope.value : organizationId.value));
const queryAllOrganizations = computed(() => isPlatformAdmin.value && organizationScope.value === '');
const scopeLabel = computed(() => {
  if (!isPlatformAdmin.value) return auth.activeOrganization?.name ?? t('shell.organization.choose');
  if (queryAllOrganizations.value) return t('audit.filters.allOrganizations');
  if (organizationScope.value === systemOrganizationId) return t('audit.filters.systemScope');
  return (
    auth.organizations.find((organization) => organization.id === organizationScope.value)?.name ??
    organizationScope.value
  );
});
const canQuery = computed(
  () => (queryAllOrganizations.value || queryOrganizationId.value.length > 0) && !audit.loading,
);

/*
 * TASK-394: ONE state for the result area. The page used to render the error state
 * outside the loading/empty chain, so "request failed" and "no audit events" could
 * appear together (B5). A single derived value makes the states exclusive by
 * construction: every branch below is a `v-if`/`v-else-if` on this one value, so no
 * two of loading / error / forbidden / empty / data can be on screen at once.
 *
 * `hasQueried` closes the first-paint hole: before onMounted fires, `loading` is still
 * false and `events` is empty, which the old chain rendered as "no audit events" for
 * one frame. Until the first request starts, the honest state is loading.
 *
 * "data" also covers a failed REFRESH with the previous page still loaded
 * (AC-059-08): the table stays and the failure is reported inline, which is not the
 * error state — that one replaces the results because there are none.
 */
type AuditViewState = 'loading' | 'forbidden' | 'error' | 'empty' | 'data';
const hasQueried = ref(false);

const viewState = computed<AuditViewState>(() => {
  const failure = audit.error;
  if (failure?.reason === 'permission_denied') return 'forbidden';
  if (failure && audit.events.length === 0) return 'error';
  if (audit.events.length > 0) return 'data';
  if (audit.loading || !hasQueried.value) return 'loading';
  return 'empty';
});

function replaceForm(filters: AuditFilterState): void {
  Object.assign(form, { ...filters });
}

async function syncQuery(): Promise<void> {
  await router.replace({ name: 'Audit', query: filtersToQuery(form) });
}

async function submit(): Promise<void> {
  cancelScheduledUrlSync();
  audit.setFilters(form);
  await syncQuery();
  await audit.query(queryOrganizationId.value, 'first', queryAllOrganizations.value);
}

async function reset(): Promise<void> {
  cancelScheduledUrlSync();
  replaceForm(emptyAuditFilters());
  audit.setFilters(form);
  audit.clearResults();
  await syncQuery();
  await audit.query(queryOrganizationId.value, 'first', queryAllOrganizations.value);
}

async function createExport(): Promise<void> {
  audit.setFilters(form);
  await audit.exportEvents(queryOrganizationId.value, queryAllOrganizations.value);
}

// TASK-339 review: paging is a new request, so it must reuse the scope the
// current page was queried with. Calling query() without the third argument
// pins the request to the active organization, which silently turns an
// "all organizations" or "system" page into a single-organization read.
//
// TASK-375: a settled page change also moves focus to the results region. The
// pager never touches focus by design (Pagination.vue), and the control that was
// activated can become disabled on arrival (last page), which lets the browser
// blur it to <body>. `nextTick` runs first so the newly paged rows are in the DOM
// when focus lands and the region is announced against the page now on screen.
const resultsTable = ref<InstanceType<typeof AuditEventTable> | null>(null);

async function focusResultsAfterPaging(): Promise<void> {
  await nextTick();
  resultsTable.value?.focusResults();
}

async function pagePrevious(): Promise<void> {
  await audit.query(queryOrganizationId.value, 'previous', queryAllOrganizations.value);
  await focusResultsAfterPaging();
}

async function pageNext(): Promise<void> {
  await audit.query(queryOrganizationId.value, 'next', queryAllOrganizations.value);
  await focusResultsAfterPaging();
}

/**
 * UX-011 (W16): the filter form used to call router.replace on EVERY keystroke,
 * so a five-character search term produced five history writes while the user
 * was still typing. The URL is only a shareable mirror of the form: debounce
 * it, and let submit/reset (deliberate actions) write it immediately.
 */
const URL_SYNC_DEBOUNCE_MS = 300;
let urlSyncTimer: ReturnType<typeof setTimeout> | undefined;

function cancelScheduledUrlSync(): void {
  if (urlSyncTimer) clearTimeout(urlSyncTimer);
  urlSyncTimer = undefined;
}

function scheduleUrlSync(): void {
  cancelScheduledUrlSync();
  urlSyncTimer = setTimeout(() => {
    urlSyncTimer = undefined;
    void syncQuery();
  }, URL_SYNC_DEBOUNCE_MS);
}

onBeforeUnmount(cancelScheduledUrlSync);

onMounted(async () => {
  const filters = filtersFromQuery(route.query);
  replaceForm(filters);
  audit.setFilters(filters);
  initialized = true;
  hasQueried.value = true;
  await audit.query(queryOrganizationId.value, 'first', queryAllOrganizations.value);
});

watch(
  form,
  () => {
    if (!initialized) return;
    audit.setFilters(form);
    scheduleUrlSync();
  },
  { deep: true },
);

watch(organizationScope, () => {
  if (!initialized) return;
  audit.clearResults();
  void audit.query(queryOrganizationId.value, 'first', queryAllOrganizations.value);
});

watch(organizationId, (next, previous) => {
  // A platform_admin looking at every organization is not scoped to the active
  // one, so an organization switch is not a scope change for this page.
  if (isPlatformAdmin.value) return;
  if (!initialized || !next || next === previous) return;
  audit.clearResults();
  void audit.query(next, 'first');
});
</script>

<template>
  <section class="audit-page">
    <PageHeader :title="t('audit.page.subtitle')" :subtitle="t('audit.page.headerSubtitle', { scope: scopeLabel })">
      <template #actions>
        <div class="audit-page__actions">
          <button
            type="button"
            class="audit-page__export"
            :disabled="!canQuery || audit.exporting || queryAllOrganizations"
            :title="queryAllOrganizations ? t('audit.page.exportScopeHint') : undefined"
            @click="createExport"
          >
            {{ audit.exporting ? t('audit.page.creatingExport') : t('audit.page.export') }}
          </button>
          <!-- TASK-339 review: an export row carries exactly one organization, so
               the server rejects all_organizations with INVALID_ARGUMENT. Disable
               the action and say why, instead of letting the click fail. -->
          <small v-if="queryAllOrganizations" class="audit-page__export-hint">
            {{ t('audit.page.exportScopeHint') }}
          </small>
        </div>
      </template>
    </PageHeader>

    <AuditFilters
      v-model="form"
      v-model:scope="organizationScope"
      :platform-admin="isPlatformAdmin"
      :organizations="auth.organizations"
      :max-range-days="audit.maxRangeDays"
      @submit="submit"
      @reset="reset"
    />
    <AuditExportPanel :tasks="audit.exportTasks" />

    <h2 class="audit-page__section">{{ t('audit.page.title') }}</h2>

    <div class="audit-page__results">
      <!-- The single chain: exactly one of these branches renders (see viewState). -->
      <ForbiddenState v-if="viewState === 'forbidden'" :message="audit.error?.message" />
      <ErrorState
        v-else-if="viewState === 'error'"
        :title="audit.error?.reason === 'range_too_large' ? t('audit.page.narrowRange') : t('audit.page.failedTitle')"
        :message="audit.error?.message"
        :action-label="t('action.retry')"
        @action="submit"
      />
      <LoadingState v-else-if="viewState === 'loading'" :message="t('audit.page.loading')" />
      <EmptyState
        v-else-if="viewState === 'empty'"
        :title="t('audit.page.empty')"
        :message="t('audit.page.emptyHint')"
      />
      <template v-else>
        <!-- AC-059-08: a failed refresh keeps the loaded page, so the failure is
             reported inline instead of replacing the table. This is a notice ABOUT
             the rows on screen, not the error state (which owns the empty result). -->
        <Alert v-if="audit.error" tone="warning" :title="t('audit.page.failedTitle')" :message="audit.error.message">
          <template #actions>
            <button type="button" class="audit-page__retry" @click="submit">{{ t('action.retry') }}</button>
          </template>
        </Alert>
        <AuditEventTable
          ref="resultsTable"
          :events="audit.events"
          :total-size="audit.totalSize"
          :page="audit.pageNumber"
          :loading="audit.loading"
          :has-previous="audit.hasPrevious"
          :has-more="audit.hasMore"
          @select="audit.selectEvent"
          @previous="pagePrevious"
          @next="pageNext"
        />
      </template>
    </div>

    <AuditEventDetail v-if="audit.selectedEvent" :event="audit.selectedEvent" @close="audit.selectEvent(null)" />
  </section>
</template>

<style scoped>
.audit-page {
  display: grid;
  gap: 16px;
  min-width: 0;
}

.audit-page__section {
  margin: 0;
  color: var(--tx);
}

.audit-page__results {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.audit-page__actions {
  display: grid;
  gap: 6px;
  justify-items: end;
}

.audit-page__export-hint {
  max-width: 16rem;
  color: var(--mu);
  font-size: var(--font-size-sm);
  text-align: right;
}

/* Direction C primary action: the accent is the text colour and the ink is the page
   background, so the button inverts per theme instead of carrying a fixed blue. */
.audit-page__export,
.audit-page__retry {
  padding: 8px 14px;
  border: 1px solid var(--color-primary-hover);
  border-radius: 6px;
  background: var(--pr);
  color: var(--on);
  cursor: pointer;
  font: inherit;
  font-weight: 600;
}

.audit-page__export:disabled {
  border-color: var(--color-disabled-border);
  background: var(--color-disabled-bg);
  color: var(--color-disabled-text);
  cursor: not-allowed;
}

/* The inline refresh failure is a notice next to the table: on a 375px screen the
   alert and its retry button wrap instead of pushing the page wider. */
.audit-page__results :deep(.alert) {
  flex-wrap: wrap;
}

@media (max-width: 48rem) {
  .audit-page__actions {
    justify-items: start;
  }

  .audit-page__export-hint {
    text-align: left;
  }
}
</style>
