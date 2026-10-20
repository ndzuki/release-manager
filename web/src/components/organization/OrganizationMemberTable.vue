<script setup lang="ts">
import { t } from '@/i18n/messages';
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
  <!--
    TASK-367: the member row (UUID + role + version + timestamp + role select and
    remove button) does not fit 375px. Same labelled, focusable scroll region as the
    other wide tables in the console; the page itself must not scroll sideways.
    TASK-394: the region is the shared `.table-wrap` instead of a page-private scroll
    class, and it now carries the frame and the surface.
  -->
  <div
    class="table-wrap"
    role="region"
    :aria-label="t('organization.table.scrollRegion')"
    tabindex="0"
    data-testid="organization-table-scroll"
  >
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
          <!-- TASK-394: the inline role control is the last raw native select in the row.
               The UA arrow is painted outside the token palette, so it is suppressed and
               the caret is drawn with the muted text colour instead. -->
          <span class="organization__select">
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
            <span class="organization__select-caret" aria-hidden="true" />
          </span>
          <button type="button" class="danger" :disabled="saving" @click="emit('remove', member)">移除</button>
        </td>
        <td v-else class="organization__readonly">只读</td>
      </tr>
    </tbody>
    </table>
  </div>
</template>

<style scoped>
/* Direction C table frame (TASK-394): the surface is `--sf`, so the strong border is what
   separates the table from the page. Horizontal containment comes from `.table-wrap`.
   `position: relative` is load-bearing: the per-row `visually-hidden` <label> is absolutely
   positioned, and without a positioned ancestor its containing block is the viewport, so its
   static position inside the scrolled row leaked past the scroll region (measured 418px
   document width with a 375px viewport, 2026-10-18). */
.table-wrap {
  position: relative;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--color-surface);
}

.organization__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

/* Cells stay on one line: the wrapper scrolls, so a narrow viewport gets a scrollbar
   instead of a header label broken into one glyph per line. */
.organization__table th,
.organization__table td {
  padding: 10px 20px;
  border-bottom: 1px solid var(--color-border-strong);
  text-align: left;
  white-space: nowrap;
}

/* Direction-C header: the muted surface and the strong muted ink separate the header row
   from the body instead of relying on the UA bold. */
.organization__table th {
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 600;
  letter-spacing: 0.04em;
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

/* Direction C select: the UA arrow is suppressed and the caret comes from the muted text
   colour (the pattern ReleaseInventoryPage and LocalUsersPage established). */
.organization__select {
  position: relative;
  display: inline-flex;
  align-items: center;
}

.organization__select select {
  padding: var(--space-2) 30px var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface-muted);
  color: var(--color-text);
  appearance: none;
  cursor: pointer;
}

.organization__select-caret {
  position: absolute;
  top: 50%;
  right: 12px;
  width: 6px;
  height: 6px;
  border-right: 1px solid var(--color-muted);
  border-bottom: 1px solid var(--color-muted);
  transform: translateY(-70%) rotate(45deg);
  pointer-events: none;
}

.organization__select select:disabled {
  cursor: not-allowed;
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
  color: var(--color-error-strong);
}

.organization__readonly {
  color: var(--color-muted);
}
</style>
