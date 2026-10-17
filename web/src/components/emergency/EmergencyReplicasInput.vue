<script setup lang="ts">
/*
 * Replicas action input (REQ-081 D3=A/D4=A).
 *
 * The wire field is a flat int32 with no presence: 0 means "replicas not
 * requested" and selects the image branch server-side
 * (internal/orchestrator/emergency.go:1095-1099), so the control starts EMPTY
 * (null) and rejects anything below min=1; the parent owns that rule
 * (features/emergency/validation.ts validateReplicasChange). The control only
 * renders the value and the server rejection the parent routed to it.
 *
 * A11y: FormField owns the label/help/error relations (label for, aria-invalid,
 * aria-describedby, role="alert"); the slot props are bound here so the
 * relations hold by construction.
 */
import FormField from '@/components/common/FormField.vue';
import { t } from '@/i18n/messages';
import { computed } from 'vue';
import { REPLICAS_MIN_FOR_CHANGE } from '@/features/emergency/validation';

const props = withDefaults(
  defineProps<{
    value: number | null;
    currentReplicas: number | null;
    max: number;
    available: boolean;
    /** Localized reason why the action is unavailable; null when it is usable. */
    unavailableReason?: string | null;
    /** Server-side rejection routed to this field (D7 double-track). */
    error: string | null;
  }>(),
  { unavailableReason: null },
);

const emit = defineEmits<{ 'update:value': [value: number | null] }>();

function onInput(event: Event): void {
  const raw = (event.target as HTMLInputElement).value;
  emit('update:value', raw === '' ? null : Number(raw));
}

const help = computed(() => t('emergency.replicas.help', { min: REPLICAS_MIN_FOR_CHANGE, max: props.max }));
</script>

<template>
  <div class="replicas-input">
    <p v-if="!available" class="replicas-input__notice" role="status">
      {{ unavailableReason ?? t('emergency.replicas.unavailable') }}
    </p>
    <FormField
      v-else
      :label="t('emergency.replicas.label')"
      :help="help"
      :error="error ?? ''"
      required
    >
      <template #default="{ id, describedBy, invalid, required }">
        <input
          :id="id"
          class="field-input"
          type="number"
          inputmode="numeric"
          :min="REPLICAS_MIN_FOR_CHANGE"
          :max="max"
          step="1"
          :value="value ?? ''"
          :required="required"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          @input="onInput"
        />
      </template>
    </FormField>
    <p v-if="available && currentReplicas !== null" class="replicas-input__current">
      {{ t('emergency.replicas.current', { current: currentReplicas, max }) }}
    </p>
  </div>
</template>

<style scoped>
.replicas-input {
  display: grid;
  gap: var(--space-2);
}

.field-input {
  width: 100%;
  padding: var(--space-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
}

.field-input:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
  box-shadow: var(--focus-ring);
}

.replicas-input__current,
.replicas-input__notice {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}
</style>
