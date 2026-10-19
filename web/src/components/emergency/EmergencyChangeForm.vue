<script setup lang="ts">
// Emergency change form (plan v3 Step 4): reason (D8/D12 plain multiline
// textarea with the fixed placeholder) and convergence policy (AC-058-14:
// REQUIRE_PROMOTION is only selectable when every affected field has a
// unique promotion mapping; otherwise it degrades to REVERT).
// Field-level validation errors render inline (D7 double-track).
import { computed } from 'vue';
import FormField from '@/components/common/FormField.vue';
import { REASON_MAX_CHARS, codePointLength, validateReason } from '@/features/emergency/validation';
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
// TASK-325 W3: the counter is in Unicode characters, the unit the server
// enforces (5-500 via utf8.RuneCountInString), so the number the operator sees
// is the number the server checks.
const reasonChars = computed(() => codePointLength(props.reason.trim()));
// TASK-275: the counter stays a description (it is a live hint, not the name);
// FormField cannot render it because it carries the `over` state class.
const charCountId = 'emergency-reason-char-count';

const effectivePolicy = computed<ConvergencePolicy>(() =>
  props.requirePromotionAvailable ? props.convergencePolicy : 'REVERT_ON_NEXT_RECONCILE',
);

function setPolicy(policy: ConvergencePolicy): void {
  emit('update:policy', policy);
}
</script>

<template>
  <form class="change-form" @submit.prevent>
    <!--
     TASK-275: the reason error used to render INSIDE the wrapping <label>, so it was part
     of the textarea's accessible NAME and a describedby pointing at it would announce it
     twice. FormField (TASK-268) owns the shape; the counter (a hint with its own `over`
     state) rides in the slot and is appended to the control's description.
    -->
    <FormField
      label="变更原因（事故 ID / 现象 / 影响范围）"
      :error="reasonValidation.valid ? '' : reasonValidation.message"
    >
      <template #default="{ id, describedBy, invalid, disabled }">
        <textarea
          :id="id"
          class="field-input reason-input"
          rows="4"
          :value="reason"
          :aria-describedby="[describedBy, charCountId].filter(Boolean).join(' ')"
          :aria-invalid="invalid"
          :disabled="disabled"
          placeholder="事故 ID / 现象 / 影响范围"
          @input="emit('update:reason', ($event.target as HTMLTextAreaElement).value)"
        />
        <span :id="charCountId" class="char-count" :class="{ over: !reasonValidation.valid }">
          {{ reasonChars }} / {{ REASON_MAX_CHARS }} 字符
        </span>
      </template>
    </FormField>

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
/* TASK-275: `.field-label` and `.error-text` are gone — the label and the alert are FormField's. */
.field-input { width: 100%; padding: 0.5rem; border: 1px solid var(--color-border-strong); border-radius: 0.375rem; }
.reason-input { resize: vertical; }
.char-count { font-size: var(--font-size-sm); color: var(--color-subtle); }
.char-count.over { color: var(--color-error); }
.policy-fieldset { display: grid; gap: 0.4rem; border: 1px solid var(--color-border); border-radius: 0.5rem; padding: 0.75rem; }
.policy-fieldset label.disabled { color: var(--color-subtle); }
.hint { color: var(--color-muted); font-size: var(--font-size-sm); }
.summary-bar { padding: 0.6rem 0.75rem; border: 1px solid var(--color-danger-border-soft); border-radius: 0.375rem; background: var(--color-danger-soft); color: var(--color-error); }
</style>
