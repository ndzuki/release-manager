<script setup lang="ts">
import { shallowRef, onMounted } from 'vue'
import { useApi } from '@/composables/useApi'
import type { ChartDefinition } from '@/types'

const api = useApi()
const charts = shallowRef<ChartDefinition[]>([])
const loading = shallowRef(true)
const showForm = shallowRef(false)
const form = shallowRef<Partial<ChartDefinition>>({})

function openCreate() { form.value = { enabled: true }; showForm.value = true }

async function save() {
  await api.createChart('default', form.value)
  showForm.value = false
  await load()
}

async function load() {
  try { charts.value = await api.listCharts() } finally { loading.value = false }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <button class="btn-primary" @click="openCreate">+ 添加 Chart</button>
    </div>

    <div v-if="loading" class="loading">加载中...</div>
    <div v-else-if="charts.length" class="card">
      <table>
        <thead><tr><th>名称</th><th>OCI URL</th><th>描述</th><th>状态</th></tr></thead>
        <tbody>
          <tr v-for="c in charts" :key="c.id">
            <td>{{ c.name }}</td>
            <td style="font-family:monospace;font-size:13px">{{ c.oci_url }}</td>
            <td>{{ c.description || '-' }}</td>
            <td><span :class="['badge', c.enabled ? 'badge-success' : 'badge-info']">{{ c.enabled ? '启用' : '禁用' }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state">暂无 Chart 定义</div>

    <div v-if="showForm" class="modal-overlay" @click.self="showForm = false">
      <div class="modal">
        <h2>添加 Chart</h2>
        <div class="form-group">
          <label>名称</label><input v-model="form.name" placeholder="magic-sandbox" />
        </div>
        <div class="form-group">
          <label>OCI URL</label><input v-model="form.oci_url" placeholder="oci://harbor.example.com/helm/magic-sandbox" />
        </div>
        <div class="form-group">
          <label>描述</label><input v-model="form.description" placeholder="描述信息" />
        </div>
        <div class="form-actions">
          <button class="btn-outline" @click="showForm = false">取消</button>
          <button class="btn-primary" @click="save">保存</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { margin-bottom: 16px; }
</style>
