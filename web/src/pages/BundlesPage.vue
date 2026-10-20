<script setup lang="ts">
import { t } from '@/i18n/messages';
import { onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import { BUNDLE_STATUSES } from '@/connect/bundle-api';
import { useBundlesStore } from '@/stores/bundles';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * Release bundle catalogue (A8 of the UX plan's missing surfaces).
 *
 * Bundles used to be a hidden dropdown in the operation form; this page makes the
 * content-addressable input visible: digests, chart reference, image bindings and —
 * in the detail panel — the git/pipeline provenance and the signature/SBOM/
 * provenance evidence references.
 *
 * TASK-394 (consistency wave 3): the header is the shared PageHeader (eyebrow "发布"
 * keeps the category the hand-rolled `.eyebrow` carried), the page root no longer adds
 * its own padding on top of the shell's 24px, and the filter/detail cards + the table
 * frame use the direction-C tokens (`--color-border-strong`, `--sf`, `--r`, muted
 * header band). The visible copy moved into the catalog; the English placeholders stay
 * inline because they are examples of the wire field values, not prose.
 */
const store = useBundlesStore();

const definitionFilter = ref('');
const chartFilter = ref('');
const statusFilter = ref('');

onMounted(() => {
  void store.load();
});

function applyFilters(): void {
  // A cursor is bound to the filter that produced it, so this always restarts.
  void store.load({
    releaseDefinitionId: definitionFilter.value.trim(),
    chartNameFilter: chartFilter.value.trim(),
    status: statusFilter.value === '' ? null : Number(statusFilter.value),
  });
}

</script>

<template>
  <section class="bundles">
    <PageHeader
      :title="t('bundles.page.title')"
      :eyebrow="t('bundle.eyebrow')"
      :subtitle="t('bundles.page.subtitle')"
    >
      <template #actions>
        <span class="bundles__total" data-testid="bundles-total">{{ t('bundles.page.total', { count: store.totalSize }) }}</span>
      </template>
    </PageHeader>

    <form class="bundles__filters" @submit.prevent="applyFilters">
      <label>
        {{ t('definitions.releaseDefinitionIdRequired') }}
        <input v-model="definitionFilter" name="releaseDefinitionId" placeholder="definition id" required />
      </label>
      <label>
        {{ t('definitions.chartNameOptional') }}
        <input v-model="chartFilter" name="chartNameFilter" placeholder="chart name" />
      </label>
      <label>
        {{ t('bundles.filters.status') }}
        <select v-model="statusFilter" name="status">
          <option value="">{{ t('bundles.filters.all') }}</option>
          <option v-for="entry in BUNDLE_STATUSES" :key="entry.value" :value="String(entry.value)">{{ entry.label }}</option>
        </select>
      </label>
      <button type="submit" :disabled="store.loading">{{ store.loading ? t('bundles.filters.applying') : t('bundles.filters.apply') }}</button>
    </form>

    <ErrorState
      v-if="store.failure && !store.needsDefinition"
      :title="t('bundles.load.failed')"
      :message="store.failure.message"
      :details="store.failure.details"
      :action-label="t('bundles.load.failedAction')"
      @action="applyFilters"
    />

    <p v-if="store.needsDefinition" class="bundles__prompt" role="status" data-testid="bundles-needs-definition">
      {{ t('bundles.load.prompt') }}
    </p>

    <LoadingState v-else-if="store.loading && store.bundles.length === 0" :message="t('bundles.loading')" />
    <EmptyState
      v-else-if="store.isEmpty && !store.needsDefinition"
      :title="t('bundles.empty.title')"
      :message="t('bundles.empty.message')"
    />

    <div
      v-else-if="store.bundles.length > 0"
      class="table-wrap bundles__table-wrap"
      role="region"
      :aria-label="t('bundles.table.scrollRegion')"
      tabindex="0"
    >
      <table class="bundles__table">
      <thead>
        <tr>
          <th scope="col">{{ t('bundles.table.name') }}</th>
          <th scope="col">{{ t('bundles.table.status') }}</th>
          <th scope="col">{{ t('bundles.table.digest') }}</th>
          <th scope="col">Chart</th>
          <th scope="col">{{ t('bundles.table.images') }}</th>
          <th scope="col">{{ t('bundles.table.receivedAt') }}</th>
          <th scope="col">{{ t('bundles.table.actions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="bundle in store.bundles" :key="bundle.id" :data-testid="`bundle-row-${bundle.id}`">
          <td class="bundles__cell-break">
            <strong>{{ bundle.name }}</strong>
            <br />
            <small>{{ bundle.id }}</small>
          </td>
          <td><span class="bundles__status">{{ bundle.statusLabel }}</span></td>
          <td><code class="bundles__digest">{{ bundle.digest }}</code></td>
          <td class="bundles__cell-break">
            {{ bundle.chartRef }}@{{ bundle.chartVersion }}
            <br />
            <small>{{ bundle.chartDigest }}</small>
          </td>
          <td>{{ t('bundles.row.images', { count: bundle.images.length }) }}</td>
          <td>{{ formatTimestamp(bundle.createdAt) }}</td>
          <td>
            <button type="button" :data-testid="`bundle-detail-${bundle.id}`" @click="store.openDetail(bundle)">{{ t('bundles.row.detail') }}</button>
          </td>
        </tr>
      </tbody>
      </table>
    </div>

    <div v-if="store.hasMore" class="bundles__more">
      <button type="button" :disabled="store.appending" data-testid="bundles-load-more" @click="store.appendNextPage">
        {{ store.appending ? t('bundles.more.loading') : t('bundles.more.label') }}
      </button>
    </div>

    <p v-if="store.appending && store.bundles.length > 0" class="bundles__hint">{{ t('bundles.more.appending') }}</p>

    <section v-if="store.detail || store.detailLoading || store.detailFailure" class="bundles__detail" data-testid="bundle-detail-panel">
      <h2>{{ t('bundles.detail.title') }}</h2>
      <LoadingState v-if="store.detailLoading" :message="t('bundles.detail.loading')" />
      <ErrorState v-else-if="store.detailFailure" :title="t('bundles.detail.loadFailed')" :message="store.detailFailure.message" :details="store.detailFailure.details" />
      <template v-else-if="store.detail">
        <dl class="bundles__facts">
          <dt>{{ t('bundles.detail.name') }}</dt><dd data-testid="detail-name">{{ store.detail.name }}</dd>
          <dt>{{ t('bundles.detail.digest') }}</dt><dd><code>{{ store.detail.digest }}</code></dd>
          <dt>{{ t('bundles.detail.status') }}</dt><dd>{{ store.detail.statusLabel }}</dd>
          <dt>{{ t('bundle.detail.gitCommit') }}</dt><dd data-testid="detail-git">{{ store.detail.gitCommit || '—' }}</dd>
          <dt>{{ t('bundle.detail.pipeline') }}</dt><dd>{{ store.detail.pipelineId || '—' }}</dd>
        </dl>

        <h3>{{ t('bundles.detail.evidence') }}</h3>
        <dl class="bundles__facts" data-testid="detail-evidence">
          <dt>{{ t('bundles.detail.signature') }}</dt><dd>{{ store.detail.signatureDigest || '—' }} <small>{{ store.detail.signatureRef }}</small></dd>
          <dt>{{ t('bundle.detail.sbom') }}</dt><dd>{{ store.detail.sbomDigest || '—' }} <small>{{ store.detail.sbomRef }}</small></dd>
          <dt>{{ t('bundles.detail.provenance') }}</dt><dd>{{ store.detail.provenanceDigest || '—' }} <small>{{ store.detail.provenanceRef }}</small></dd>
        </dl>

        <h3>{{ t('bundles.detail.images', { count: store.detail.images.length }) }}</h3>
        <div
          class="table-wrap bundles__table-wrap"
          role="region"
          :aria-label="t('bundles.images.scrollRegion')"
          tabindex="0"
        >
          <table class="bundles__images">
          <thead>
            <tr>
              <th scope="col">{{ t('bundles.images.ref') }}</th>
              <th scope="col">{{ t('bundles.images.digest') }}</th>
              <th scope="col">{{ t('bundles.images.valuesPath') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="image in store.detail.images" :key="`${image.valuesPath}-${image.ref}`">
              <td><code>{{ image.ref }}</code></td>
              <td><code>{{ image.digest }}</code></td>
              <td>{{ image.valuesPath }}</td>
            </tr>
          </tbody>
          </table>
        </div>

        <button type="button" @click="store.closeDetail()">{{ t('bundles.detail.close') }}</button>
      </template>
    </section>
  </section>
</template>

<style scoped>
.bundles {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
}

.bundles__total {
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

/* Direction-C card: the surface plus a strong border, because `--color-border` is a
   hairline that measures ~1.3:1 on the dark background and leaves the card frameless. */
.bundles__filters {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.bundles__filters label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.bundles__filters input,
.bundles__filters select {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.bundles__prompt {
  margin: 0;
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--color-surface-muted);
  color: var(--color-text);
}

/* Direction-C table frame: the `.table-wrap` base class supplies the horizontal
   containment (overflow-x/min-width/max-width) so a wide table scrolls inside its own
   box instead of widening the document. */
.bundles__table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.bundles__table,
.bundles__images {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

/* At 375 a 7-column table at `width:100%` squeezes the digest column to one glyph per
   line. The reference tables state a floor instead (ReleaseInventoryTable: 44rem) and
   let `.table-wrap` scroll; 62rem keeps every column readable and is below the 1180px
   shell at desktop, so 1440 still renders the table at its full width. */
.bundles__table {
  min-width: 62rem;
}

/* Quiet header band: the old table had no `th` rule at all, so the header row relied on
   the UA's bold weight and had no separation from the body. */
.bundles__table th,
.bundles__images th {
  padding: 10px 20px;
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 600;
  text-align: left;
  /* A header that wraps turns "镜像绑定" into one glyph per line at 375px; the table
     scrolls inside `.table-wrap` instead, which is what the wrapper exists for. */
  white-space: nowrap;
}

.bundles__table td,
.bundles__images td {
  padding: 10px 20px;
  border-top: 1px solid var(--color-border-strong);
  text-align: left;
}

/* The name and chart cells hold machine values with no break opportunities; `anywhere`
   (not `break-all`) lets them wrap when narrow while still preferring the spaces and
   slashes that are already there, and it lowers their min-content width so the table
   fits the 1180px shell at 1440. */
.bundles__cell-break,
.bundles__digest {
  white-space: normal;
  overflow-wrap: anywhere;
}

.bundles__table small,
.bundles__hint {
  color: var(--color-muted);
}


.bundles__status {
  display: inline-block;
  /* A status chip that wraps one glyph per line is unreadable; the table scrolls. */
  white-space: nowrap;
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.bundles__table button,
.bundles__filters button,
.bundles__more button,
.bundles__detail button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
  white-space: nowrap;
}

.bundles__more {
  display: flex;
  justify-content: center;
}

.bundles__detail {
  display: grid;
  gap: var(--space-3);
  min-width: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.bundles__detail h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.bundles__detail h3 {
  margin: var(--space-2) 0 0;
  font-size: var(--font-size-md);
}

.bundles__facts {
  display: grid;
  gap: var(--space-1) var(--space-3);
  /* `minmax(0, 1fr)` instead of `1fr`: a digest has no spaces, so without the zero
     minimum the column's min-content width is the whole hash and the card (and then
     the page) grows to 744px at a 375px viewport. */
  grid-template-columns: max-content minmax(0, 1fr);
  min-width: 0;
  margin: 0;
}

/* Long machine values wrap instead of widening the card; the dd default indent would
   also eat 40px inside a two-column grid. */
.bundles__facts dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
}

.bundles__facts dt {
  color: var(--color-muted);
}
</style>
