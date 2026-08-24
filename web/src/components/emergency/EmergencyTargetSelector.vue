<script setup lang="ts">
// Workload/action availability selection (plan v3 Step 4). Availability comes
// from the server target projection; reason codes explain the blocking cause
// (AC-058-01/09). No v-html anywhere.
import type { EmergencyTargetDisplay } from '@/features/emergency/model';

const props = defineProps<{
  targets: EmergencyTargetDisplay[];
  selectedUid: string | null;
  loading: boolean;
  error: string | null;
}>();

const emit = defineEmits<{ select: [uid: string] }>();

function select(target: EmergencyTargetDisplay): void {
  emit('select', target.workloadRef.uid);
}

function imageAvailabilityLabel(target: EmergencyTargetDisplay): string {
  const available = target.imageActions.some((action) => action.availability.available);
  return available ? '镜像可变更' : '镜像不可用';
}

function replicasAvailabilityLabel(target: EmergencyTargetDisplay): string {
  const action = target.replicasAction;
  if (!action) return '副本不可用';
  if (action.availability.reasonCode === 'hpa_managed') return '副本由 HPA 管理';
  if (!action.availability.available) return '副本不可用';
  return '副本可变更';
}
</script>

<template>
  <div class="target-selector" role="radiogroup" aria-label="选择变更目标">
    <p v-if="loading" class="hint">正在加载候选目标…</p>
    <p v-else-if="error" class="hint error-text">{{ error }}</p>
    <p v-else-if="targets.length === 0" class="hint">该发布定义没有可变更的 workload</p>
    <label
      v-for="target in targets"
      :key="target.workloadRef.uid"
      class="target-card"
      :class="{ selected: target.workloadRef.uid === props.selectedUid }"
    >
      <input
        type="radio"
        name="emergency-target"
        :checked="target.workloadRef.uid === props.selectedUid"
        @change="select(target)"
      />
      <div class="target-body">
        <strong>{{ target.workloadRef.kind }} {{ target.workloadRef.namespace }}/{{ target.workloadRef.name }}</strong>
        <span class="target-actions">
          <span>{{ imageAvailabilityLabel(target) }}</span>
          <span :class="{ blocked: !target.replicasAction?.availability.available }">
            {{ replicasAvailabilityLabel(target) }}
          </span>
          <span
            v-for="annotation in target.annotationActions"
            :key="annotation.key"
            :class="{ blocked: !annotation.availability.available }"
          >
            注解 {{ annotation.key }}
          </span>
        </span>
      </div>
    </label>
  </div>
</template>

<style scoped>
.target-selector { display: grid; gap: 0.5rem; }
.target-card { display: flex; gap: 0.75rem; align-items: flex-start; padding: 0.75rem; border: 1px solid #e2e8f0; border-radius: 0.5rem; background: #fff; cursor: pointer; }
.target-card.selected { border-color: #2563eb; background: #eff6ff; }
.target-body { display: grid; gap: 0.35rem; }
.target-actions { display: flex; flex-wrap: wrap; gap: 0.5rem; color: #475569; font-size: 0.85rem; }
.blocked { color: #b91c1c; }
.hint { color: #64748b; }
.error-text { color: #b91c1c; }
</style>
