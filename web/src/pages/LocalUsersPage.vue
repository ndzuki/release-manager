<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import FormField from '@/components/common/FormField.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Toast from '@/components/common/Toast.vue';
import { LOCAL_USER_ROLES, type LocalUserRole } from '@/connect/local-user-api';
import { PASSWORD_MAX_BYTES, passwordTooLong } from '@/connect/auth-api';
import { useAuthStore } from '@/stores/auth';
import { useLocalUsersStore } from '@/stores/localUsers';
import { statusLabel } from '@/i18n/status-labels';
import { t } from '@/i18n/messages';

/*
 * Local accounts (REQ-025 / A4's second half).
 *
 * Server-side this whole namespace is platform_admin-only (procedure_policy.go marks
 * CreateLocalUser/GetLocalUser/ListLocalUsers adminOnly), so the page refuses to
 * render anything for other roles rather than letting them click into a 403.
 */
const auth = useAuthStore();
const store = useLocalUsersStore();

const username = ref('');
const password = ref('');
const role = ref<LocalUserRole>('viewer');

// The server hashes with bcrypt, which rejects anything over 72 bytes. Pre-checking
// keeps the failure in the form (with the exact reason) instead of surfacing an opaque
// Internal answer the user would retry forever.
const tooLong = computed(() => passwordTooLong(password.value));
const canSubmit = computed(
  () =>
    username.value.trim().length > 0 &&
    password.value.length > 0 &&
    !tooLong.value &&
    !store.saving,
);

/*
 * Display labels for an account's roles. `platform_admin` is included so an EXISTING
 * admin account reads as 平台管理员 instead of a raw wire value; it is deliberately not
 * in LOCAL_USER_ROLES, because CreateLocalUser rejects granting it (D-16).
 */
const roleLabels: Record<string, string> = {
  platform_admin: t('role.platformAdmin'),
  release_admin: t('role.releaseAdmin'),
  deployer: t('role.deployer'),
  viewer: t('role.viewer'),
};

async function submit(): Promise<void> {
  if (!canSubmit.value) return;
  const created = await store.create({ username: username.value.trim(), password: password.value, role: role.value });
  if (created) {
    username.value = '';
    password.value = '';
    role.value = 'viewer';
  }
}

onMounted(() => {
  if (auth.canManageLocalUsers) void store.load();
});
</script>

<template>
  <section class="local-users-page">
    <ForbiddenState
      v-if="!auth.canManageLocalUsers"
      :message="t('localUser.error.permissionDenied')"
    />

    <template v-else>
      <PageHeader
        :eyebrow="t('localUser.page.eyebrow')"
        :title="t('localUser.page.title')"
        :subtitle="t('localUser.page.description')"
      >
        <template #actions>
          <div class="actions">
            <button type="button" @click="store.load()">{{ t('action.refresh') }}</button>
          </div>
        </template>
      </PageHeader>

      <!--
        TASK-299: the primitive, used without `auto-dismiss`. The store puts both the
        "account ready" confirmation and the stale-cursor recovery note through this
        one field, so the default `info` tone (polite `status`) is the honest mapping
        for the pair; the copy and the testid are unchanged.
      -->
      <Toast v-if="store.notice" class="notice" data-testid="local-users-notice">
        {{ store.notice }}
      </Toast>

      <ErrorState
        v-if="store.failure && !store.hasUsers"
        :title="t('localUser.page.title')"
        :message="store.failure.message"
        :details="store.failure.details"
      >
        <button v-if="store.failure.retryable" type="button" @click="store.load()">
          {{ t('action.retry') }}
        </button>
      </ErrorState>

      <LoadingState v-else-if="store.loading && !store.hasUsers" :message="t('localUser.list.loading')" />

      <EmptyState
        v-else-if="!store.hasUsers"
        :title="t('localUser.page.title')"
        :message="t('localUser.list.empty')"
      />

      <div
        v-else
        class="table-wrap"
        role="region"
        :aria-label="t('localUser.table.scrollRegion')"
        tabindex="0"
      >
        <table class="local-users__table">
        <thead>
          <tr>
            <th scope="col">{{ t('localUser.column.username') }}</th>
            <th scope="col">{{ t('localUser.column.role') }}</th>
            <th scope="col">{{ t('localUser.column.org') }}</th>
            <th scope="col">{{ t('localUser.column.status') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="user in store.users" :key="user.id" :data-testid="`local-user-${user.username}`">
            <td>{{ user.username }}</td>
            <td>{{ user.roles.map((name) => roleLabels[name] ?? name).join('、') }}</td>
            <td>{{ user.orgId }}</td>
            <td>{{ statusLabel('localUser', user.status) }}</td>
          </tr>
        </tbody>
        </table>
      </div>

      <button v-if="store.hasMore" type="button" :disabled="store.loadingMore" @click="store.loadMore()">
        {{ t('localUser.list.loadMore') }}
      </button>

      <form class="local-users__create" @submit.prevent="submit">
        <h2>{{ t('localUser.create.title') }}</h2>
        <FormField :label="t('localUser.create.username')" required>
          <template #default="{ id, describedBy, invalid, required }">
            <input
              :id="id"
              v-model="username"
              name="username"
              autocomplete="off"
              :aria-describedby="describedBy"
              :aria-invalid="invalid"
              :required="required"
            />
          </template>
        </FormField>
        <!--
          The hint and the error are exclusive on this form: FormField renders what it
          is given, so the page keeps the previous behaviour (the hint disappears while
          the error is shown) and aria-describedby follows the rendered message.
        -->
        <FormField
          :label="t('localUser.create.password')"
          :help="tooLong ? '' : t('localUser.create.passwordHint')"
          :error="tooLong ? t('localUser.error.passwordTooLong', { max: String(PASSWORD_MAX_BYTES) }) : ''"
          required
        >
          <template #default="{ id, describedBy, invalid, required }">
            <input
              :id="id"
              v-model="password"
              name="password"
              type="password"
              autocomplete="new-password"
              :aria-describedby="describedBy"
              :aria-invalid="invalid"
              :required="required"
            />
          </template>
        </FormField>
        <label>
          {{ t('localUser.create.role') }}
          <!--
            W2 (TASK-394): the select was the console's last raw native control. The UA
            arrow is painted outside the token palette, so it is suppressed and the caret
            is drawn with the muted text colour instead.
          -->
          <span class="select-wrap">
            <select v-model="role" name="role">
              <option v-for="value in LOCAL_USER_ROLES" :key="value" :value="value">{{ roleLabels[value] }}</option>
            </select>
            <span class="select-wrap__caret" aria-hidden="true" />
          </span>
        </label>
        <p v-if="store.failure && store.hasUsers" class="error" role="alert">{{ store.failure.message }}</p>
        <button type="submit" :disabled="!canSubmit">
          {{ store.saving ? t('localUser.create.submitting') : t('localUser.create.submit') }}
        </button>
      </form>
    </template>
  </section>
</template>

<style scoped>
.local-users-page {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
}

/* PageHeader owns a 20px bottom margin; the grid gap above already spaces the header
   from the table, so the two must not stack. */
.local-users-page :deep(.page-header) {
  margin-bottom: 0;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: var(--space-2);
}

.actions button,
.local-users__create button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text);
  cursor: pointer;
}

.local-users__create button:disabled {
  border-color: var(--color-disabled-border);
  background: var(--color-disabled-bg);
  color: var(--color-disabled-text);
  cursor: not-allowed;
}

/* W2 (TASK-394): the table used to float on the page with only row rules. Direction C
   gives the scroller the frame and the surface, so the table reads as one card. */
.table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--color-surface);
}

.local-users__table {
  border-collapse: collapse;
  width: 100%;
}

.local-users__table th,
.local-users__table td {
  border-bottom: 1px solid var(--color-border-strong);
  padding: var(--space-2) var(--space-3);
  text-align: left;
}

/* W2 (TASK-394): direction-C header — the muted surface and the strong muted ink, so
   the header row separates from the body instead of relying on the UA bold. */
.local-users__table th {
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  letter-spacing: 0.01em;
}

.local-users__create {
  display: grid;
  gap: var(--space-3);
  max-width: 32rem;
  min-width: 0;
}

.local-users__create label {
  display: grid;
  gap: var(--space-1);
}

/* W2 (TASK-394): the create form's controls were unauthored UA widgets; the muted
   surface and the strong border put them on the same vocabulary as the rest of the
   console's forms. */
.local-users__create input,
.local-users__create select {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface-muted);
  color: var(--color-text);
}

/* Direction C select: the UA arrow is suppressed and the caret comes from the muted
   text colour (the same pattern ReleaseInventoryPage established). */
.select-wrap {
  position: relative;
  display: block;
}

.select-wrap select {
  width: 100%;
  padding-right: 30px;
  appearance: none;
  cursor: pointer;
}

.select-wrap__caret {
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
</style>
