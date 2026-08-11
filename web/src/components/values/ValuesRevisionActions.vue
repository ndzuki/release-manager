<script setup lang="ts">
import { computed } from 'vue';
import type { RevisionStatus, ValuesRevision } from '@/types/valuesRevision';

const props = defineProps<{
  revision: ValuesRevision | null;
  saving: boolean;
  approving: boolean;
  saveDisabled: boolean;
  canApprove: boolean;
  selfApproval: boolean;
  readOnly: boolean;
}>();

const emit = defineEmits<{ save: []; approve: []; reject: [] }>();

const statusLabel = computed(() => ({
  draft: 'Draft', pending_approval: 'Pending Approval', approved: 'Approved', rejected: 'Rejected', superseded: 'Superseded',
} satisfies Record<RevisionStatus, string>)[props.revision?.status ?? 'draft']);

function formatTimestamp(value?: string): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '';
}
</script>

<template>
  <section class="revision-actions" aria-labelledby="revision-actions-title">
    <div>
      <p class="eyebrow">Revision workflow</p>
      <h2 id="revision-actions-title">{{ revision ? `Revision ${revision.revision}` : 'New revision' }}</h2>
      <p v-if="revision" class="status-line">
        <span :class="['status', `status--${revision.status}`]">{{ statusLabel }}</span>
        <code>{{ revision.valuesDigest }}</code>
      </p>
      <p v-if="revision?.status === 'rejected' && revision.decidedAt" class="rejection">
        拒绝时间 · {{ formatTimestamp(revision.decidedAt) }}
      </p>
      <p v-if="selfApproval && revision?.status === 'pending_approval'" class="hint">不可审批自己创建的 Revision。</p>
    </div>

    <div class="revision-actions__buttons">
      <button v-if="!readOnly" type="button" class="primary" :disabled="saveDisabled" @click="emit('save')">
        {{ saving ? '保存中…' : '保存为 Draft' }}
      </button>
      <template v-if="canApprove && revision?.status === 'pending_approval'">
        <button type="button" class="success" :disabled="approving" @click="emit('approve')">
          {{ approving ? '审批中…' : 'Approve' }}
        </button>
        <button type="button" class="danger" :disabled="approving" @click="emit('reject')">Reject</button>
      </template>
    </div>
  </section>
</template>

<style scoped>
.revision-actions { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; padding: 1rem; border: 1px solid #e2e8f0; border-radius: 0.75rem; background: #fff; }
h2, p { margin: 0; }
.eyebrow { color: #2563eb; font-size: 0.7rem; font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.status-line { display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem; margin-top: 0.45rem; }
.status-line code { color: #64748b; font-size: 0.75rem; overflow-wrap: anywhere; }
.status { padding: 0.2rem 0.45rem; border-radius: 999px; font-size: 0.7rem; font-weight: 800; }
.status--draft { background: #e0f2fe; color: #075985; }
.status--pending_approval { background: #fef3c7; color: #92400e; }
.status--approved { background: #dcfce7; color: #166534; }
.status--rejected { background: #fee2e2; color: #991b1b; }
.status--superseded { background: #e2e8f0; color: #475569; }
.rejection { margin-top: 0.5rem; color: #991b1b; font-size: 0.85rem; }
.hint { margin-top: 0.5rem; color: #92400e; font-size: 0.8rem; }
.revision-actions__buttons { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 0.65rem; }
button { min-height: 2.5rem; padding: 0.5rem 0.8rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; background: #fff; cursor: pointer; }
button.primary { border-color: #2563eb; background: #2563eb; color: #fff; }
button.success { border-color: #15803d; background: #15803d; color: #fff; }
button.danger { border-color: #b91c1c; color: #b91c1c; }
button:disabled { cursor: not-allowed; opacity: 0.6; }
@media (max-width: 48rem) { .revision-actions { flex-direction: column; } .revision-actions__buttons { justify-content: flex-start; } }
</style>
