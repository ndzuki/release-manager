<script setup lang="ts">
import ReleaseStatusBadge from './ReleaseStatusBadge.vue';
import type { ReleaseSummary } from '@/stores/releaseInventory';

const props = defineProps<{
  releases: ReleaseSummary[];
  canCreateOperation?: boolean;
  canEmergency?: boolean;
  canConvergence?: boolean;
  customerId?: string;
  clusterId?: string;
  customerName?: string;
  clusterName?: string;
}>();

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
          <th v-if="canEmergency || canConvergence" scope="col">紧急变更</th>
          <th v-if="canCreateOperation" scope="col">操作</th>
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
          <td v-if="canEmergency || canConvergence">
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
          <td v-if="canCreateOperation">
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
          </td>
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
.release-table__operation { color: #1d4ed8; font-weight: 700; text-decoration: none; white-space: nowrap; }
.emergency-link { color: #dc2626; font-weight: 700; text-decoration: none; white-space: nowrap; }
.emergency-link.blocked { color: #94a3b8; cursor: not-allowed; }
.convergence-link { color: #7c3aed; font-weight: 700; text-decoration: none; white-space: nowrap; margin-left: 0.6rem; }
.muted { color: #94a3b8; white-space: nowrap; }
</style>
