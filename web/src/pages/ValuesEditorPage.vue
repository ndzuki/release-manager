<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRoute } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ConvergenceLockedPathsPanel from '@/components/emergency/ConvergenceLockedPathsPanel.vue';
import SecretRefEditor from '@/components/values/SecretRefEditor.vue';
import ValuesCodeEditor from '@/components/values/ValuesCodeEditor.vue';
import ValuesConflictDialog from '@/components/values/ValuesConflictDialog.vue';
import ValuesDiffPanel from '@/components/values/ValuesDiffPanel.vue';
import ValuesEditorSkeleton from '@/components/values/ValuesEditorSkeleton.vue';
import ValuesRevisionActions from '@/components/values/ValuesRevisionActions.vue';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useValuesEditorStore } from '@/stores/valuesEditor';
import type { EditorLanguage, SecretRef } from '@/types/valuesRevision';

const route = useRoute();
const auth = useAuthStore();
const editor = useValuesEditorStore();
const authorization = useEmergencyAuthorizationStore();
const reloadingParent = shallowRef(false);
const discardConfirmOpen = ref(false);

const customerId = computed(() => String(route.params.customerId ?? ''));
const clusterId = computed(() => String(route.params.clusterId ?? ''));
const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const customerName = computed(() => String(route.query.customerName ?? customerId.value));
const clusterName = computed(() => String(route.query.clusterName ?? clusterId.value));
const releaseName = computed(() => String(route.query.releaseName ?? releaseDefinitionId.value));
const roles = computed(() => auth.user?.roles.map((role) => role.toLowerCase()) ?? []);
const canWrite = computed(() => roles.value.some((role) => ['platform_admin', 'release_admin', 'deployer'].includes(role)));
const firstRevision = computed(() => !editor.currentRevision && !editor.parentRevision);
const readOnly = computed(() => !canWrite.value || !editor.canEdit);
// Convergence mode (REQ-058 Step 8): URL carries ONLY mode + opaque token.
const convergenceMode = computed(() => String(route.query.mode ?? '') === 'convergence');
const prepareToken = computed(() => String(route.query.prepareToken ?? ''));
// Cross-actor approval: the server enforces the final authorization; the
// snapshot capability gates the buttons, and self-approval is always denied.
const canApprove = computed(
  () => authorization.canApproveValuesRevision && editor.currentRevision?.status === 'pending_approval',
);
const selfApproval = computed(
  () => editor.currentRevision !== null && editor.currentRevision.createdByUserId === auth.user?.id,
);

watch(
  [releaseDefinitionId, clusterId, canWrite, convergenceMode, prepareToken],
  async ([definitionId, nextClusterId, writable, isConvergence, token]) => {
    editor.resetScope(definitionId, nextClusterId);
    editor.setEditable(writable);
    const organizationId = auth.activeOrganization?.id ?? '';
    if (organizationId && customerId.value) {
      void authorization.load(organizationId, customerId.value);
    }
    if (isConvergence && token) {
      await editor.loadConvergence(token);
    } else {
      await editor.load();
    }
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

function requestDiscard(): void {
  discardConfirmOpen.value = true;
}

async function confirmDiscard(): Promise<void> {
  discardConfirmOpen.value = false;
  await editor.discard();
}

onBeforeUnmount(() => {
  editor.dispose();
  authorization.reset();
});
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
        <p>编辑 canonical values 并通过 SecretRef 引用集群 Secret。</p>
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
    <div v-if="firstRevision && !editor.loading" class="notice">创建首个配置 Revision。</div>
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

      <ConvergenceLockedPathsPanel
        v-if="editor.convergenceMode"
        :locked-paths="editor.lockedPaths"
        :task-ids="editor.preparedTaskIds"
      />

      <ValuesRevisionActions
        :revision="editor.currentRevision"
        :saving="editor.saving"
        :approving="editor.approving"
        :discarding="editor.discarding"
        :save-disabled="editor.saveDisabled"
        :can-approve="canApprove"
        :self-approval="selfApproval"
        :read-only="readOnly"
        @save="editor.save"
        @submit="editor.submit"
        @approve="editor.approve"
        @reject="editor.reject"
        @discard="requestDiscard"
      />

      <div v-if="discardConfirmOpen" class="discard-dialog" role="dialog" aria-modal="true" aria-label="确认丢弃 Draft">
        <p>确认丢弃当前 Draft？绑定的 {{ editor.preparedTaskIds.length }} 个收敛任务将被解绑。</p>
        <div class="discard-actions">
          <button type="button" @click="discardConfirmOpen = false">取消</button>
          <button type="button" class="danger" @click="confirmDiscard">确认丢弃</button>
        </div>
      </div>
    </template>

    <ValuesConflictDialog
      v-if="editor.showConflictDialog"
      :loading="reloadingParent"
      @reload="reloadParent"
      @close="editor.showConflictDialog = false"
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
.language-select select, .notice button, .save-bar button { min-height: 2.4rem; padding: 0.45rem 0.65rem; border: 1px solid #cbd5e1; border-radius: 0.4rem; background: #fff; }
.save-bar button.primary { border-color: #2563eb; background: #2563eb; color: #fff; }
.save-bar button:disabled { cursor: not-allowed; opacity: 0.6; }
.editor-grid { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(22rem, 0.65fr); gap: 1rem; align-items: start; }
.editor-column { display: grid; gap: 0.55rem; }
.validation-message { margin: 0; padding: 0.65rem 0.8rem; border-left: 3px solid #dc2626; background: #fef2f2; color: #991b1b; font-size: 0.85rem; }
.notice { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 0.75rem 0.9rem; border: 1px solid #93c5fd; border-radius: 0.5rem; background: #eff6ff; color: #1e3a8a; }
.notice--warning { border-color: #fbbf24; background: #fffbeb; color: #92400e; }
.notice--error { border-color: #fca5a5; background: #fef2f2; color: #991b1b; }
.save-bar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 1rem; border: 1px solid #e2e8f0; border-radius: 0.75rem; background: #fff; }
.status-line { display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem; margin: 0; }
.status-line code { color: #64748b; font-size: 0.75rem; overflow-wrap: anywhere; }
.status { padding: 0.2rem 0.45rem; border-radius: 999px; font-size: 0.7rem; font-weight: 800; }
.status--draft { background: #e0f2fe; color: #075985; }
.status--approved { background: #dcfce7; color: #166534; }
.status--rejected { background: #fee2e2; color: #991b1b; }
.status--superseded { background: #e2e8f0; color: #475569; }
.status--pending_approval { background: #fef3c7; color: #92400e; }
.discard-dialog { display: grid; gap: 0.75rem; padding: 1rem; border: 1px solid #fca5a5; border-radius: 0.65rem; background: #fef2f2; }
.discard-actions { display: flex; justify-content: flex-end; gap: 0.6rem; }
.discard-actions button { min-height: 2.4rem; padding: 0.45rem 0.8rem; border: 1px solid #cbd5e1; border-radius: 0.4rem; background: #fff; }
.discard-actions button.danger { border-color: #b91c1c; background: #b91c1c; color: #fff; }
@media (max-width: 72rem) { .editor-grid { grid-template-columns: 1fr; } }
@media (max-width: 48rem) { .values-page__header, .save-bar { flex-direction: column; } }
</style>
