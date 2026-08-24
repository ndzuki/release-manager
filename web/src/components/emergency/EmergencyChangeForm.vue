<script setup lang="ts">
// Emergency change form (plan v3 Step 4): reason (D8/D12 plain multiline
// textarea with the fixed placeholder) and convergence policy (AC-058-14:
// REQUIRE_PROMOTION is only selectable when every affected field has a
// unique promotion mapping; otherwise it degrades to REVERT).
// Field-level validation errors render inline (D7 double-track).
import { computed } from 'vue';
import { REASON_MAX_BYTES, utf8ByteLength, validateReason } from '@/features/emergency/validation';
import type { ConvergencePolicy } from '@/features/emergency/model';

const props = defineProps<{
  reason: string;
  convergencePolicy: ConvergencePolicy;
  requirePromotionAvailable: boolean;
  mappingComplete: boolean;
  /** Typed server error for the summary bar (D7). */
  submitError: { code: string; message: string } | null;
}>();

const emit = defineEmits<{
  'update:reason': [value: string];
  'update:policy': [value: ConvergencePolicy];
}>();

const reasonValidation = computed(() => validateReason(props.reason));
const reasonBytes = computed(() => utf8ByteLength(props.reason.trim()));

const effectivePolicy = computed<ConvergencePolicy>(() =>
  props.requirePromotionAvailable ? props.convergencePolicy : 'REVERT_ON_NEXT_RECONCILE',
);

function setPolicy(policy: ConvergencePolicy): void {
  emit('update:policy', policy);
}
</script>

<template>
  <form class="change-form" @submit.prevent>
    <label>
      <span class="field-label">变更原因（事故 ID / 现象 / 影响范围）</span>
      <textarea
        class="field-input reason-input"
        rows="4"
        :value="reason"
        placeholder="事故 ID / 现象 / 影响范围"
        @input="emit('update:reason', ($event.target as HTMLTextAreaElement).value)"
      />
      <span class="byte-count" :class="{ over: !reasonValidation.valid }">
        {{ reasonBytes }} / {{ REASON_MAX_BYTES }} 字节
      </span>
      <span v-if="!reasonValidation.valid" class="error-text">{{ reasonValidation.message }}</span>
    </label>

    <fieldset class="policy-fieldset">
      <legend>收敛策略</legend>
      <label :class="{ disabled: !requirePromotionAvailable }">
        <input
          type="radio"
          name="convergence-policy"
          value="REQUIRE_PROMOTION"
          :checked="effectivePolicy === 'REQUIRE_PROMOTION'"
          :disabled="!requirePromotionAvailable"
          @change="setPolicy('REQUIRE_PROMOTION')"
        />
        REQUIRE_PROMOTION — 生成收敛任务，需异人审批
      </label>
      <label>
        <input
          type="radio"
          name="convergence-policy"
          value="REVERT_ON_NEXT_RECONCILE"
          :checked="effectivePolicy === 'REVERT_ON_NEXT_RECONCILE'"
          @change="setPolicy('REVERT_ON_NEXT_RECONCILE')"
        />
        REVERT_ON_NEXT_RECONCILE — 下次对账时回退
      </label>
      <p v-if="!requirePromotionAvailable" class="hint">
        当前变更字段缺少完整 Promotion Mapping，已固定为 REVERT（AC-058-14）。
      </p>
      <p v-else-if="mappingComplete" class="hint">全部变更字段均有唯一 Promotion Mapping。</p>
    </fieldset>

    <p v-if="submitError" class="summary-bar" role="alert">{{ submitError.message }}</p>
  </form>
</template>

<style scoped>
.change-form { display: grid; gap: 1rem; }
.field-label { display: block; margin-bottom: 0.25rem; color: #475569; font-size: 0.85rem; }
.field-input { width: 100%; padding: 0.5rem; border: 1px solid #cbd5e1; border-radius: 0.375rem; }
.reason-input { resize: vertical; }
.byte-count { font-size: 0.8rem; color: #94a3b8; }
.byte-count.over { color: #b91c1c; }
.policy-fieldset { display: grid; gap: 0.4rem; border: 1px solid #e2e8f0; border-radius: 0.5rem; padding: 0.75rem; }
.policy-fieldset label.disabled { color: #94a3b8; }
.hint { color: #64748b; font-size: 0.85rem; }
.error-text { color: #b91c1c; font-size: 0.85rem; }
.summary-bar { padding: 0.6rem 0.75rem; border: 1px solid #fecaca; border-radius: 0.375rem; background: #fef2f2; color: #b91c1c; }
</style>
