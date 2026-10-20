<script setup lang="ts">
import { t } from '@/i18n/messages';
import { AUTHORIZATION_ACTIONS, type AuthorizationAction, type GrantVersions } from '@/connect/binding-api';

/*
 * TASK-363: the capability-grant form, carved out of BindingsPage unchanged.
 *
 * The page owns the form state (composables/useCapabilityGrant) through the
 * `subject` / `action` / `desiredState` models and decides what submitting means;
 * this component owns only the markup. The root element is still the original
 * `<form class="governance__panel">`.
 *
 * There is NO list-grants RPC, so the hint explains that the panel can only apply
 * one grant/revoke and report the versions the server answered (`versions`).
 */
defineProps<{
  saving: boolean;
  error: string;
  versions: GrantVersions | null;
}>();

const emit = defineEmits<{
  submit: [];
}>();

const subject = defineModel<string>('subject', { required: true });
const action = defineModel<AuthorizationAction>('action', { required: true });
const desiredState = defineModel<'grant' | 'revoke'>('desiredState', { required: true });
</script>

<template>
  <form class="governance__panel" @submit.prevent="emit('submit')">
    <h2>能力授予</h2>
    <p class="governance__hint">
      契约没有「列出既有授权」的 RPC，因此这里只能施加一次授予/撤销并回报服务端返回的版本号。
    </p>
    <label>
      主体（用户 ID）
      <input v-model="subject" name="subject" placeholder="用户 ID" />
    </label>
    <label>
      能力
      <select v-model="action" name="action">
        <option v-for="item in AUTHORIZATION_ACTIONS" :key="item" :value="item">{{ item }}</option>
      </select>
    </label>
    <label>
      动作
      <select v-model="desiredState" name="desiredState">
        <option value="grant">授予</option>
        <option value="revoke">撤销</option>
      </select>
    </label>
    <button type="submit" :disabled="saving">{{ saving ? '提交中…' : '应用' }}</button>
    <p v-if="versions" class="governance__versions" data-testid="grant-versions">
      {{ t('governance.sourcePolicy') }} {{ versions.sourceVersion }} / {{ versions.policyVersion }}
    </p>
    <p v-if="error" class="governance__error" role="alert" data-testid="grant-error">{{ error }}</p>
  </form>
</template>

<style scoped>
/* Direction C card (TASK-394): surface fill + the STRONG border (`--color-border` is a
   hairline and vanishes in the dark theme) + the shared radius. */
.governance__panel {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--color-surface);
}

.governance__panel h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.governance__panel label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.governance__panel input,
.governance__panel select {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.governance__panel button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.governance__hint {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-xs);
}

.governance__versions {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.governance__error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
</style>
