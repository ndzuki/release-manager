<script setup lang="ts">
import type { OrganizationRole } from '@/connect/organization-api';

/*
 * TASK-363: the add-member form, carved out of OrganizationPage unchanged.
 *
 * The page owns the form state (composables/useMemberAdd) through the two v-model
 * contracts and decides what submitting means; this component owns only the markup.
 * `roles` is the roles the caller may actually grant (the server still owns the
 * decision), so a release_admin never sees platform_admin in the dropdown. The root
 * element is still the original `<form class="organization__add">`.
 */
defineProps<{
  saving: boolean;
  roles: OrganizationRole[];
  error: string;
}>();

const emit = defineEmits<{
  submit: [];
}>();

const userId = defineModel<string>('userId', { required: true });
const role = defineModel<OrganizationRole>('role', { required: true });
</script>

<template>
  <form class="organization__add" @submit.prevent="emit('submit')">
    <h2>添加成员</h2>
    <label>
      用户 ID
      <input v-model="userId" name="userId" placeholder="用户 ID" />
    </label>
    <label>
      角色
      <select v-model="role" name="role">
        <option v-for="item in roles" :key="item" :value="item">{{ item }}</option>
      </select>
    </label>
    <button type="submit" :disabled="saving">{{ saving ? '提交中…' : '添加' }}</button>
    <p v-if="error" class="organization__error" role="alert" data-testid="organization-add-error">{{ error }}</p>
  </form>
</template>

<style scoped>
.organization__add {
  display: flex;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.organization__add h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.organization__add label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.organization__add input,
.organization__add select {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.organization__add button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.organization__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
