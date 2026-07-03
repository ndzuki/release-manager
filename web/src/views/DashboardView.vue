<script setup lang="ts">
import { shallowRef, onMounted, computed } from 'vue'
import { useApi } from '@/composables/useApi'
import type { SystemOverview } from '@/types'

const api = useApi()
const overview = shallowRef<SystemOverview | null>(null)
const loading = shallowRef(true)

const stats = computed(() => {
  if (!overview.value) return []
  const o = overview.value
  return [
    { label: '客户总数', value: o.total_customers, sub: `${o.enabled_customers} 在线` },
    { label: '成功率', value: `${o.release_success_rate.toFixed(1)}%`, sub: '近 30 天' },
    { label: '发布记录', value: o.recent_releases.length, sub: '最近 20 条' },
  ]
})

onMounted(async () => {
  try {
    overview.value = await api.getDashboard()
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div v-if="loading" class="loading">加载中...</div>
  <div v-else-if="overview">
    <div class="stats-grid">
      <div v-for="s in stats" :key="s.label" class="card stat-card">
        <div class="stat-value">{{ s.value }}</div>
        <div class="stat-label">{{ s.label }}</div>
        <div class="stat-sub">{{ s.sub }}</div>
      </div>
    </div>

    <div class="card">
      <h2>客户状态</h2>
      <table>
        <thead>
          <tr><th>客户</th><th>状态</th><th>版本</th><th>证书到期</th><th>最后心跳</th></tr>
        </thead>
        <tbody>
          <tr v-for="cs in overview.customer_statuses" :key="cs.customer_id">
            <td>{{ cs.customer_name }}</td>
            <td><span :class="['badge', cs.online ? 'badge-success' : 'badge-danger']">{{ cs.online ? '在线' : '离线' }}</span></td>
            <td>{{ cs.release_count }} 个 release</td>
            <td>
              <span :class="['badge', cs.days_until_cert_expiry < 30 ? 'badge-danger' : cs.days_until_cert_expiry < 60 ? 'badge-warning' : 'badge-info']">
                {{ cs.days_until_cert_expiry }} 天
              </span>
            </td>
            <td>{{ cs.last_seen_at ? new Date(cs.last_seen_at).toLocaleString() : '-' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!overview.customer_statuses?.length" class="empty-state">暂无客户数据</div>
    </div>

    <div class="card">
      <h2>证书到期预警</h2>
      <div v-if="overview.certificate_warnings?.length">
        <div v-for="w in overview.certificate_warnings" :key="w.customer_id" class="warning-row">
          <span :class="['badge', w.severity === 'critical' ? 'badge-danger' : 'badge-warning']">
            {{ w.severity === 'critical' ? '⚠ 严重' : '注意' }}
          </span>
          <span>{{ w.customer_name }}</span>
          <span>{{ w.days_until_expiry }} 天后到期</span>
        </div>
      </div>
      <div v-else class="empty-state">所有证书正常</div>
    </div>

    <div class="card">
      <h2>最近发布</h2>
      <table>
        <thead>
          <tr><th>客户</th><th>Chart</th><th>版本</th><th>状态</th><th>时间</th></tr>
        </thead>
        <tbody>
          <tr v-for="r in overview.recent_releases" :key="r.id">
            <td>{{ r.customer_id }}</td>
            <td>{{ r.chart_name }}</td>
            <td>{{ r.chart_version }}</td>
            <td><span :class="['badge', r.status === 'SUCCEEDED' ? 'badge-success' : 'badge-danger']">{{ r.status }}</span></td>
            <td>{{ new Date(r.started_at).toLocaleString() }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!overview.recent_releases?.length" class="empty-state">暂无发布记录</div>
    </div>
  </div>
</template>

<style scoped>
.stats-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; margin-bottom: 16px; }
.stat-card { text-align: center; }
.stat-value { font-size: 32px; font-weight: 700; color: var(--color-primary); }
.stat-label { color: var(--color-text-secondary); margin-top: 4px; }
.stat-sub { font-size: 12px; color: var(--color-text-secondary); }
.warning-row { display: flex; gap: 12px; align-items: center; padding: 8px 0; }
</style>
