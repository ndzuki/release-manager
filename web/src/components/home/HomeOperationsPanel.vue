<script setup lang="ts">
import { computed } from 'vue';
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import StatusTag from '@/components/common/StatusTag.vue';
import type { NonTerminalOperationItem } from '@/connect/operation-api';
import { OPERATION_ROW_LIMIT } from './homeOverview';
import { relativeTime } from './relativeTime';
import type { OperationsState } from './useHomeOverview';

/*
 * "In progress" panel (TASK-394).
 *
 * The design calls this the recent-operations list, but there is no
 * organization-wide list of FINISHED operations: ListOperations is scoped to one
 * release definition and the audit query is a cursor-paginated event list. The
 * only cross-release source is the non-terminal feed, so the panel is titled for
 * what it can actually show and says where its rows come from. A row's state is
 * therefore always an in-progress one; the success/failure/cancel tones would need
 * a data source the contract does not have, and inventing them is the one thing
 * this page must not do.
 */
const props = defineProps<{
  items: NonTerminalOperationItem[];
  truncated: boolean;
  state: OperationsState;
  /** Cluster display names from the inventory; a row outside it falls back to the id. */
  clusterNames: Record<string, string>;
}>();

const emit = defineEmits<{ retry: [] }>();

const visible = computed(() => props.items.slice(0, OPERATION_ROW_LIMIT));

function clusterOf(item: NonTerminalOperationItem): string {
  return props.clusterNames[item.clusterId] || item.clusterId || t('home.operations.clusterUnknown');
}
</script>

<template>
  <section class="home-panel" aria-labelledby="home-operations-title">
    <header class="home-panel__header">
      <h2 id="home-operations-title" class="home-panel__title">{{ t('home.operations.title') }}</h2>
      <RouterLink v-if="state !== 'off'" class="home-panel__link" :to="{ name: 'OperationCenter' }">
        {{ t('home.todo.viewAll') }}
      </RouterLink>
    </header>
    <p class="home-panel__note">{{ t('home.operations.note') }}</p>

    <p v-if="state === 'loading'" class="home-panel__state" role="status">{{ t('home.todo.loading') }}</p>
    <p v-else-if="state === 'maintenance'" class="home-panel__state" role="status">{{ t('home.todo.maintenance') }}</p>
    <p v-else-if="state === 'forbidden'" class="home-panel__state" role="alert">{{ t('home.todo.forbidden') }}</p>
    <div v-else-if="state === 'error'" class="home-panel__state" role="alert">
      {{ t('home.todo.error') }}
      <button type="button" class="home-panel__retry" @click="emit('retry')">{{ t('action.retry') }}</button>
    </div>
    <p v-else-if="visible.length === 0" class="home-panel__state">{{ t('home.todo.empty') }}</p>
    <template v-else>
      <ul class="home-panel__rows">
        <li v-for="item in visible" :key="item.operationId">
          <RouterLink
            class="home-op"
            :to="{
              name: 'OperationCenterDetail',
              params: { operationId: item.operationId },
              query: { releaseName: item.releaseDefinitionName },
            }"
            :data-testid="`home-op-${item.operationId}`"
          >
            <span class="home-op__body">
              <span class="home-op__title">
                <span class="home-op__type">{{ statusLabel('operationType', item.operationType) }}</span>
                <strong class="home-op__release">{{ item.releaseDefinitionName || item.releaseDefinitionId }}</strong>
              </span>
              <span class="home-op__meta">
                <span class="mono">{{ clusterOf(item) }}</span>
                <span aria-hidden="true">·</span>
                <span>{{ relativeTime(item.createdAt) }}</span>
              </span>
            </span>
            <StatusTag tone="info" :pulse="true" :label="statusLabel('operation', item.state)" />
          </RouterLink>
        </li>
      </ul>
      <p v-if="truncated" class="home-panel__more">
        {{ t('home.operations.truncated', { count: items.length }) }}
      </p>
    </template>
  </section>
</template>

<style scoped>
.home-panel {
  display: grid;
  gap: 12px;
  align-content: start;
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

.home-panel__link {
  color: var(--mu);
  font-size: var(--font-size-xs);
  text-decoration: none;
}

.home-panel__link:hover {
  color: var(--tx);
  text-decoration: underline;
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
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.home-op {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: space-between;
  padding: 8px;
  border-radius: 6px;
  color: var(--tx);
  text-decoration: none;
}

.home-op:hover {
  background: var(--sf2);
}

.home-op__body {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.home-op__title {
  display: flex;
  gap: 6px;
  align-items: baseline;
  min-width: 0;
}

.home-op__type {
  flex: none;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.home-op__release {
  overflow: hidden;
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-heading);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.home-op__meta {
  display: flex;
  gap: 6px;
  align-items: center;
  color: var(--mu);
  font-size: var(--font-size-xs);
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
