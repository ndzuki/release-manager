<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import FormField from '@/components/common/FormField.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import RouteRuleEditor from '@/components/clusters/RouteRuleEditor.vue';
import { useScopeNames } from '@/composables/useScopeNames';
import { useClusterStore } from '@/stores/cluster';
import type { ArtifactType, RouteRuleInput } from '@/types/cluster';
import { useAuthStore } from '@/stores/auth';

const route = useRoute();
const router = useRouter();
const store = useClusterStore();
const auth = useAuthStore();
const customerId = String(route.params.customerId);
const clusterId = route.params.clusterId ? String(route.params.clusterId) : undefined;
const isCreate = computed(() => !clusterId);
/*
 * The per-field half of the save failure. The page-level alert above only carries
 * `saveError.message`, so this violation was the one thing that had to reach the
 * name control as a description (TASK-271). The server/`mapSaveError` and the local
 * validator both emit at most one 'name' violation (the checks are mutually
 * exclusive), so picking the first is lossless.
 */
const nameError = computed(
  () => store.saveError?.fieldViolations?.find((item) => item.field === 'name')?.description ?? '',
);
const idError = computed(
  () => store.saveError?.fieldViolations?.find((item) => item.field === 'id')?.description ?? '',
);
const endpoints = {
  cacheEndpoint: import.meta.env.VITE_ARTIFACT_CACHE_ENDPOINT ?? 'cache.local',
  registryEndpoint: import.meta.env.VITE_ARTIFACT_REGISTRY_ENDPOINT ?? 'registry.local',
};

/*
 * Direction C (TASK-394). Both labels the hand-rolled `.eyebrow` carried move to
 * PageHeader verbatim: "新建集群" / "编辑集群" stay the category line above the title.
 * The trail is names only — the customer from `useScopeNames` and, when editing, the
 * loaded cluster. A level whose name cannot be resolved is omitted rather than printed
 * as its route identifier (PageHeader's contract forbids UUID crumbs).
 */
const { customerName } = useScopeNames({ resolveCluster: false });
const modeLabel = computed(() => (isCreate.value ? t('cluster.edit.newLabel') : t('cluster.edit.editLabel')));
const pageTitle = computed(() =>
  isCreate.value ? t('cluster.edit.createTitle') : (store.current?.name ?? t('cluster.edit.editLabel')),
);
const customerLabel = computed(() =>
  customerName.value && customerName.value !== customerId ? customerName.value : '',
);
const crumbs = computed(() =>
  [customerLabel.value, isCreate.value ? '' : (store.current?.name ?? '')]
    .filter((label) => label !== '')
    .map((label) => ({ label })),
);

onMounted(async () => {
  if (!auth.canWrite) {
    await router.replace({ name: 'ClusterList', params: { customerId } });
    return;
  }
  if (clusterId) await store.loadCluster(clusterId);
  else store.startCreate();
});

function addRule(artifactType: ArtifactType) {
  if (!store.draft) return;
  const rule: RouteRuleInput = {
    clientKey: crypto.randomUUID(),
    artifactType,
    mode: 'direct',
    sourcePrefix: '',
    targetPrefix: '',
  };
  if (artifactType === 'image') store.draft.imageRules.push(rule);
  else store.draft.chartRules.push(rule);
}

function removeRule(artifactType: ArtifactType, index: number) {
  if (!store.draft) return;
  if (artifactType === 'image') store.draft.imageRules.splice(index, 1);
  else store.draft.chartRules.splice(index, 1);
}

async function handleSave() {
  const saved = await store.save(customerId, clusterId);
  if (saved) await router.push({ name: 'ClusterDetail', params: { customerId, clusterId: saved.id } });
}
</script>

<template>
  <section class="page">
    <LoadingState v-if="store.loading" :message="t('state.loading.cluster')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.notFound" :message="t('cluster.notFound')">
      <RouterLink :to="{ name: 'ClusterList', params: { customerId } }">{{ t('cluster.detail.back') }}</RouterLink>
    </ErrorState>

    <form v-else-if="store.draft" class="cluster-form" @submit.prevent="handleSave">
      <PageHeader
        :title="pageTitle"
        :eyebrow="modeLabel"
        :subtitle="t('cluster.edit.credentials')"
        :crumbs="crumbs"
      >
        <template #actions>
          <div class="actions">
            <RouterLink :to="clusterId ? { name: 'ClusterDetail', params: { customerId, clusterId } } : { name: 'ClusterList', params: { customerId } }">{{ t('action.cancel') }}</RouterLink>
            <button type="submit" class="primary" :disabled="store.saving">
              {{ store.saving ? t('cluster.edit.saving') : t('cluster.edit.save') }}
            </button>
          </div>
        </template>
      </PageHeader>

      <div v-if="store.saveError" class="save-error" role="alert">
        <strong>{{ store.saveError.code === 'optimistic_lock_conflict' ? t('cluster.edit.conflict') : store.saveError.message }}</strong>
        <span v-if="store.saveError.code === 'optimistic_lock_conflict'">{{ t('cluster.edit.draftPreserved') }}</span>
        <button v-if="store.saveError.code === 'network_error'" type="submit">{{ t('cluster.edit.retrySave') }}</button>
        <button v-if="store.saveError.code === 'optimistic_lock_conflict' && clusterId" type="button" @click="store.refreshCluster(clusterId)">{{ t('cluster.edit.refreshVersion') }}</button>
      </div>

      <section class="cluster-fields">
        <FormField
          v-if="isCreate"
          :label="t('cluster.edit.id')"
          :help="t('cluster.edit.idHelp')"
          :error="idError"
        >
          <template #default="{ id, describedBy, invalid }">
            <input
              :id="id"
              v-model="store.draft.id"
              maxlength="63"
              :aria-describedby="describedBy"
              :aria-invalid="invalid"
            />
          </template>
        </FormField>
        <FormField :label="t('cluster.edit.name')" :error="nameError">
          <template #default="{ id, describedBy, invalid }">
            <input
              :id="id"
              v-model="store.draft.name"
              maxlength="254"
              :aria-describedby="describedBy"
              :aria-invalid="invalid"
            />
          </template>
        </FormField>
        <label class="checkbox">
          <input v-model="store.draft.enabled" type="checkbox" />
          {{ t('cluster.edit.enabled') }}
        </label>
      </section>

      <RouteRuleEditor
        :title="t('cluster.edit.imageRoutes')"
        artifact-type="image"
        :rules="store.draft.imageRules"
        :violations="store.saveError?.fieldViolations"
        :conflicting-rule-id="store.saveError?.conflictingRuleId"
        :endpoints="endpoints"
        @add="addRule('image')"
        @remove="removeRule('image', $event)"
      />
      <RouteRuleEditor
        :title="t('cluster.edit.chartRoutes')"
        artifact-type="chart"
        :rules="store.draft.chartRules"
        :violations="store.saveError?.fieldViolations"
        :conflicting-rule-id="store.saveError?.conflictingRuleId"
        :endpoints="endpoints"
        @add="addRule('chart')"
        @remove="removeRule('chart', $event)"
      />
    </form>
  </section>
</template>

<style scoped>
.page { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
/* The form is the page's only child, so the page frame owns the width. Repeating
   `max-width: 72rem; margin: 0 auto` here made the form a shrink-to-fit grid item
   (auto margins on a grid item disable the stretch), which centred a ~470px column on a
   1116px content area — in create mode, where no rule card sets the max-content width. */
.cluster-form { display: grid; gap: 1.5rem; }
/* PageHeader keeps a 20px bottom margin for pages that do not stack their sections in a
   grid; this page does, so the gap is the only space between the header and the form. */
.cluster-form :deep(.page-header) { margin-bottom: 0; }
.actions { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 1rem; }
/* Direction C card: surface fill + the STRONG border (`--color-border` is a hairline and
   vanishes in the dark theme). */
.cluster-fields {
  display: grid;
  grid-template-columns: 2fr 1fr;
  gap: 1rem;
  padding: 1rem;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}
label { display: grid; gap: 0.375rem; font-weight: 600; }
.checkbox { display: flex; align-items: center; }
/* Direction C control: 7px/11px padding, the shared radius, the strong border and the
   page background so the field reads as an inset of the card rather than a hole in it. */
input {
  padding: 7px 11px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--color-bg);
  color: var(--color-text);
  font: inherit;
}
.save-error { display: grid; gap: 0.5rem; padding: 1rem; border: 1px solid var(--color-danger-border-bright); border-radius: 0.5rem; background: var(--color-danger-soft); color: var(--color-error-strong); }
.actions a, button { padding: 0.5rem 0.75rem; border: 1px solid var(--color-border-strong); border-radius: var(--r); background: var(--color-surface); }
.primary { background: var(--color-primary); color: var(--color-on-accent); border-color: var(--color-primary); }
</style>
