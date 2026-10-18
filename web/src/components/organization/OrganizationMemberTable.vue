<script setup lang="ts">
import { ORGANIZATION_ROLES, type OrganizationMemberView, type OrganizationRole } from '@/connect/organization-api';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * TASK-363: the member list, carved out of OrganizationPage unchanged.
 *
 * Purely presentational: the page owns the stores and decides what a role change or
 * a removal means; this component only reports which row asked for them (the role
 * change forwards the native event so the page can put the dropdown back when the
 * server refuses the change). The root element is still the original
 * `<table class="organization__table">`, so the DOM (columns, row testids, the
 * read-only column and both action controls) is the one the page rendered.
 */
defineProps<{
  members: OrganizationMemberView[];
  canWrite: boolean;
  saving: boolean;
  grantableRoles: OrganizationRole[];
}>();

const emit = defineEmits<{
  'change-role': [member: OrganizationMemberView, event: Event];
  remove: [member: OrganizationMemberView];
}>();
</script>

<template>
  <table class="organization__table">
    <thead>
      <tr>
        <th scope="col">用户</th>
        <th scope="col">角色</th>
        <th scope="col">版本</th>
        <th scope="col">加入时间</th>
        <th scope="col">操作</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="member in members" :key="member.userId" :data-testid="`member-row-${member.userId}`">
        <td><code>{{ member.userId }}</code></td>
        <td>
          <span class="organization__role" :class="`organization__role--${member.role}`">{{ member.role }}</span>
        </td>
        <td>{{ member.optimisticVersion }}</td>
        <td>{{ formatTimestamp(member.createdAt) }}</td>
        <td v-if="canWrite" class="organization__actions">
          <label class="visually-hidden" :for="`role-${member.userId}`">修改 {{ member.userId }} 的角色</label>
          <select
            :id="`role-${member.userId}`"
            :value="member.role"
            :disabled="saving"
            @change="emit('change-role', member, $event)"
          >
            <option v-for="role in ORGANIZATION_ROLES" :key="role" :value="role" :disabled="!grantableRoles.includes(role)">
              {{ role }}
            </option>
          </select>
          <button type="button" class="danger" :disabled="saving" @click="emit('remove', member)">移除</button>
        </td>
        <td v-else class="organization__readonly">只读</td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.organization__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.organization__table th,
.organization__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.organization__role {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.organization__role--platform_admin {
  background: var(--color-info-subtle);
  color: var(--color-info-ink-strong);
}

.organization__actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.organization__actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.organization__actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.organization__readonly {
  color: var(--color-muted);
}
</style>
