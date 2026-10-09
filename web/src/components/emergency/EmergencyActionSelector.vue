<script setup lang="ts">
/*
 * Emergency action selector (REQ-081 single-action semantics).
 *
 * ExecuteEmergencyChange carries exactly ONE action, so the form must ask which
 * one before it renders a payload. Availability comes from the same target
 * projection the badges use (model.ts availableEmergencyActions), and an
 * unavailable action stays visible but disabled WITH its reason — hiding it
 * would make the absence look like a platform gap instead of a target property
 * (AC-058-01/09).
 */
import { computed } from 'vue';
import { t, type MessageKey } from '@/i18n/messages';
import { availableEmergencyActions, type EmergencyActionKind, type EmergencyTargetDisplay } from '@/features/emergency/model';

const props = defineProps<{
  target: EmergencyTargetDisplay | null;
  selected: EmergencyActionKind;
}>();

const emit = defineEmits<{ 'update:selected': [action: EmergencyActionKind] }>();

const ACTIONS: EmergencyActionKind[] = ['image', 'replicas', 'annotations'];

const ACTION_LABELS: Record<EmergencyActionKind, MessageKey> = {
  image: 'emergency.action.image',
  replicas: 'emergency.action.replicas',
  annotations: 'emergency.action.annotations',
};

const available = computed<EmergencyActionKind[]>(() =>
  props.target ? availableEmergencyActions(props.target) : [],
);

/** Localized reason an action is disabled; null when it is selectable. */
function unavailableReason(action: EmergencyActionKind): string | null {
  const target = props.target;
  if (!target || available.value.includes(action)) return null;
  if (action === 'image') return t('emergency.action.unavailable.image');
  if (action === 'replicas') {
    return target.replicasAction?.availability.reasonCode === 'hpa_managed'
      ? t('emergency.replicas.unavailable.hpa')
      : t('emergency.action.unavailable.replicas');
  }
  return t('emergency.action.unavailable.annotations');
}

function select(action: EmergencyActionKind): void {
  if (!available.value.includes(action)) return;
  emit('update:selected', action);
}
</script>

<template>
  <fieldset class="action-selector">
    <legend>{{ t('emergency.action.label') }}</legend>
    <div class="action-selector__options">
      <label
        v-for="action in ACTIONS"
        :key="action"
        class="action-selector__option"
        :class="{ disabled: !available.includes(action) }"
      >
        <input
          type="radio"
          name="emergency-action"
          :value="action"
          :checked="selected === action"
          :disabled="!available.includes(action)"
          @change="select(action)"
        />
        <span>{{ t(ACTION_LABELS[action]) }}</span>
      </label>
    </div>
    <p v-for="action in ACTIONS.filter((candidate) => !available.includes(candidate))" :key="action" class="hint">
      {{ unavailableReason(action) }}
    </p>
  </fieldset>
</template>

<style scoped>
.action-selector {
  display: grid;
  gap: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  padding: var(--space-3);
}

.action-selector__options {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-4);
}

.action-selector__option {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.action-selector__option.disabled {
  color: var(--color-subtle);
}

.hint {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}
</style>
