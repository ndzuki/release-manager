import { computed, ref, toRaw } from 'vue';
import { defineStore } from 'pinia';
import {
  createCluster,
  disableCluster as disableClusterRpc,
  getCluster,
  listClusters,
  mapSaveError,
  updateCluster,
} from '@/connect/cluster-api';
import type { Cluster, ClusterFormInput, ClusterSummary, SaveError } from '@/types/cluster';
import { validateClusterForm } from '@/utils/cluster-routing';

export const useClusterStore = defineStore('cluster', () => {
  const clusters = ref<ClusterSummary[]>([]);
  const loading = ref(false);
  const error = ref<string | null>(null);
  const forbidden = ref(false);
  const notFound = ref(false);
  const current = ref<Cluster | null>(null);
  const draft = ref<ClusterFormInput | null>(null);
  const serverVersion = ref<number | null>(null);
  const saving = ref(false);
  const saveError = ref<SaveError | null>(null);

  const hasClusters = computed(() => clusters.value.length > 0);

  async function loadList(customerId: string) {
    loading.value = true;
    error.value = null;
    forbidden.value = false;
    try {
      clusters.value = await listClusters(customerId);
    } catch (cause) {
      applyLoadError(cause);
    } finally {
      loading.value = false;
    }
  }

  async function loadCluster(clusterId: string) {
    loading.value = true;
    error.value = null;
    forbidden.value = false;
    notFound.value = false;
    try {
      const cluster = await getCluster(clusterId);
      current.value = cluster;
      draft.value = {
        name: cluster.name,
        enabled: cluster.enabled,
        version: cluster.version,
        imageRules: structuredClone(cluster.imageRules),
        chartRules: structuredClone(cluster.chartRules),
      };
      serverVersion.value = cluster.version;
    } catch (cause) {
      applyLoadError(cause);
    } finally {
      loading.value = false;
    }
  }

  function startCreate() {
    current.value = null;
    serverVersion.value = null;
    saveError.value = null;
    draft.value = {
      name: '',
      enabled: true,
      version: 0,
      imageRules: [],
      chartRules: [],
    };
  }

  async function save(customerId: string, clusterId?: string): Promise<Cluster | null> {
    if (!draft.value) return null;
    saveError.value = null;
    const validation = validateClusterForm(draft.value);
    if (!validation.valid) {
      saveError.value = {
        code: 'client_validation',
        message: 'Fix the highlighted fields before saving.',
        fieldViolations: validation.violations,
      };
      return null;
    }

    saving.value = true;
    try {
      let saved: Cluster;
      if (!clusterId) {
        const created = await createCluster(customerId, draft.value.name.trim());
        const createDraft = { ...draft.value, version: created.version };
        saved = await updateCluster(created.id, createDraft);
      } else {
        saved = await updateCluster(clusterId, draft.value);
      }
      current.value = saved;
      draft.value = {
        name: saved.name,
        enabled: saved.enabled,
        version: saved.version,
        imageRules: structuredClone(saved.imageRules),
        chartRules: structuredClone(saved.chartRules),
      };
      serverVersion.value = saved.version;
      const index = clusters.value.findIndex((cluster) => cluster.id === saved.id);
      if (index >= 0) clusters.value[index] = saved;
      else clusters.value.unshift(saved);
      return saved;
    } catch (cause) {
      saveError.value = mapSaveError(cause, draft.value);
      return null;
    } finally {
      saving.value = false;
    }
  }

  async function disable(clusterId: string) {
    await disableClusterRpc(clusterId);
    const cluster = clusters.value.find((item) => item.id === clusterId);
    if (cluster) cluster.enabled = false;
    if (current.value?.id === clusterId) current.value.enabled = false;
  }

  async function refreshCluster(clusterId: string) {
    const previousDraft = draft.value ? structuredClone(toRaw(draft.value)) : null;
    await loadCluster(clusterId);
    if (previousDraft && !error.value && !forbidden.value && !notFound.value) {
      draft.value = {
        ...previousDraft,
        version: serverVersion.value ?? previousDraft.version,
      };
      saveError.value = null;
    }
  }

  function clearSaveError() {
    saveError.value = null;
  }

  function applyLoadError(cause: unknown) {
    const mapped = mapSaveError(cause);
    forbidden.value = mapped.code === 'permission_denied';
    notFound.value = mapped.code === 'not_found';
    error.value = mapped.message;
  }

  return {
    clusters,
    loading,
    error,
    forbidden,
    notFound,
    current,
    draft,
    serverVersion,
    saving,
    saveError,
    hasClusters,
    loadList,
    loadCluster,
    startCreate,
    save,
    disable,
    refreshCluster,
    clearSaveError,
  };
});
