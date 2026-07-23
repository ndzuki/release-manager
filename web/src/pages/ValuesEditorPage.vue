<script setup lang="ts">
import { computed, onBeforeUnmount, shallowRef, watch } from 'vue';
import { useRoute } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import RejectRevisionDialog from '@/components/values/RejectRevisionDialog.vue';
import SecretRefEditor from '@/components/values/SecretRefEditor.vue';
import ValuesCodeEditor from '@/components/values/ValuesCodeEditor.vue';
import ValuesConflictDialog from '@/components/values/ValuesConflictDialog.vue';
import ValuesDiffPanel from '@/components/values/ValuesDiffPanel.vue';
import ValuesEditorSkeleton from '@/components/values/ValuesEditorSkeleton.vue';
import ValuesRevisionActions from '@/components/values/ValuesRevisionActions.vue';
import { useAuthStore } from '@/stores/auth';
import { useValuesEditorStore } from '@/stores/valuesEditor';
import type { EditorLanguage, SecretRef } from '@/types/valuesRevision';

const route = useRoute();
const auth = useAuthStore();
const editor = useValuesEditorStore();
const showRejectDialog = shallowRef(false);
const reloadingParent = shallowRef(false);

const customerId = computed(() => String(route.params.customerId ?? ''));
const clusterId = computed(() => String(route.params.clusterId ?? ''));
const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const customerName = computed(() => String(route.query.customerName ?? customerId.value));
const clusterName = computed(() => String(route.query.clusterName ?? clusterId.value));
const releaseName = computed(() => String(route.query.releaseName ?? releaseDefinitionId.value));
const roles = computed(() => auth.user?.roles.map((role) => role.toLowerCase()) ?? []);
const canWrite = computed(() => roles.value.some((role) => ['platform_admin', 'release_admin', 'deployer'].includes(role)));
const hasApprovalRole = computed(() => roles.value.some((role) => ['platform_admin', 'release_admin'].includes(role)));
const selfApproval = computed(() => editor.currentRevision?.createdBy === auth.user?.id);
const canApprove = computed(() => hasApprovalRole.value && !selfApproval.value && editor.canApprove);
const firstRevision = computed(() => !editor.currentRevision && !editor.parentRevision);
const readOnly = computed(() => !canWrite.value || !editor.canEdit);

watch(
  [releaseDefinitionId, clusterId, canWrite],
  async ([definitionId, nextClusterId, writable]) => {
    editor.resetScope(definitionId, nextClusterId);
    editor.setEditable(writable);
    await editor.load();
  },
  { immediate: true },
);

function handleLanguage(event: Event): void {
  editor.setEditorLanguage((event.target as HTMLSelectElement).value as EditorLanguage);
}

function updateSecretRef(id: string, patch: Partial<SecretRef>): void {
  editor.updateSecretRef(id, patch);
}

async function reloadParent(): Promise<void> {
  reloadingParent.value = true;
  try {
    await editor.reloadParent();
  } finally {
    reloadingParent.value = false;
  }
}

async function reject(reason: string): Promise<void> {
  if (await editor.reject(reason)) showRejectDialog.value = false;
}

onBeforeUnmount(() => editor.dispose());
</script>

<template>
  <section class="values-page">
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <span>{{ customerName }}</span><span aria-hidden="true">/</span>
      <span>{{ clusterName }}</span><span aria-hidden="true">/</span>
      <span>{{ releaseName }}</span><span aria-hidden="true">/</span>
      <strong>Values</strong>
    </nav>

    <header class="values-page__header">
      <div>
        <p class="eyebrow">ValuesRevision editor</p>
        <h1>{{ releaseName }} Values</h1>
        <p>编辑 canonical values、检查结构化 diff，并通过 SecretRef 引用集群 Secret。</p>
      </div>
      <label class="language-select">
        Language
        <select :value="editor.editorLanguage" :disabled="readOnly" @change="handleLanguage">
          <option value="yaml">YAML</option>
          <option value="json">JSON</option>
        </select>
      </label>
    </header>

    <div v-if="editor.toast" class="notice" role="status">{{ editor.toast }}</div>
    <div v-if="editor.restoredDraft" class="notice notice--warning" role="status">已恢复未保存的编辑</div>
    <div v-if="firstRevision && !editor.loading" class="notice">创建首个配置 Revision。不会拉取 chart 默认 values.yaml。</div>
    <div v-if="!canWrite" class="notice notice--warning">当前角色为只读。服务端仍会独立执行授权。</div>

    <ValuesEditorSkeleton v-if="editor.loading && !editor.currentRevision && !editor.parentRevision" />
    <ErrorState
      v-else-if="editor.error && !editor.canonicalCurrent"
      title="ValuesRevision 加载失败"
      :message="editor.error"
      action-label="重试"
      @action="editor.load"
    />
    <template v-else>
      <div v-if="editor.error" class="notice notice--error" role="alert">
        <span>{{ editor.error }}</span>
        <button type="button" @click="editor.load">重试</button>
      </div>

      <div class="editor-grid">
        <div class="editor-column">
          <ValuesCodeEditor
            :model-value="editor.editorContent"
            :language="editor.editorLanguage"
            :read-only="readOnly"
            :server-issue="editor.validationIssue"
            @update:model-value="editor.setEditorContent"
          />
          <p v-if="editor.validationIssue" class="validation-message" role="alert">{{ editor.validationIssue.message }}</p>
        </div>
        <ValuesDiffPanel :result="editor.diffResult" />
      </div>

      <SecretRefEditor
        :items="editor.secretRefs"
        :secrets="editor.availableSecrets"
        :disabled="readOnly || editor.saving"
        :error="editor.secretRefsError"
        @add="editor.addSecretRef"
        @remove="editor.removeSecretRef"
        @update="updateSecretRef"
      />

      <ValuesRevisionActions
        :revision="editor.currentRevision"
        :saving="editor.saving"
        :approving="editor.approving"
        :save-disabled="editor.saveDisabled"
        :can-approve="canApprove"
        :self-approval="selfApproval"
        :read-only="!canWrite"
        @save="editor.save"
        @approve="editor.approve"
        @reject="showRejectDialog = true"
      />
    </template>

    <ValuesConflictDialog
      v-if="editor.showConflictDialog"
      :loading="reloadingParent"
      @reload="reloadParent"
      @close="editor.showConflictDialog = false"
    />
    <RejectRevisionDialog
      v-if="showRejectDialog"
      :submitting="editor.approving"
      @submit="reject"
      @close="showRejectDialog = false"
    />
  </section>
</template>

<style scoped>
.values-page { display: grid; gap: 1.25rem; max-width: 96rem; margin: 0 auto; }
.breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: #64748b; font-size: 0.85rem; }
.values-page__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1.5rem; }
.values-page__header h1, .values-page__header p { margin: 0; }
.values-page__header > div { display: grid; gap: 0.35rem; }
.values-page__header > div > p:last-child { color: #64748b; }
.eyebrow { color: #2563eb; font-size: 0.75rem; font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.language-select { display: grid; gap: 0.35rem; color: #475569; font-size: 0.75rem; font-weight: 700; }
.language-select select, .notice button { min-height: 2.4rem; padding: 0.45rem 0.65rem; border: 1px solid #cbd5e1; border-radius: 0.4rem; background: #fff; }
.editor-grid { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(22rem, 0.65fr); gap: 1rem; align-items: start; }
.editor-column { display: grid; gap: 0.55rem; }
.validation-message { margin: 0; padding: 0.65rem 0.8rem; border-left: 3px solid #dc2626; background: #fef2f2; color: #991b1b; font-size: 0.85rem; }
.notice { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 0.75rem 0.9rem; border: 1px solid #93c5fd; border-radius: 0.5rem; background: #eff6ff; color: #1e3a8a; }
.notice--warning { border-color: #fbbf24; background: #fffbeb; color: #92400e; }
.notice--error { border-color: #fca5a5; background: #fef2f2; color: #991b1b; }
@media (max-width: 72rem) { .editor-grid { grid-template-columns: 1fr; } }
@media (max-width: 48rem) { .values-page__header { flex-direction: column; } }
</style>
