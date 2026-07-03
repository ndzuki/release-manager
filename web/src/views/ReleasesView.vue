<script setup lang="ts">
import { shallowRef, computed, onMounted } from 'vue'
import { useApi } from '@/composables/useApi'
import type { ReleaseRecord } from '@/types'

const api = useApi()
const releases = shallowRef<ReleaseRecord[]>([])
const loading = shallowRef(true)
const filterStatus = shallowRef('')

const filtered = computed(() => {
  if (!filterStatus.value) return releases.value
  return releases.value.filter(r => r.status === filterStatus.value)
})

const statuses = computed(() => [...new Set(releases.value.map(r => r.status))])

async function load() {
  try { releases.value = await api.listReleases() } finally { loading.value = false }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <select v-model="filterStatus" style="width:200px">
        <option value="">全部状态</option>
        <option v-for="s in statuses" :key="s" :value="s">{{ s }}</option>
      </select>
    </div>

    <div class="card">
      <table>
        <thead><tr><th>客户</th><th>Chart</th><th>版本</th><th>状态</th><th>耗时</th><th>时间</th><th>错误</th></tr></thead>
        <tbody>
          <tr v-for="r in filtered" :key="r.id">
            <td>{{ r.customer_id }}</td>
            <td>{{ r.chart_name }}</td>
            <td>{{ r.chart_version }}</td>
            <td>
              <span :class="['badge',
                r.status === 'SUCCEEDED' ? 'badge-success' :
                r.status === 'ROLLED_BACK' ? 'badge-warning' :
                r.status === 'FAILED' || r.status === 'ROLLBACK_FAILED' ? 'badge-danger' : 'badge-info'
              ]">{{ r.status }}</span>
            </td>
            <td>{{ r.duration_secs }}s</td>
            <td>{{ new Date(r.completed_at || r.started_at).toLocaleString() }}</td>
            <td style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" :title="r.error_message">
              {{ r.error_message || '-' }}
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="!filtered.length && !loading" class="empty-state">暂无发布记录</div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { margin-bottom: 16px; }
</style>
