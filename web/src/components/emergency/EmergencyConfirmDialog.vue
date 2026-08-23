<script setup lang="ts">
// Frozen-intent confirmation dialog (plan v3 Step 4, AC-058-15/16/17):
// renders a read-only summary of the frozen intent, requires the explicit
// risk acceptance checkbox, and submits through the store seam. Closing the
// dialog keeps the page form and the frozen key (reopening the same intent
// reuses it — handled by the store).
import type { CandidateArtifactDisplay, WorkloadRefDisplay } from '@/features/emergency/model';

defineProps<{
  open: boolean;
  workload: WorkloadRefDisplay | null;
  container: string;
  artifact: CandidateArtifactDisplay | null;
  reason: string;
  policy: string;
  riskAccepted: boolean;
  submitting: boolean;
  error: { code: string; message: string } | null;
}>();

const emit = defineEmits<{
  cancel: [];
  confirm: [];
  'update:risk-accepted': [accepted: boolean];
}>();
</script>

<template>
  <div v-if="open" class="dialog-backdrop" role="dialog" aria-modal="true" aria-label="确认紧急变更">
    <div class="dialog">
      <h2>确认紧急变更</h2>
      <dl class="intent-summary">
        <template v-if="workload">
          <dt>目标</dt>
          <dd>{{ workload.kind }} {{ workload.namespace }}/{{ workload.name }}</dd>
        </template>
        <dt>容器</dt>
        <dd>{{ container || '—' }}</dd>
        <dt>制品</dt>
        <dd>{{ artifact ? `${artifact.repository}（${artifact.digest}）` : '—' }}</dd>
        <dt>收敛策略</dt>
        <dd>{{ policy }}</dd>
        <dt>原因</dt>
        <dd class="reason">{{ reason }}</dd>
      </dl>
      <p class="risk-hint">确认后系统将立即投递变更命令（可能影响线上服务），且此确认不替代收敛审批。</p>
      <label class="risk-row">
        <input
          type="checkbox"
          :checked="riskAccepted"
          @change="emit('update:risk-accepted', ($event.target as HTMLInputElement).checked)"
        />
        我已确认变更内容与风险
      </label>
      <p v-if="error" class="error-text" role="alert">{{ error.message }}</p>
      <div class="dialog-actions">
        <button type="button" class="secondary" :disabled="submitting" @click="emit('cancel')">取消</button>
        <button type="button" class="primary" :disabled="submitting || !riskAccepted" @click="emit('confirm')">
          {{ submitting ? '提交中…' : '确认提交' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog-backdrop { position: fixed; inset: 0; display: flex; align-items: center; justify-content: center; background: rgb(15 23 42 / 50%); z-index: 40; }
.dialog { width: min(560px, 92vw); display: grid; gap: 0.9rem; padding: 1.25rem; border-radius: 0.5rem; background: #fff; }
.intent-summary { display: grid; grid-template-columns: max-content 1fr; gap: 0.4rem 0.9rem; }
.intent-summary dt { color: #64748b; }
.intent-summary dd { margin: 0; }
.reason { white-space: pre-wrap; }
.risk-hint { color: #92400e; font-size: 0.85rem; }
.risk-row { display: flex; gap: 0.5rem; align-items: center; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 0.6rem; }
.primary { padding: 0.5rem 1rem; border: 0; border-radius: 0.375rem; background: #dc2626; color: #fff; }
.primary:disabled { background: #fca5a5; }
.secondary { padding: 0.5rem 1rem; border: 1px solid #cbd5e1; border-radius: 0.375rem; background: #fff; }
.error-text { color: #b91c1c; }
</style>
