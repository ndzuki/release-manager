<script setup lang="ts">
import { shallowRef, onMounted } from 'vue'
import { useApi } from '@/composables/useApi'
import type { SystemOverview } from '@/types'

const api = useApi()
const warnings = shallowRef<SystemOverview['certificate_warnings']>([])
const loading = shallowRef(true)

onMounted(async () => {
  try {
    const overview = await api.getDashboard()
    warnings.value = overview.certificate_warnings ?? []
  } finally { loading.value = false }
})
</script>

<template>
  <div class="card">
    <h2>证书到期预警</h2>
    <div v-if="loading" class="loading">加载中...</div>
    <table v-else-if="warnings.length">
      <thead><tr><th>客户</th><th>严重度</th><th>到期天数</th><th>指纹</th></tr></thead>
      <tbody>
        <tr v-for="w in warnings" :key="w.customer_id">
          <td>{{ w.customer_name }}</td>
          <td>
            <span :class="['badge', w.severity === 'critical' ? 'badge-danger' : 'badge-warning']">
              {{ w.severity === 'critical' ? '严重' : '警告' }}
            </span>
          </td>
          <td>{{ w.days_until_expiry }} 天</td>
          <td style="font-family:monospace;font-size:12px">{{ w.fingerprint?.slice(0,16) }}...</td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty-state">所有证书正常，暂无到期风险</div>

    <div class="card" style="margin-top:16px; background:#f0f9eb">
      <h3>证书远程更新</h3>
      <p style="color:var(--color-text-secondary);margin:8px 0">
        通过 release-manager 调用 UpdateCertificate API 远程推送新证书到目标客户 operator，
        支持 TLS 热加载，无需重启 Pod。
        详见 <a href="/api/proto/release/v1/swagger.md">API 文档</a>。
      </p>
    </div>
  </div>
</template>
