// API 客户端 composable — 封装 fetch + 认证 + 错误处理

import { useAuthStore } from '@/stores/auth'

const BASE_URL = '/api/v1'

class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const auth = useAuthStore()
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string>),
  }

  if (auth.token) {
    headers['Authorization'] = `Bearer ${auth.token}`
  }
  if (auth.apiKey) {
    headers['X-API-Key'] = auth.apiKey
  }

  const res = await fetch(`${BASE_URL}${path}`, {
    ...options,
    headers,
  })

  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new ApiError(res.status, body.error || res.statusText)
  }

  return res.json()
}

export function useApi() {
  return {
    // --- Customers ---
    listCustomers(enabledOnly = false) {
      return request<Customer[]>(`/customers?enabled=${enabledOnly}`)
    },
    getCustomer(id: string) {
      return request<Customer>(`/customers/${id}`)
    },
    createCustomer(data: Partial<Customer>) {
      return request<Customer>('/customers', { method: 'POST', body: JSON.stringify(data) })
    },
    updateCustomer(id: string, data: Partial<Customer>) {
      return request<Customer>(`/customers/${id}`, { method: 'PUT', body: JSON.stringify(data) })
    },
    deleteCustomer(id: string) {
      return request<{ deleted: string }>(`/customers/${id}`, { method: 'DELETE' })
    },

    // --- Charts ---
    listCharts(orgId = 'default') {
      return request<ChartDefinition[]>(`/orgs/${orgId}/charts`)
    },
    createChart(orgId: string, data: Partial<ChartDefinition>) {
      return request<ChartDefinition>(`/orgs/${orgId}/charts`, { method: 'POST', body: JSON.stringify(data) })
    },

    // --- Customer-Chart Bindings ---
    listCustomerCharts(orgId: string, custId: string) {
      return request<CustomerChartBinding[]>(`/orgs/${orgId}/customers/${custId}/charts`)
    },
    bindChart(orgId: string, custId: string, data: Partial<CustomerChartBinding>) {
      return request<CustomerChartBinding>(`/orgs/${orgId}/customers/${custId}/charts`, { method: 'POST', body: JSON.stringify(data) })
    },

    // --- Releases ---
    listReleases(requestId = '') {
      const q = requestId ? `/${requestId}` : ''
      return request<ReleaseRecord[]>(`/releases${q}`)
    },

    // --- Dashboard ---
    getDashboard() {
      return request<SystemOverview>('/dashboard/overview')
    },
  }
}

import type { Customer, ChartDefinition, CustomerChartBinding, ReleaseRecord, SystemOverview } from '@/types'

export { ApiError }
