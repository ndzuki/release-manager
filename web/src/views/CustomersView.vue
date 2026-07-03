<script setup lang="ts">
import { shallowRef, onMounted } from 'vue'
import { useApi } from '@/composables/useApi'
import { useAuthStore } from '@/stores/auth'
import type { Customer, CustomerChartBinding } from '@/types'

const api = useApi()
const auth = useAuthStore()
const customers = shallowRef<Customer[]>([])
const loading = shallowRef(true)
const showForm = shallowRef(false)
const editingCustomer = shallowRef<Partial<Customer> | null>(null)
const bindings = shallowRef<CustomerChartBinding[]>([])
const selectedCustomer = shallowRef<Customer | null>(null)

async function load() {
  loading.value = true
  try { customers.value = await api.listCustomers() } finally { loading.value = false }
}

function openCreate() { editingCustomer.value = { enabled: true }; showForm.value = true }
function openEdit(c: Customer) { editingCustomer.value = { ...c }; showForm.value = true }
function closeForm() { showForm.value = false; editingCustomer.value = null }

async function save() {
  if (!editingCustomer.value) return
  const d = editingCustomer.value
  if (d.id && await api.getCustomer(d.id).catch(() => null)) {
    await api.updateCustomer(d.id, d)
  } else {
    await api.createCustomer(d)
  }
  closeForm()
  await load()
}

async function remove(id: string) {
  if (!confirm('确认删除？')) return
  await api.deleteCustomer(id)
  await load()
}

async function showBindings(c: Customer) {
  selectedCustomer.value = c
  try { bindings.value = await api.listCustomerCharts('default', c.id) } catch { bindings.value = [] }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <button v-if="auth.canWrite" class="btn-primary" @click="openCreate">+ 添加客户</button>
    </div>

    <div v-if="loading" class="loading">加载中...</div>
    <div v-else-if="customers.length" class="card">
      <table>
        <thead>
          <tr><th>名称</th><th>地址</th><th>状态</th><th>操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="c in customers" :key="c.id">
            <td>{{ c.name }} <span class="text-secondary">({{ c.id }})</span></td>
            <td>{{ c.operator_endpoint }}</td>
            <td><span :class="['badge', c.enabled ? 'badge-success' : 'badge-info']">{{ c.enabled ? '启用' : '禁用' }}</span></td>
            <td class="actions">
              <button class="btn-outline" @click="showBindings(c)">Charts</button>
              <button v-if="auth.canWrite" class="btn-outline" @click="openEdit(c)">编辑</button>
              <button v-if="auth.canWrite" class="btn-danger" @click="remove(c.id)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state">暂无客户，点击"添加客户"开始</div>

    <!-- Customer Form Modal -->
    <div v-if="showForm" class="modal-overlay" @click.self="closeForm">
      <div class="modal">
        <h2>{{ editingCustomer?.id ? '编辑客户' : '添加客户' }}</h2>
        <div class="form-group">
          <label>客户 ID</label>
          <input v-model="editingCustomer!.id" placeholder="customer-001" :disabled="!!editingCustomer?.id" />
        </div>
        <div class="form-group">
          <label>名称</label>
          <input v-model="editingCustomer!.name" placeholder="客户名称" />
        </div>
        <div class="form-group">
          <label>Operator 地址</label>
          <input v-model="editingCustomer!.operator_endpoint" placeholder="10.0.0.5:8443" />
        </div>
        <div class="form-group">
          <label>证书指纹</label>
          <input v-model="editingCustomer!.cert_fingerprint" placeholder="SHA256..." />
        </div>
        <div class="form-group">
          <label><input type="checkbox" v-model="editingCustomer!.enabled" style="width:auto" /> 启用</label>
        </div>
        <div class="form-actions">
          <button class="btn-outline" @click="closeForm">取消</button>
          <button class="btn-primary" @click="save">保存</button>
        </div>
      </div>
    </div>

    <!-- Chart Bindings Modal -->
    <div v-if="selectedCustomer" class="modal-overlay" @click.self="selectedCustomer = null">
      <div class="modal">
        <h2>{{ selectedCustomer.name }} — Chart 分配</h2>
        <table v-if="bindings.length">
          <thead><tr><th>Chart</th><th>Release</th><th>Namespace</th><th>版本</th><th>状态</th></tr></thead>
          <tbody>
            <tr v-for="b in bindings" :key="b.id">
              <td>{{ b.chart_name }}</td>
              <td>{{ b.release_name }}</td>
              <td>{{ b.namespace }}</td>
              <td>{{ b.current_version ?? '-' }}</td>
              <td><span :class="['badge', b.last_status === 'SUCCEEDED' ? 'badge-success' : 'badge-info']">{{ b.last_status ?? '未部署' }}</span></td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty-state">该客户尚未分配任何 Chart</div>
        <div class="form-actions">
          <button class="btn-outline" @click="selectedCustomer = null">关闭</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { margin-bottom: 16px; }
.actions { display: flex; gap: 8px; }
.text-secondary { color: var(--color-text-secondary); font-size: 12px; }
</style>
