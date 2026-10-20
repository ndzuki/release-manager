<script setup lang="ts">
import { computed } from 'vue';
import { t } from '@/i18n/messages';
import { CLUSTER_LIMIT, type ClusterHealth } from './homeOverview';
import type { SourceState } from './useHomeOverview';

/*
 * Cluster health panel (TASK-394).
 *
 * "已同步 N/M" is the observed inventory split: M releases are recorded for the
 * cluster and N of them currently read `active`. A cluster with releases but no
 * online operator is exactly the case the design calls out, so it gets its own
 * label instead of a 0 that reads like a normal countdown.
 *
 * The operator count comes from a per-cluster probe (ListOperators is
 * cluster-scoped). Only the clusters this panel renders are probed, and the
 * footer states the coverage rather than implying the whole organization was
 * counted.
 */
const props = defineProps<{
  clusters: ClusterHealth[];
  clustersTotal: number;
  state: SourceState;
}>();

const emit = defineEmits<{ retry: [] }>();

const shown = computed(() => props.clusters.slice(0, CLUSTER_LIMIT));
const hidden = computed(() => Math.max(0, props.clustersTotal - shown.value.length));

/*
 * One heartbeat interval for the whole panel: the server returns the same
 * constant for every operator list, so the first probed cluster carries it.
 */
const heartbeat = computed(
  () => shown.value.find((cluster) => cluster.heartbeatSeconds !== null)?.heartbeatSeconds ?? null,
);

function percent(cluster: ClusterHealth): number {
  return cluster.total === 0 ? 0 : Math.round((cluster.synced / cluster.total) * 100);
}
</script>

<template>
  <section class="home-panel" aria-labelledby="home-clusters-title">
    <header class="home-panel__header">
      <h2 id="home-clusters-title" class="home-panel__title">{{ t('home.cluster.title') }}</h2>
    </header>

    <p v-if="state === 'off'" class="home-panel__state">{{ t('home.overview.featureOff') }}</p>
    <p v-else-if="state === 'loading'" class="home-panel__state" role="status">{{ t('home.overview.loading') }}</p>
    <div v-else-if="state === 'error'" class="home-panel__state" role="alert">
      {{ t('home.cluster.error') }}
      <button type="button" class="home-panel__retry" @click="emit('retry')">{{ t('action.retry') }}</button>
    </div>
    <p v-else-if="shown.length === 0" class="home-panel__state">{{ t('home.cluster.empty') }}</p>
    <template v-else>
      <ul class="home-panel__rows">
        <li v-for="cluster in shown" :key="cluster.clusterId" class="home-cluster" :data-testid="`home-cluster-${cluster.clusterId}`">
          <div class="home-cluster__head">
            <span class="mono home-cluster__name">{{ cluster.name }}</span>
            <span class="home-cluster__operators">
              <template v-if="cluster.onlineOperators === null">—</template>
              <template v-else-if="cluster.onlineOperators === 0">{{ t('home.cluster.noOperator') }}</template>
              <template v-else>{{ t('home.cluster.operators', { count: cluster.onlineOperators }) }}</template>
            </span>
          </div>
          <div class="home-cluster__meter">
            <span class="home-cluster__synced tabular-nums">
              {{ t('home.cluster.synced', { synced: cluster.synced, total: cluster.total }) }}
            </span>
            <span
              class="home-cluster__bar"
              role="progressbar"
              :aria-valuenow="cluster.synced"
              aria-valuemin="0"
              :aria-valuemax="cluster.total"
              :aria-label="t('home.cluster.synced', { synced: cluster.synced, total: cluster.total })"
            >
              <span class="home-cluster__fill" :style="{ width: `${percent(cluster)}%` }" />
            </span>
          </div>
        </li>
      </ul>
      <p v-if="hidden > 0" class="home-panel__more">{{ t('home.cluster.more', { count: hidden }) }}</p>
      <p class="home-panel__note home-panel__foot">
        {{ heartbeat === null ? t('home.cluster.heartbeatUnknown') : t('home.cluster.heartbeat', { seconds: heartbeat }) }}
      </p>
    </template>
  </section>
</template>

<style scoped>
.home-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
}

.home-panel__header {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: baseline;
  justify-content: space-between;
}

.home-panel__title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-heading);
}

.home-panel__note,
.home-panel__state,
.home-panel__more {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.home-panel__rows {
  display: grid;
  gap: 10px;
  margin: 0;
  padding: 0;
  list-style: none;
}

/* The panel shares its row with the (taller) operation list, so the heartbeat sits
   at the bottom edge instead of floating mid-card. */
.home-panel__foot {
  margin-top: auto;
}

.home-cluster {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.home-cluster__head {
  display: flex;
  gap: 8px;
  align-items: baseline;
  justify-content: space-between;
  min-width: 0;
}

.home-cluster__name {
  overflow: hidden;
  color: var(--tx);
  font-size: var(--font-size-xs);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.home-cluster__operators {
  flex: none;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.home-cluster__meter {
  display: flex;
  gap: 8px;
  align-items: center;
}

.home-cluster__synced {
  flex: none;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.home-cluster__bar {
  flex: 1;
  overflow: hidden;
  height: 6px;
  border-radius: 999px;
  background: var(--sf2);
}

.home-cluster__fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--ac);
}

.home-panel__retry {
  margin-left: 8px;
  padding: 2px 8px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--sf2);
  color: var(--tx);
  font-size: var(--font-size-xs);
  cursor: pointer;
}
</style>
