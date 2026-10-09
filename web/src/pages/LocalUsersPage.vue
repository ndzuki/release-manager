<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import FormField from '@/components/common/FormField.vue';
import LoadingState from '@/components/common/LoadingState.vue';
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
      <header class="local-users-page__header">
        <div>
          <p class="eyebrow">{{ t('localUser.page.eyebrow') }}</p>
          <h1>{{ t('localUser.page.title') }}</h1>
          <p class="local-users-page__description">{{ t('localUser.page.description') }}</p>
        </div>
        <button type="button" @click="store.load()">{{ t('action.refresh') }}</button>
      </header>

      <p v-if="store.notice" class="notice" role="status" data-testid="local-users-notice">
        {{ store.notice }}
      </p>

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

      <table v-else class="local-users__table">
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
          <select v-model="role" name="role">
            <option v-for="value in LOCAL_USER_ROLES" :key="value" :value="value">{{ roleLabels[value] }}</option>
          </select>
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
}

.local-users-page__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}

.local-users-page__description {
  color: var(--color-muted);
  max-width: 48rem;
}

.local-users__table {
  border-collapse: collapse;
  width: 100%;
}

.local-users__table th,
.local-users__table td {
  border-bottom: 1px solid var(--color-border);
  padding: var(--space-2) var(--space-3);
  text-align: left;
}

.local-users__create {
  display: grid;
  gap: var(--space-3);
  max-width: 32rem;
}

.local-users__create label {
  display: grid;
  gap: var(--space-1);
}
</style>
