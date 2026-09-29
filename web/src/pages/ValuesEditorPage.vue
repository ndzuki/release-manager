<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import AppDialog from '@/components/common/AppDialog.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import AuthorizationStaleNotice from '@/components/common/AuthorizationStaleNotice.vue';
import ConvergenceLockedPathsPanel from '@/components/emergency/ConvergenceLockedPathsPanel.vue';
import SecretRefEditor from '@/components/values/SecretRefEditor.vue';
import ValuesCodeEditor from '@/components/values/ValuesCodeEditor.vue';
import ValuesConflictDialog from '@/components/values/ValuesConflictDialog.vue';
import ValuesDiffPanel from '@/components/values/ValuesDiffPanel.vue';
import ValuesEditorSkeleton from '@/components/values/ValuesEditorSkeleton.vue';
import RejectRevisionDialog from '@/components/values/RejectRevisionDialog.vue';
import ValuesRevisionActions from '@/components/values/ValuesRevisionActions.vue';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useValuesEditorStore } from '@/stores/valuesEditor';
import type { EditorLanguage, SecretRef } from '@/types/valuesRevision';

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();
const editor = useValuesEditorStore();
const authorization = useEmergencyAuthorizationStore();
// AC-033-10: the values/convergence path stays readable, but the state is
// surfaced so a disabled write entry is explained rather than mysterious.
const writeBlocked = computed(() => !authorization.writeAllowed);
const reloadingParent = shallowRef(false);
const discardConfirmOpen = ref(false);
// AC-055-11: the reject reason is captured in a dialog, not submitted blind.
const rejectDialogOpen = ref(false);

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

// AC-055-15: a successful Reject or Discard returns to the convergence task
// list. Leaving the page on its own must NOT discard, so this is called only
// after the store reports success.
async function returnToConvergenceTasks(): Promise<void> {
  await router.push({
    name: 'ConvergenceTasks',
    params: {
      customerId: customerId.value,
      clusterId: clusterId.value,
      releaseId: releaseDefinitionId.value,
    },
  });
}

function requestReject(): void {
  rejectDialogOpen.value = true;
}

async function confirmReject(reason: string): Promise<void> {
  rejectDialogOpen.value = false;
  if (await editor.reject(reason)) {
    await returnToConvergenceTasks();
  }
}

function requestDiscard(): void {
  discardConfirmOpen.value = true;
}

async function confirmDiscard(): Promise<void> {
  discardConfirmOpen.value = false;
  if (await editor.discard()) {
    await returnToConvergenceTasks();
  }
}

onBeforeUnmount(() => {
  editor.dispose();
  authorization.reset();
});
</script>

<template>
  <section class="values-page">
    <AuthorizationStaleNotice :stale="writeBlocked" />
    <nav class="breadcrumbs" :aria-label="t('values.page.breadcrumb')">
      <span>{{ customerName }}</span><span aria-hidden="true">/</span>
      <span>{{ clusterName }}</span><span aria-hidden="true">/</span>
      <span>{{ releaseName }}</span><span aria-hidden="true">/</span>
      <strong>{{ t('values.page.title') }}</strong>
    </nav>

    <header class="values-page__header">
      <div>
        <p class="eyebrow">{{ t('values.page.editor') }}</p>
        <h1>{{ releaseName }} {{ t('values.page.title') }}</h1>
        <p>编辑 canonical values 并通过 SecretRef 引用集群 Secret。</p>
      </div>
      <label class="language-select">
        {{ t('values.page.language') }}
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
        :submit-disabled="editor.saveDisabled || editor.saving || editor.hasUnsavedChanges"
        :can-approve="canApprove"
        :self-approval="selfApproval"
        :read-only="readOnly"
        @save="editor.save"
        @submit="editor.submit"
        @approve="editor.approve"
        @reject="requestReject"
        @discard="requestDiscard"
      />

      <RejectRevisionDialog
        v-if="rejectDialogOpen"
        :submitting="editor.approving"
        @submit="confirmReject"
        @close="rejectDialogOpen = false"
      />

      <AppDialog
        :open="discardConfirmOpen"
        title="确认丢弃 Draft"
        danger
        :close-on-backdrop="false"
        @close="discardConfirmOpen = false"
      >
        <p class="discard-dialog__body">
          确认丢弃当前 Draft？绑定的 {{ editor.preparedTaskIds.length }} 个收敛任务将被解绑。
        </p>
        <template #footer>
          <button type="button" @click="discardConfirmOpen = false">取消</button>
          <button type="button" class="discard-dialog__danger" @click="confirmDiscard">确认丢弃</button>
        </template>
      </AppDialog>
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
.breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: var(--color-muted); font-size: var(--font-size-sm); }
.values-page__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1.5rem; }
.values-page__header h1, .values-page__header p { margin: 0; }
.values-page__header > div { display: grid; gap: 0.35rem; }
.values-page__header > div > p:last-child { color: var(--color-muted); }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.language-select { display: grid; gap: 0.35rem; color: var(--color-muted-strong); font-size: var(--font-size-xs); font-weight: 700; }
.language-select select, .notice button, .save-bar button { min-height: 2.4rem; padding: 0.45rem 0.65rem; border: 1px solid var(--color-border-strong); border-radius: 0.4rem; background: var(--color-surface); }
.save-bar button.primary { border-color: var(--color-primary); background: var(--color-primary); color: var(--color-on-accent); }
.save-bar button:disabled { cursor: not-allowed; opacity: 0.6; }
.editor-grid { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(22rem, 0.65fr); gap: 1rem; align-items: start; }
.editor-column { display: grid; gap: 0.55rem; }
.validation-message { margin: 0; padding: 0.65rem 0.8rem; border-left: 3px solid var(--color-danger); background: var(--color-danger-soft); color: var(--color-error-strong); font-size: var(--font-size-sm); }
.notice { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 0.75rem 0.9rem; border: 1px solid var(--color-info-border); border-radius: 0.5rem; background: var(--color-info-soft); color: var(--color-info-ink); }
.notice--warning { border-color: var(--color-warning-border-bright); background: var(--color-warning-surface); color: var(--color-warning-ink-strong); }
.notice--error { border-color: var(--color-danger-border-strong); background: var(--color-danger-soft); color: var(--color-error-strong); }
.save-bar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border); border-radius: 0.75rem; background: var(--color-surface); }
.status-line { display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem; margin: 0; }
.status-line code { color: var(--color-muted); font-size: var(--font-size-xs); overflow-wrap: anywhere; }
.status { padding: 0.2rem 0.45rem; border-radius: 999px; font-size: var(--font-size-xs); font-weight: 800; }
.status--draft { background: var(--color-info-subtle); color: var(--color-info-ink-strong); }
.status--approved { background: var(--color-success-surface); color: var(--color-success-ink); }
.status--rejected { background: var(--color-danger-surface); color: var(--color-error-strong); }
.status--superseded { background: var(--color-border); color: var(--color-muted-strong); }
.status--pending_approval { background: var(--color-warning-subtle); color: var(--color-warning-ink-strong); }
.discard-dialog__body { margin: 0; }
.discard-dialog__danger { border-color: var(--color-error); background: var(--color-error); color: var(--color-on-accent); }
@media (max-width: 72rem) { .editor-grid { grid-template-columns: 1fr; } }
@media (max-width: 48rem) { .values-page__header, .save-bar { flex-direction: column; } }
</style>
