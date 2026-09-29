import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  createLocalUser,
  listLocalUsers,
  mapLocalUserError,
  type LocalUserFailure,
  type LocalUserRole,
  type LocalUserView,
} from '@/connect/local-user-api';
import { t } from '@/i18n/messages';

/*
 * Local-account state (REQ-025 / A4's second half).
 *
 * The two contract behaviours worth owning here:
 *  - pagination is cursor-based and a stale cursor is INVALID_ARGUMENT, so a failed
 *    page falls back to the first page instead of retrying the same token;
 *  - after a create the list reloads from the first page, because the new account
 *    sorts wherever the server puts it and a local append would lie about order;
 *  - CreateLocalUser is IDEMPOTENT on the username (D-13): a duplicate create returns
 *    the existing account instead of ALREADY_EXISTS, so the notice says "ready", not
 *    "created" — claiming a creation that did not happen would be a false success.
 */
export const useLocalUsersStore = defineStore('localUsers', () => {
  const users = ref<LocalUserView[]>([]);
  const nextCursor = ref('');
  const loading = ref(false);
  const loadingMore = ref(false);
  const saving = ref(false);
  const failure = ref<LocalUserFailure | null>(null);
  const notice = ref('');

  const hasUsers = computed(() => users.value.length > 0);
  const hasMore = computed(() => nextCursor.value !== '');

  async function load(): Promise<void> {
    loading.value = true;
    failure.value = null;
    try {
      const page = await listLocalUsers('');
      users.value = page.users;
      nextCursor.value = page.nextCursor;
    } catch (error) {
      const mapped = mapLocalUserError(error);
      users.value = [];
      nextCursor.value = '';
      failure.value = mapped;
    } finally {
      loading.value = false;
    }
  }

  async function loadMore(): Promise<void> {
    if (!nextCursor.value) return;
    loadingMore.value = true;
    failure.value = null;
    try {
      const page = await listLocalUsers(nextCursor.value);
      users.value = [...users.value, ...page.users];
      nextCursor.value = page.nextCursor;
    } catch (error) {
      const mapped = mapLocalUserError(error, 'loadMore');
      // A cursor the server cannot decode must not be retried: reload from page one.
      if (mapped.code === 'invalid_cursor') {
        failure.value = null;
        await load();
        notice.value = mapped.message;
        return;
      }
      failure.value = mapped;
    } finally {
      loadingMore.value = false;
    }
  }

  async function create(input: { username: string; password: string; role: LocalUserRole }): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const user = await createLocalUser(input);
      notice.value = t('localUser.create.ready', { username: user.username });
      await load();
      return true;
    } catch (error) {
      failure.value = mapLocalUserError(error);
      return false;
    } finally {
      saving.value = false;
    }
  }

  return {
    users,
    loading,
    loadingMore,
    saving,
    failure,
    notice,
    hasUsers,
    hasMore,
    load,
    loadMore,
    create,
  };
});
