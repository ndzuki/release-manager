import { ref, type Ref } from 'vue';
import { t } from '@/i18n/messages';
import type { OrganizationView } from '@/connect/organization-api';

/*
 * TASK-363: the create-organization dialog's state moved out of OrganizationPage.
 *
 * Create is platform_admin-only and NOT idempotent (no deduplication key, names are
 * not unique), so a retry after a lost response creates a second organization. The
 * page only decides WHEN to open the dialog; this composable owns the reset on open,
 * the required-name check and the inline failure slot. `create` and `failureMessage`
 * are injected so both paths can be tested without mounting the page or faking
 * Connect.
 */

export interface OrganizationCreateOptions {
  /** The organizations store's create; null when the server refused it. */
  create: (name: string) => Promise<OrganizationView | null>;
  /** The mapped reason the store holds after a refusal. */
  failureMessage: () => string | null;
}

export interface OrganizationCreate {
  creating: Ref<boolean>;
  name: Ref<string>;
  validationError: Ref<string>;
  failure: Ref<string | null>;
  open: () => void;
  close: () => void;
  submit: () => Promise<boolean>;
}

export function useOrganizationCreate(options: OrganizationCreateOptions): OrganizationCreate {
  const creating = ref(false);
  const name = ref('');
  const validationError = ref('');
  const failure = ref<string | null>(null);

  function open(): void {
    validationError.value = '';
    failure.value = null;
    name.value = '';
    creating.value = true;
  }

  function close(): void {
    creating.value = false;
  }

  async function submit(): Promise<boolean> {
    validationError.value = '';
    failure.value = null;
    const trimmed = name.value.trim();
    if (!trimmed) {
      validationError.value = t('organization.create.name');
      return false;
    }
    if (await options.create(trimmed)) {
      creating.value = false;
      return true;
    }
    failure.value = options.failureMessage();
    return false;
  }

  return { creating, name, validationError, failure, open, close, submit };
}
