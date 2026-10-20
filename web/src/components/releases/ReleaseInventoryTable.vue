<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { MessageKey } from '@/i18n/messages';
import CopyableHash from '@/components/common/CopyableHash.vue';
import StatusTag from '@/components/common/StatusTag.vue';
import type { ReleaseStatus, ReleaseSummary } from '@/stores/releaseInventory';
import { formatTimestamp } from '@/utils/formatTimestamp';

const props = defineProps<{
  releases: ReleaseSummary[];
  canCreateOperation?: boolean;
  canEmergency?: boolean;
  canConvergence?: boolean;
  /** Rollback needs release/write plus deployer/admin (server-authoritative). */
  canRollback?: boolean;
  customerId?: string;
  clusterId?: string;
  customerName?: string;
  clusterName?: string;
}>();

const emit = defineEmits<{
  /** The owner owns the dialog; ROLLBACK is queued as a new operation. */
  rollback: [release: ReleaseSummary];
}>();

/** A rollback needs a bound definition and somewhere to go back to. */
function canRollbackRow(release: ReleaseSummary): boolean {
  return Boolean(props.canRollback && release.releaseDefinitionId && release.revision > 1);
}

const valuesRevisionEnabled = import.meta.env.VITE_ENABLE_VALUES_REVISION !== 'false';

/*
 * Direction-C status pill (TASK-394). The keys are the store's own vocabulary, which is
 * mapped from the wire enum in web/src/stores/releaseInventory.ts (`statusFromProto`) and
 * ultimately from api/proto/orchestrator/v1/orchestrator.proto lines 994-1003:
 *
 *   RELEASE_INVENTORY_STATUS_ACTIVE (1)       → active      → ok
 *   RELEASE_INVENTORY_STATUS_MISSING (2)      → missing     → er
 *   RELEASE_INVENTORY_STATUS_OUT_OF_SYNC (3)  → out_of_sync → wn
 *   RELEASE_INVENTORY_STATUS_UNSPECIFIED (0)  → unknown     → neutral
 *
 * UNSPECIFIED is documented as never returned as a status, but proto3 also produces it
 * for a field the server left out (UX-061), so it keeps a neutral pill instead of
 * reading as ACTIVE. Each pill keeps the AC-054-02 tooltip; StatusTag has no tooltip
 * prop, so the caller wraps it in a titled span rather than dropping the explanation.
 */
type TagTone = 'ok' | 'wn' | 'er' | 'neutral';

const STATUS_TAG: Record<ReleaseStatus, { tone: TagTone; labelKey: MessageKey; tooltipKey: MessageKey }> = {
  active: { tone: 'ok', labelKey: 'release.status.active', tooltipKey: 'release.status.tooltip.active' },
  missing: { tone: 'er', labelKey: 'release.status.missing', tooltipKey: 'release.status.tooltip.missing' },
  out_of_sync: { tone: 'wn', labelKey: 'release.status.outOfSync', tooltipKey: 'release.status.tooltip.outOfSync' },
  unknown: { tone: 'neutral', labelKey: 'release.status.unknown', tooltipKey: 'release.status.unknownTooltip' },
};

function statusTag(status: ReleaseStatus): { tone: TagTone; label: string; tooltip: string } {
  const meta = STATUS_TAG[status];
  return { tone: meta.tone, label: t(meta.labelKey), tooltip: t(meta.tooltipKey) };
}

function valuesRoute(release: ReleaseSummary) {
  return {
    name: 'ValuesEditor',
    params: { customerId: props.customerId, clusterId: props.clusterId, releaseId: release.releaseDefinitionId },
    query: { releaseName: release.name },
  };
}

function emergencyRoute(release: ReleaseSummary) {
  return {
    name: 'EmergencyChange',
    params: { customerId: props.customerId, clusterId: props.clusterId, releaseId: release.releaseDefinitionId },
  };
}

function convergenceRoute(release: ReleaseSummary) {
  return {
    name: 'ConvergenceTasks',
    params: { customerId: props.customerId, clusterId: props.clusterId, releaseId: release.releaseDefinitionId },
  };
}

// UX-076 (W17): the table's own fallback word; the formatting itself is shared.
function formatSyncTime(value: string | null): string {
  return formatTimestamp(value, { fallback: '尚未同步' });
}

function chartLabel(release: ReleaseSummary): string {
  return release.chartVersion ? `${release.chart}@${release.chartVersion}` : release.chart;
}
</script>

<template>
  <!-- UX-073 (W15): at narrow widths the table used to be a bare horizontal scroller.
       It is now a labelled, keyboard-scrollable region at every size, and below 40rem
       each row becomes a card (nothing hidden off-screen). The ARIA roles are kept
       defensively: a Chromium 480px AX-tree comparison (2026-10-18) showed the tree
       unchanged without them, but other engines were unverified; they document narrow-card intent.

       TASK-394 (direction C): `.table-wrap` is the shared scroller from base.css
       (`overflow-x:auto` + the load-bearing `min-width:0`); this component adds the frame,
       because `--sf` and `--bg` differ by about 2% and a borderless table reads as a slab. -->
  <div
    class="table-wrap release-table-wrap"
    role="region"
    :aria-label="t('release.table.scrollRegion')"
    tabindex="0"
  >
    <table class="release-table" role="table">
      <thead role="rowgroup">
        <tr role="row">
          <th scope="col" role="columnheader">{{ t('release.table.release') }}</th>
          <th scope="col" role="columnheader">{{ t('release.table.cluster') }}</th>
          <th scope="col" role="columnheader">{{ t('release.table.revision') }}</th>
          <th scope="col" role="columnheader">{{ t('release.table.status') }}</th>
          <th v-if="canEmergency || canConvergence" scope="col" role="columnheader">
            {{ t('release.table.emergency') }}
          </th>
          <th scope="col" role="columnheader">{{ t('release.table.valuesDigest') }}</th>
          <th v-if="canCreateOperation" scope="col" role="columnheader">{{ t('release.table.actions') }}</th>
        </tr>
      </thead>
      <tbody role="rowgroup">
        <tr v-for="release in releases" :key="`${release.namespace}/${release.name}`" role="row">
          <td role="cell">
            <strong class="release-table__name">{{ release.namespace }}/{{ release.name }}</strong>
            <!-- Chart and the last sync time are the row's context, not columns of their
                 own in the direction-C table: keeping them here carries the same data
                 without widening the grid back to the old nine columns. -->
            <span class="release-table__meta">
              <span class="mono">{{ chartLabel(release) }}</span>
              <span class="release-table__dot" aria-hidden="true">·</span>
              <span>{{ formatSyncTime(release.lastSyncAt) }}</span>
            </span>
            <!-- Operation history is a READ entry: it must not sit behind the
                 create-operation capability, or a viewer never reaches it. -->
            <RouterLink
              v-if="release.releaseDefinitionId"
              class="release-table__history"
              :to="{
                name: 'OperationList',
                params: { customerId, clusterId, releaseId: release.releaseDefinitionId },
                query: {
                  customerName,
                  clusterName,
                  releaseName: `${release.namespace}/${release.name}`,
                },
              }"
            >
              操作历史
            </RouterLink>
          </td>
          <td role="cell" :data-label="t('release.table.cluster')" class="release-table__cluster mono">
            {{ clusterName || '—' }}
          </td>
          <!-- The contract publishes the INSTALLED revision only: ReleaseSummary has no
               desired-revision field (api/proto/orchestrator/v1/orchestrator.proto:1005-1021),
               so this column does not invent one. -->
          <td role="cell" :data-label="t('release.table.revision')" class="release-table__revision">
            {{ release.revision }}
          </td>
          <td role="cell" :data-label="t('release.table.status')">
            <span class="release-table__status" :title="statusTag(release.status).tooltip">
              <StatusTag :tone="statusTag(release.status).tone" :label="statusTag(release.status).label" />
            </span>
          </td>
          <td v-if="canEmergency || canConvergence" role="cell" :data-label="t('release.table.emergency')">
            <span v-if="!release.releaseDefinitionId" class="muted" title="未关联 ReleaseDefinition，紧急变更入口不可用">
              未绑定 Definition
            </span>
            <template v-else>
              <span
                v-if="canEmergency && release.emergencyConflict"
                class="release-table__emergency"
                title="存在进行中的标准操作，紧急变更入口已阻断"
              >
                <StatusTag class="release-table__emergency-tag" tone="er" label="紧急变更已阻断" />
              </span>
              <RouterLink
                v-else-if="canEmergency"
                class="release-table__emergency"
                :to="emergencyRoute(release)"
                title="发起紧急变更"
              >
                <StatusTag class="release-table__emergency-tag" tone="er" label="紧急变更" />
              </RouterLink>
              <RouterLink
                v-if="canConvergence && release.pendingConvergenceCount > 0"
                class="release-table__convergence"
                :to="convergenceRoute(release)"
                :title="`${release.pendingConvergenceCount} 个待收敛任务`"
              >
                收敛 {{ release.pendingConvergenceCount }}
              </RouterLink>
            </template>
          </td>
          <td role="cell" :data-label="t('release.table.valuesDigest')">
            <!-- The digest used to render all 64 characters (350.8px, a quarter of the
                 table). CopyableHash keeps the first 8 plus a copy control, and the full
                 value still travels in the title and through the clipboard. -->
            <span class="release-table__digest">
              <CopyableHash v-if="release.valuesDigest" :value="release.valuesDigest" />
              <span v-else class="muted">—</span>
              <!-- A READ entry next to the value it opens, so a viewer without the write
                   capability still reaches the Values surface. -->
              <RouterLink
                v-if="valuesRevisionEnabled && release.releaseDefinitionId"
                class="release-table__values"
                :to="valuesRoute(release)"
              >
                Values
              </RouterLink>
            </span>
          </td>
          <td v-if="canCreateOperation" role="cell" :data-label="t('release.table.actions')">
            <span class="release-table__actions">
              <RouterLink
                v-if="release.releaseDefinitionId"
                class="release-table__operation"
                :to="{
                  name: 'OperationCreate',
                  params: { customerId, clusterId, releaseId: release.releaseDefinitionId },
                  query: {
                    customerName,
                    clusterName,
                    releaseName: `${release.namespace}/${release.name}`,
                    currentRevision: release.revision,
                  },
                }"
              >
                创建操作
              </RouterLink>
              <span v-else class="muted">未绑定 Definition</span>
              <button
                v-if="canRollbackRow(release)"
                type="button"
                class="release-table__rollback"
                :data-testid="`release-rollback-${release.namespace}-${release.name}`"
                @click="emit('rollback', release)"
              >
                回滚
              </button>
            </span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
/* The frame and the surface belong to the component; horizontal containment is the
   global `.table-wrap` rule so every wide table in the console behaves the same. */
.release-table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.release-table-wrap:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

/* Direction-C table: quiet headers on the muted surface, a strong hairline between rows
   (—color-border is ~1.3:1 on dark and invisible), and no wrapping inside a cell.
   `min-width` keeps the 40rem card breakpoint from having to squeeze the grid first.
   The sample's 12.5px header is drawn with the xs step (12px): tokens.test.ts forbids a
   raw font-size anywhere in src/**, and there is no sub-xs step in the scale. */
.release-table {
  width: 100%;
  min-width: 44rem;
  border-collapse: collapse;
}

th,
td {
  padding: 10px 20px;
  text-align: left;
  vertical-align: middle;
  white-space: nowrap;
}

th {
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  letter-spacing: 0.01em;
}

td {
  border-top: 1px solid var(--color-border-strong);
}

/* The row identity stays in view while the table is scrolled sideways (UX-073). */
th:first-child,
td:first-child {
  position: sticky;
  left: 0;
  z-index: 1;
  background: var(--color-surface);
}

th:first-child {
  background: var(--color-surface-muted);
}

td:first-child {
  box-shadow: 1px 0 0 var(--color-border-strong);
}

tbody tr:hover td {
  background: color-mix(in srgb, var(--color-surface-muted) 55%, var(--color-surface));
}

.release-table__name {
  display: block;
  color: var(--color-text);
  font-weight: var(--font-weight-heading);
}

.release-table__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: baseline;
  color: var(--color-muted);
  font-size: var(--font-size-xs);
}

.release-table__dot {
  color: var(--color-subtle);
}

.release-table__history {
  display: inline-block;
  margin-top: 2px;
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  text-decoration: none;
}

.release-table__history:hover {
  color: var(--color-text);
  text-decoration: underline;
}

.release-table__cluster {
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
}

.release-table__revision {
  font-variant-numeric: tabular-nums;
  font-weight: var(--font-weight-medium);
}

.release-table__status {
  display: inline-flex;
}

.release-table__digest {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  /* Wide enough for the truncated value AND its copy button on one line: CopyableHash
     wraps internally when squeezed, and a stacked copy control reads as a bug. The floor
     is still a fraction of the 64-character digest column this replaces, and the table
     stays inside the 1180px content column at 1440. */
  min-width: 140px;
}

.release-table__values {
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  text-decoration: none;
  white-space: nowrap;
}

.release-table__values:hover {
  color: var(--color-text);
  text-decoration: underline;
}

.release-table__emergency {
  text-decoration: none;
}

/* The direction-C sample draws this entry as a small pill rather than a column of red
   text. The step stays on the xs token (the type gate has no smaller one); the smaller
   look comes from the tighter padding and line box, not from an off-scale font size. */
.release-table__emergency-tag {
  padding: 0 7px;
  font-size: var(--font-size-xs);
  line-height: 1.5;
}

.release-table__convergence {
  margin-left: 8px;
  color: var(--color-violet-ink);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  text-decoration: none;
  white-space: nowrap;
}

.release-table__convergence:hover {
  text-decoration: underline;
}

.release-table__actions {
  display: inline-flex;
  gap: 12px;
  align-items: center;
}

.release-table__operation {
  color: var(--color-text);
  font-weight: var(--font-weight-medium);
  text-decoration: none;
}

.release-table__operation:hover {
  text-decoration: underline;
}

.release-table__rollback {
  padding: 3px 9px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-muted-strong);
  font: inherit;
  font-size: var(--font-size-xs);
  cursor: pointer;
}

.release-table__rollback:hover {
  border-color: var(--color-muted);
  color: var(--color-text);
}

.muted {
  color: var(--color-subtle);
  white-space: nowrap;
}

/* UX-073 (W15): below 40rem the columns become labelled cards. The labels come from
   the cells' data-label, so the header row is only visually hidden — it keeps naming
   the columns for assistive technology. */
@media (max-width: 40rem) {
  .release-table-wrap {
    border: 0;
    background: transparent;
  }

  .release-table {
    display: block;
    min-width: 0;
  }

  .release-table thead {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  .release-table tbody {
    display: grid;
    gap: 0.75rem;
  }

  .release-table tr {
    display: grid;
    gap: 0.35rem;
    padding: 0.85rem 1rem;
    border: 1px solid var(--color-border-strong);
    border-radius: var(--radius-lg);
    background: var(--color-surface);
  }

  /* The label is absolutely positioned instead of a grid/flex sibling: a cell's
     content is a mix of text nodes and inline elements (e.g. chart + "@version"),
     and a grid/flex container would split those into separate rows. */
  .release-table td {
    display: block;
    position: relative;
    padding: 0.15rem 0 0.15rem 8rem;
    border: 0;
    white-space: normal;
  }

  .release-table td::before {
    content: attr(data-label);
    position: absolute;
    left: 0;
    top: 0.15rem;
    width: 7.5rem;
    color: var(--color-muted-strong);
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-medium);
    letter-spacing: 0.04em;
  }

  .release-table td:first-child {
    padding: 0 0 0.4rem;
    border-bottom: 1px solid var(--color-border);
    background: transparent;
    box-shadow: none;
  }

  .release-table td:first-child::before {
    content: none;
  }

  .release-table .release-table__meta,
  .release-table .release-table__status,
  .release-table .release-table__digest,
  .release-table .release-table__actions {
    display: flex;
    flex-wrap: wrap;
  }

  .release-table .release-table__operation,
  .release-table .release-table__emergency,
  .release-table .release-table__convergence,
  .release-table .release-table__values,
  .release-table .muted {
    white-space: normal;
  }
}
</style>
