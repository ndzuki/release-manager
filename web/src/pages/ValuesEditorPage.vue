<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import AppDialog from '@/components/common/AppDialog.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import AuthorizationStaleNotice from '@/components/common/AuthorizationStaleNotice.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Toast from '@/components/common/Toast.vue';
import ConvergenceLockedPathsPanel from '@/components/emergency/ConvergenceLockedPathsPanel.vue';
import SecretRefEditor from '@/components/values/SecretRefEditor.vue';
import ValuesCodeEditor from '@/components/values/ValuesCodeEditor.vue';
import ValuesConflictDialog from '@/components/values/ValuesConflictDialog.vue';
import ValuesDiffPanel from '@/components/values/ValuesDiffPanel.vue';
import ValuesEditorSkeleton from '@/components/values/ValuesEditorSkeleton.vue';
import RejectRevisionDialog from '@/components/values/RejectRevisionDialog.vue';
import ValuesRevisionActions from '@/components/values/ValuesRevisionActions.vue';
import { useScopeNames } from '@/composables/useScopeNames';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useValuesEditorStore } from '@/stores/valuesEditor';
import type { EditorLanguage, SecretRef } from '@/types/valuesRevision';
import { formatTimestamp } from '@/utils/formatTimestamp';

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
// The visible label names the segmented group; the page owns the id because the buttons
// live in the PageHeader's action slot (same pattern as OperationForm's type group).
const languageLabelId = 'values-language-label';

/*
 * NAMES, never identifiers (TASK-394). In-app navigation carries customerName/clusterName
 * in the query, and `useScopeNames` resolves them for a direct entry. Its last-resort
 * fallback is the identifier itself, and printing a UUID as a breadcrumb level is the
 * anti-pattern the direction-C audit called out — so a crumb whose label would be the id is
 * omitted instead. The release has no lookup here (this page only reads revisions), so its
 * crumb is dropped when the query does not carry the name.
 */
const { customerId, clusterId, customerName, clusterName } = useScopeNames();
const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const releaseName = computed(() => (typeof route.query.releaseName === 'string' ? route.query.releaseName : ''));
const crumbs = computed<Array<{ label: string }>>(() => {
  const trail: Array<{ label: string }> = [];
  if (customerName.value && customerName.value !== customerId.value) trail.push({ label: customerName.value });
  if (clusterName.value && clusterName.value !== clusterId.value) trail.push({ label: clusterName.value });
  if (releaseName.value) trail.push({ label: releaseName.value });
  // The last crumb is the current page; PageHeader never links it.
  trail.push({ label: t('values.page.title') });
  return trail;
});
// Direction C: the title names the release and the surface. Without a name the title falls
// back to the catalog label rather than to the release-definition UUID.
const pageTitle = computed(() => (releaseName.value ? `${releaseName.value} Values` : t('values.page.title')));
const roles = computed(() => auth.user?.roles.map((role) => role.toLowerCase()) ?? []);
const canWrite = computed(() => roles.value.some((role) => ['platform_admin', 'release_admin', 'deployer'].includes(role)));
const firstRevision = computed(() => !editor.currentRevision && !editor.parentRevision);
const readOnly = computed(() => !canWrite.value || !editor.canEdit);
const saveBlocked = computed(() => readOnly.value || editor.saving || editor.saveDisabled);
const saveBlockedReason = computed(() => (saveBlocked.value ? t('values.page.saveBlocked') : undefined));
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
  [releaseDefinitionId, clusterId, customerId, canWrite, convergenceMode, prepareToken],
  async ([definitionId, nextClusterId, nextCustomerId, writable, isConvergence, token]) => {
    const organizationId = auth.activeOrganization?.id ?? '';
    // UX-008 (W19): the browser draft is scoped by the whole route, not just the
    // definition id — the same id reached from another cluster/organization must
    // not share a draft slot.
    editor.resetScope(definitionId, nextClusterId, nextCustomerId, organizationId);
    editor.setEditable(writable);
    if (organizationId && nextCustomerId) {
      void authorization.load(organizationId, nextCustomerId);
    }
    if (isConvergence && token) {
      await editor.loadConvergence(token);
    } else {
      await editor.load();
    }
  },
  { immediate: true },
);

/*
 * The YAML/JSON switch really switches the editor language: the store value is the same
 * `language` prop ValuesCodeEditor feeds its CodeMirror language compartment, which is
 * reconfigured in a watcher (ValuesCodeEditor.vue watch(() => props.language)).
 */
function setLanguage(language: EditorLanguage): void {
  editor.setEditorLanguage(language);
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

    <PageHeader :title="pageTitle" subtitle="编辑 canonical values，通过 SecretRef 引用集群 Secret。" :crumbs="crumbs">
      <template #actions>
        <div class="values-actions">
          <span :id="languageLabelId" class="values-actions__label">{{ t('values.page.language') }}</span>
          <div class="seg" role="group" :aria-labelledby="languageLabelId">
            <button
              type="button"
              class="seg__button"
              :class="{ 'seg__button--selected': editor.editorLanguage === 'yaml' }"
              :aria-pressed="editor.editorLanguage === 'yaml'"
              :disabled="readOnly"
              data-testid="values-language-yaml"
              @click="setLanguage('yaml')"
            >
              YAML
            </button>
            <button
              type="button"
              class="seg__button"
              :class="{ 'seg__button--selected': editor.editorLanguage === 'json' }"
              :aria-pressed="editor.editorLanguage === 'json'"
              :disabled="readOnly"
              data-testid="values-language-json"
              @click="setLanguage('json')"
            >
              JSON
            </button>
          </div>
          <button
            type="button"
            class="values-actions__save"
            :disabled="saveBlocked"
            :title="saveBlockedReason"
            data-testid="values-save-new"
            @click="editor.save"
          >
            {{ editor.saving ? t('values.page.saving') : t('values.page.saveNew') }}
          </button>
        </div>
      </template>
    </PageHeader>

    <!-- TASK-267: operation results (saved / submitted / approved / rejected / discarded)
         render through the shared Toast primitive; the page class keeps the box unchanged. -->
    <Toast
      v-if="editor.toast"
      class="notice"
      tone="success"
      testid="values-editor-toast"
      @dismiss="editor.toast = null"
    >
      {{ editor.toast }}
    </Toast>
    <div v-if="editor.restoredDraft" class="notice notice--warning" role="status">
      <span>
        {{ t('values.draft.restored') }}<template v-if="editor.draftSavedAt">{{ t('values.draft.savedAt', { time: formatTimestamp(editor.draftSavedAt) }) }}</template>
      </span>
      <button type="button" @click="editor.discardLocalDraft()">{{ t('values.draft.discard') }}</button>
    </div>
    <div v-else-if="editor.draftNotice" class="notice" role="status">{{ editor.draftNotice }}</div>
    <div v-if="editor.draftSaveFailed" class="notice notice--warning" role="alert">
      {{ t('values.draft.saveFailed') }}
    </div>
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

      <!-- Direction C: the editor and the diff are equal halves of one row at 1440 and
           stack below 900px. The diff is the parent → editor patch, so it belongs beside
           the text it describes rather than under the action bar. -->
      <div class="values-layout">
        <section class="values-panel" aria-labelledby="values-editor-title">
          <header class="values-panel__header">
            <p class="eyebrow">{{ t('values.page.editor') }}</p>
            <h2 id="values-editor-title">{{ t('values.page.title') }}</h2>
          </header>
          <ValuesCodeEditor
            :model-value="editor.editorContent"
            :language="editor.editorLanguage"
            :read-only="readOnly"
            :server-issue="editor.validationIssue"
            @update:model-value="editor.setEditorContent"
          />
          <p v-if="editor.validationIssue" class="validation-message" role="alert">{{ editor.validationIssue.message }}</p>
        </section>
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
/* TASK-394: the console has ONE content width (AppShell main = 1180px). This page used
   96rem, so it was the only surface asking for more than the shell allows — the extra
   width never materialised and the editor still had to work at 1180. */
.values-page { display: grid; gap: 1.25rem; max-width: 1180px; margin: 0 auto; }

/* Header actions: a named segmented group for the language plus the write entry. */
.values-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem; }
.values-actions__label { color: var(--mu); font-size: var(--font-size-xs); font-weight: 700; }
.seg { display: flex; gap: 2px; padding: 3px; border: 1px solid var(--bd); border-radius: var(--r); background: var(--sf2); }
.seg__button { min-height: 2.2rem; padding: 0.3rem 0.75rem; border: 0; border-radius: calc(var(--r) - 3px); background: transparent; color: var(--mu); font-size: var(--font-size-sm); font-weight: var(--font-weight-medium); cursor: pointer; }
.seg__button:hover:not(:disabled) { color: var(--tx); }
.seg__button--selected { background: var(--sf); color: var(--tx); box-shadow: 0 0 0 1px var(--bd); }
/* Disabled is its own neutral state (never the primary fill next to it). */
.seg__button:disabled { background: var(--color-disabled-bg); color: var(--color-disabled-text); cursor: not-allowed; }
.values-actions__save { min-height: 2.4rem; padding: 0.4rem 0.9rem; border: 1px solid var(--pr); border-radius: 0.4rem; background: var(--pr); color: var(--on); font-size: var(--font-size-sm); font-weight: var(--font-weight-medium); cursor: pointer; }
.values-actions__save:disabled { border-color: var(--color-disabled-border); background: var(--color-disabled-bg); color: var(--color-disabled-text); cursor: not-allowed; }

/* Direction C two-column row: editor | diff, equal halves, single column ≤900px. */
.values-layout { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; align-items: start; }
.values-panel { display: grid; align-content: start; gap: 0.55rem; min-width: 0; padding: 1rem; border: 1px solid var(--bd); border-radius: 0.75rem; background: var(--sf); }
.values-panel__header { display: grid; gap: 0.2rem; min-width: 0; }
.values-panel__header h2 { margin: 0; color: var(--tx); font-size: var(--font-size-lg); }
.eyebrow { margin: 0; color: var(--mu); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.validation-message { margin: 0; padding: 0.65rem 0.8rem; border-left: 3px solid var(--color-danger); background: var(--color-danger-soft); color: var(--color-error-strong); font-size: var(--font-size-sm); }
.notice { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 0.75rem 0.9rem; border: 1px solid var(--color-info-border); border-radius: 0.5rem; background: var(--color-info-soft); color: var(--color-info-ink); }
.notice--warning { border-color: var(--color-warning-border-bright); background: var(--color-warning-surface); color: var(--color-warning-ink-strong); }
.notice--error { border-color: var(--color-danger-border-strong); background: var(--color-danger-soft); color: var(--color-error-strong); }
.notice button { min-height: 2.4rem; padding: 0.45rem 0.65rem; border: 1px solid var(--color-border-strong); border-radius: 0.4rem; background: var(--color-surface); }
.discard-dialog__body { margin: 0; }
.discard-dialog__danger { border-color: var(--color-error); background: var(--color-error); color: var(--color-on-accent); }
@media (max-width: 56.25rem) { .values-layout { grid-template-columns: 1fr; } }
@media (max-width: 48rem) { .values-actions { width: 100%; } .values-actions__save { flex: 1 1 auto; } }
</style>
