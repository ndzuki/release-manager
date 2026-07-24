<script setup lang="ts">
import { computed } from 'vue';
import RoutePreview from './RoutePreview.vue';
import type { ArtifactType, FieldViolation, RouteMode, RouteRuleInput, RoutingEndpoints } from '@/types/cluster';
import { ruleKey } from '@/utils/cluster-routing';

const props = defineProps<{
  title: string;
  artifactType: ArtifactType;
  rules: RouteRuleInput[];
  violations?: FieldViolation[];
  conflictingRuleId?: string;
  endpoints: RoutingEndpoints;
  readonly?: boolean;
}>();

const emit = defineEmits<{
  add: [];
  remove: [index: number];
}>();

const modes = computed<Array<{ value: RouteMode; label: string; disabled: boolean; title?: string }>>(() => [
  { value: 'direct', label: 'Direct', disabled: false },
  {
    value: 'pull_through_cache',
    label: 'Pull-through cache',
    disabled: props.artifactType === 'chart',
    title: props.artifactType === 'chart' ? 'Requires successful capability testing before it can be enabled' : undefined,
  },
  { value: 'replicated', label: 'Replicated', disabled: false },
]);

function fieldError(index: number, field: string) {
  const prefix = props.artifactType === 'image' ? 'imageRules' : 'chartRules';
  return props.violations?.find((violation) => violation.field === `${prefix}[${index}].${field}`)?.description;
}

function isConflictingRule(rule: RouteRuleInput, index: number): boolean {
  if (props.conflictingRuleId && rule.id === props.conflictingRuleId) return true;
  return Boolean(fieldError(index, 'sourcePrefix')?.toLowerCase().includes('conflict'));
}
</script>

<template>
  <section class="rule-editor">
    <header class="rule-editor__header">
      <div>
        <h2>{{ title }}</h2>
        <p>{{ artifactType === 'image' ? 'Container image routing rules' : 'Helm chart routing rules' }}</p>
      </div>
      <button v-if="!readonly" type="button" @click="emit('add')">Add rule</button>
    </header>

    <p v-if="rules.length === 0" class="rule-editor__empty">No {{ artifactType }} rules configured.</p>

    <article
      v-for="(rule, index) in rules"
      :key="ruleKey(rule)"
      class="rule-card"
      :class="{ 'rule-card--conflict': isConflictingRule(rule, index) }"
      :data-rule-id="rule.id ?? rule.clientKey"
    >
      <header class="rule-card__header">
        <strong>Rule {{ index + 1 }}</strong>
        <span v-if="rule.id" class="rule-card__id">{{ rule.id }}</span>
        <button v-if="!readonly" type="button" class="danger" @click="emit('remove', index)">Remove</button>
      </header>

      <div class="rule-grid">
        <label>
          Mode
          <select v-model="rule.mode" :disabled="readonly" :aria-invalid="Boolean(fieldError(index, 'mode'))">
            <option
              v-for="mode in modes"
              :key="mode.value"
              :value="mode.value"
              :disabled="mode.disabled"
              :title="mode.title"
            >
              {{ mode.label }}{{ mode.disabled ? ' — unavailable' : '' }}
            </option>
          </select>
          <small v-if="artifactType === 'chart'" class="hint">Pull-through cache requires capability testing.</small>
          <small v-if="fieldError(index, 'mode')" class="field-error">{{ fieldError(index, 'mode') }}</small>
        </label>

        <label>
          Provider
          <input v-model="rule.provider" :readonly="readonly" placeholder="Optional provider name" />
        </label>

        <label>
          Source prefix
          <input
            v-model="rule.sourcePrefix"
            :readonly="readonly"
            placeholder="docker.io/library/"
            :aria-invalid="Boolean(fieldError(index, 'sourcePrefix'))"
          />
          <small v-if="fieldError(index, 'sourcePrefix')" class="field-error">{{ fieldError(index, 'sourcePrefix') }}</small>
        </label>

        <label>
          Target prefix
          <input
            v-model="rule.targetPrefix"
            :readonly="readonly"
            placeholder="harbor.example.com/proxy/"
            :aria-invalid="Boolean(fieldError(index, 'targetPrefix'))"
          />
          <small v-if="fieldError(index, 'targetPrefix')" class="field-error">{{ fieldError(index, 'targetPrefix') }}</small>
        </label>
      </div>

      <RoutePreview :rule="rule" :endpoints="endpoints" />
    </article>
  </section>
</template>

<style scoped>
.rule-editor { display: grid; gap: 1rem; }
.rule-editor__header, .rule-card__header { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.rule-editor__header h2 { margin: 0; }
.rule-editor__header p, .rule-editor__empty { margin: 0.25rem 0 0; color: #64748b; }
.rule-card { display: grid; gap: 1rem; padding: 1rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.rule-card--conflict { border-color: #dc2626; box-shadow: 0 0 0 2px #fecaca; }
.rule-card__id { color: #64748b; font-family: monospace; font-size: 0.75rem; }
.rule-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
label { display: grid; gap: 0.375rem; font-weight: 600; }
input, select { padding: 0.625rem; border: 1px solid #cbd5e1; border-radius: 0.375rem; font: inherit; }
[aria-invalid='true'] { border-color: #dc2626; }
.field-error { color: #dc2626; }
.hint { color: #64748b; font-weight: 400; }
button { padding: 0.5rem 0.75rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; cursor: pointer; }
button.danger { color: #b91c1c; }
@media (max-width: 720px) { .rule-grid { grid-template-columns: 1fr; } }
</style>
