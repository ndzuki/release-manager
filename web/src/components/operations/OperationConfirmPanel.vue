<script setup lang="ts">
import type { BundleSummary, OperationType, PatchOverride } from '@/types/operation';

defineProps<{
  operationType: OperationType;
  customerName: string;
  clusterName: string;
  releaseName: string;
  bundle: BundleSummary | null;
  valuesRevisionId: string;
  patch: PatchOverride[];
  currentRevision: number | null;
  submitting: boolean;
}>();

defineEmits<{
  cancel: [];
  confirm: [];
}>();
</script>

<template>
  <section class="confirm-panel" aria-labelledby="operation-confirm-title">
    <header>
      <p class="confirm-panel__eyebrow">最终确认</p>
      <h2 id="operation-confirm-title">确认创建 {{ operationType }} 操作</h2>
      <p>提交后将立即进入 Preflight。</p>
    </header>
    <dl class="confirm-panel__summary">
      <div><dt>Release</dt><dd>{{ releaseName }}</dd></div>
      <div><dt>Cluster</dt><dd>{{ customerName }} / {{ clusterName }}</dd></div>
      <div v-if="bundle"><dt>制品</dt><dd>{{ bundle.name }} v{{ bundle.chartVersion }}，{{ bundle.images.length }} 个镜像</dd></div>
      <div><dt>配置版本</dt><dd>{{ valuesRevisionId || '—' }}</dd></div>
      <div><dt>Patch</dt><dd>{{ patch.length > 0 ? `${patch.length} 项` : '无' }}</dd></div>
      <div v-if="operationType !== 'INSTALL'"><dt>当前 Revision</dt><dd>{{ currentRevision ?? '—' }}</dd></div>
    </dl>
    <div class="confirm-panel__actions">
      <button type="button" :disabled="submitting" @click="$emit('cancel')">取消</button>
      <button type="button" class="confirm-panel__confirm" :disabled="submitting" @click="$emit('confirm')">
        {{ submitting ? '创建中…' : '确认创建' }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.confirm-panel { display: grid; gap: 1.25rem; padding: 1.5rem; border: 2px solid #2563eb; border-radius: 0.85rem; background: #eff6ff; }
.confirm-panel header h2, .confirm-panel header p { margin: 0; }
.confirm-panel__eyebrow { color: #1d4ed8; font-size: 0.75rem; font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.confirm-panel__summary { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0; margin: 0; border: 1px solid #bfdbfe; border-radius: 0.65rem; background: #fff; }
.confirm-panel__summary div { padding: 0.85rem 1rem; border-bottom: 1px solid #dbeafe; }
.confirm-panel__summary dt { color: #64748b; font-size: 0.8rem; }
.confirm-panel__summary dd { margin: 0.2rem 0 0; font-weight: 650; overflow-wrap: anywhere; }
.confirm-panel__actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
.confirm-panel__actions button { padding: 0.65rem 1rem; border: 1px solid #94a3b8; border-radius: 0.4rem; background: #fff; }
.confirm-panel__confirm { border-color: #2563eb !important; background: #2563eb !important; color: #fff; }
@media (max-width: 42rem) { .confirm-panel__summary { grid-template-columns: 1fr; } }
</style>
