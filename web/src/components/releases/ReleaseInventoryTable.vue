<script setup lang="ts">
import ReleaseStatusBadge from './ReleaseStatusBadge.vue';
import type { ReleaseSummary } from '@/stores/releaseInventory';

const props = defineProps<{
  releases: ReleaseSummary[];
  customerId: string;
  clusterId: string;
}>();

const valuesRevisionEnabled = import.meta.env.VITE_ENABLE_VALUES_REVISION !== 'false';

function valuesRoute(release: ReleaseSummary) {
  return {
    name: 'ValuesEditor',
    params: { customerId: props.customerId, clusterId: props.clusterId, releaseId: release.releaseDefinitionId },
    query: { releaseName: release.name },
  };
}

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '尚未同步';
}
</script>

<template>
  <div class="release-table-wrap">
    <table class="release-table">
      <thead>
        <tr>
          <th scope="col">Release</th>
          <th scope="col">状态</th>
          <th scope="col">Chart</th>
          <th scope="col">Revision</th>
          <th scope="col">Values digest</th>
          <th scope="col">最近同步</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="release in releases" :key="`${release.namespace}/${release.name}`">
          <td><strong>{{ release.namespace }}/{{ release.name }}</strong></td>
          <td><ReleaseStatusBadge :status="release.status" /></td>
          <td>{{ release.chart }}<span v-if="release.chartVersion">@{{ release.chartVersion }}</span></td>
          <td>{{ release.revision }}</td>
          <td>
            <RouterLink
              v-if="valuesRevisionEnabled && release.releaseDefinitionId && release.valuesDigest"
              class="values-link"
              :to="valuesRoute(release)"
            >
              {{ release.valuesDigest }}
            </RouterLink>
            <code v-else>{{ release.valuesDigest || '—' }}</code>
          </td>
          <td>{{ formatTimestamp(release.lastSyncAt) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.release-table-wrap { overflow-x: auto; border: 1px solid #e2e8f0; border-radius: 0.65rem; background: #fff; }
.release-table { width: 100%; border-collapse: collapse; min-width: 58rem; }
th, td { padding: 0.9rem 1rem; border-bottom: 1px solid #e2e8f0; text-align: left; vertical-align: middle; }
th { color: #475569; background: #f8fafc; font-size: 0.75rem; letter-spacing: 0.04em; text-transform: uppercase; }
tbody tr:last-child td { border-bottom: 0; }
code { color: #334155; font-size: 0.75rem; overflow-wrap: anywhere; }
.values-link { color: #2563eb; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.75rem; overflow-wrap: anywhere; }
</style>
