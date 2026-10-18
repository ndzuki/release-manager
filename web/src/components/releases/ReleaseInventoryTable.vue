<script setup lang="ts">
import { t } from '@/i18n/messages';
import ReleaseStatusBadge from './ReleaseStatusBadge.vue';
import type { ReleaseSummary } from '@/stores/releaseInventory';
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
</script>

<template>
  <!-- UX-073 (W15): at narrow widths the table used to be a bare horizontal scroller.
       It is now a labelled, keyboard-scrollable region at every size, and below 40rem
       each row becomes a card (nothing hidden off-screen). The ARIA roles are kept
       defensively: a Chromium 480px AX-tree comparison (2026-10-18) showed the tree
       unchanged without them, but other engines were unverified; they document narrow-card intent. -->
  <div
    class="release-table-wrap"
    role="region"
    :aria-label="t('release.table.scrollRegion')"
    tabindex="0"
  >
    <table class="release-table" role="table">
      <thead role="rowgroup">
        <tr role="row">
          <th scope="col" role="columnheader">Release</th>
          <th scope="col" role="columnheader">状态</th>
          <th scope="col" role="columnheader">Chart</th>
          <th scope="col" role="columnheader">Revision</th>
          <th scope="col" role="columnheader">{{ t('release.table.valuesDigest') }}</th>
          <th scope="col" role="columnheader">最近同步</th>
          <th v-if="canEmergency || canConvergence" scope="col" role="columnheader">紧急变更</th>
          <th v-if="canCreateOperation" scope="col" role="columnheader">操作</th>
        </tr>
      </thead>
      <tbody role="rowgroup">
        <tr v-for="release in releases" :key="`${release.namespace}/${release.name}`" role="row">
          <td role="cell">
            <strong>{{ release.namespace }}/{{ release.name }}</strong>
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
          <td role="cell" data-label="状态"><ReleaseStatusBadge :status="release.status" /></td>
          <td role="cell" data-label="Chart">{{ release.chart }}<span v-if="release.chartVersion">@{{ release.chartVersion }}</span></td>
          <td role="cell" data-label="Revision">{{ release.revision }}</td>
          <td role="cell" :data-label="t('release.table.valuesDigest')">
            <RouterLink
              v-if="valuesRevisionEnabled && release.releaseDefinitionId && release.valuesDigest"
              class="values-link"
              :to="valuesRoute(release)"
            >
              {{ release.valuesDigest }}
            </RouterLink>
            <code v-else>{{ release.valuesDigest || '—' }}</code>
          </td>
          <td role="cell" data-label="最近同步">{{ formatSyncTime(release.lastSyncAt) }}</td>
          <td v-if="canEmergency || canConvergence" role="cell" data-label="紧急变更">
            <span v-if="!release.releaseDefinitionId" class="muted" title="未关联 ReleaseDefinition，紧急变更入口不可用">
              未绑定 Definition
            </span>
            <template v-else>
              <span
                v-if="canEmergency && release.emergencyConflict"
                class="emergency-link blocked"
                title="存在进行中的标准操作，紧急变更入口已阻断"
              >
                紧急变更
              </span>
              <RouterLink
                v-else-if="canEmergency"
                class="emergency-link"
                :to="emergencyRoute(release)"
                title="发起紧急变更"
              >
                紧急变更
              </RouterLink>
              <RouterLink
                v-if="canConvergence && release.pendingConvergenceCount > 0"
                class="convergence-link"
                :to="convergenceRoute(release)"
                :title="`${release.pendingConvergenceCount} 个待收敛任务`"
              >
                收敛 {{ release.pendingConvergenceCount }}
              </RouterLink>
            </template>
          </td>
          <td v-if="canCreateOperation" role="cell" data-label="操作">
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
            <span v-else>未绑定 Definition</span>
            <button
              v-if="canRollbackRow(release)"
              type="button"
              class="release-table__rollback"
              :data-testid="`release-rollback-${release.namespace}-${release.name}`"
              @click="emit('rollback', release)"
            >
              回滚
            </button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.release-table-wrap { overflow-x: auto; border: 1px solid var(--color-border); border-radius: 0.65rem; background: var(--color-surface); }
.release-table-wrap:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 2px; }
.release-table { width: 100%; border-collapse: collapse; min-width: 58rem; }
/* UX-073: keep the row identity in view while the table is scrolled sideways. */
th:first-child, td:first-child { position: sticky; left: 0; z-index: 1; background: var(--color-surface); }
th:first-child { background: var(--color-bg); }
td:first-child { box-shadow: 1px 0 0 var(--color-border); }
th, td { padding: 0.9rem 1rem; border-bottom: 1px solid var(--color-border); text-align: left; vertical-align: middle; }
th { color: var(--color-muted-strong); background: var(--color-bg); font-size: var(--font-size-xs); letter-spacing: 0.04em; text-transform: uppercase; }
tbody tr:last-child td { border-bottom: 0; }
code { color: var(--color-text-secondary); font-size: var(--font-size-xs); overflow-wrap: anywhere; }
.values-link { color: var(--color-primary); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: var(--font-size-xs); overflow-wrap: anywhere; }
.release-table__operation { color: var(--color-primary-hover); font-weight: 700; text-decoration: none; white-space: nowrap; }
.emergency-link { color: var(--color-danger); font-weight: 700; text-decoration: none; white-space: nowrap; }
.emergency-link.blocked { color: var(--color-subtle); cursor: not-allowed; }
.convergence-link { color: var(--color-violet); font-weight: 700; text-decoration: none; white-space: nowrap; margin-left: 0.6rem; }
.muted { color: var(--color-subtle); white-space: nowrap; }

/* UX-073 (W15): below 40rem the columns become labelled cards. The labels come from
   the cells' data-label, so the header row is only visually hidden — it keeps naming
   the columns for assistive technology. */
@media (max-width: 40rem) {
  .release-table-wrap { border: 0; background: transparent; }
  .release-table { display: block; min-width: 0; }
  .release-table thead { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  .release-table tbody { display: grid; gap: 0.75rem; }
  .release-table tr { display: grid; gap: 0.35rem; padding: 0.85rem 1rem; border: 1px solid var(--color-border); border-radius: 0.6rem; background: var(--color-surface); }
  /* The label is absolutely positioned instead of a grid/flex sibling: a cell's
     content is a mix of text nodes and inline elements (e.g. chart + "@version"),
     and a grid/flex container would split those into separate rows. */
  .release-table td { display: block; position: relative; padding: 0.15rem 0 0.15rem 8rem; border: 0; }
  .release-table td::before { content: attr(data-label); position: absolute; left: 0; top: 0.15rem; width: 7.5rem; color: var(--color-muted-strong); font-size: var(--font-size-xs); font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase; }
  .release-table td:first-child { padding: 0 0 0.4rem; border-bottom: 1px solid var(--color-border); box-shadow: none; }
  .release-table td:first-child::before { content: none; }
  .release-table tbody tr:last-child td { border-bottom: 0; }
  .release-table .release-table__operation, .release-table .emergency-link,
  .release-table .convergence-link, .release-table .muted { white-space: normal; }
}
</style>
