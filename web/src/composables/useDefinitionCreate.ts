import { ref, type Ref } from 'vue';
import { t } from '@/i18n/messages';
import type { CreateDefinitionInput } from '@/connect/definition-api';

/*
 * TASK-363: the create dialog's form and its two failure slots moved out of
 * DefinitionsPage.
 *
 * The failure is kept in its own slot and rendered INSIDE the dialog: the page-level
 * <ErrorState> sits behind the fixed backdrop, so reading the reason there meant
 * cancelling out of a confirmation the user had not yet completed (TASK-297/TASK-303).
 * `create` and `failureMessage` are injected so both the local validation and the
 * inline failure can be tested without the store.
 */

export interface DefinitionCreateForm {
  customerId: string;
  clusterId: string;
  namespace: string;
  releaseName: string;
  chartName: string;
  enabled: boolean;
}

export interface DefinitionCreateOptions {
  /** The store's create; returns false when the server refused it. */
  create: (input: CreateDefinitionInput) => Promise<boolean>;
  /** The mapped reason the store holds after a refusal. */
  failureMessage: () => string | null;
}

export interface DefinitionCreate {
  creating: Ref<boolean>;
  form: Ref<DefinitionCreateForm>;
  validationError: Ref<string>;
  failure: Ref<string | null>;
  open: () => void;
  close: () => void;
  submit: () => Promise<boolean>;
}

export function blankCreateForm(): DefinitionCreateForm {
  return { customerId: '', clusterId: '', namespace: '', releaseName: '', chartName: '', enabled: true };
}

export function useDefinitionCreate(options: DefinitionCreateOptions): DefinitionCreate {
  const creating = ref(false);
  const validationError = ref('');
  const failure = ref<string | null>(null);
  const form = ref<DefinitionCreateForm>(blankCreateForm());

  function open(): void {
    validationError.value = '';
    failure.value = null;
    form.value = blankCreateForm();
    creating.value = true;
  }

  function close(): void {
    creating.value = false;
  }

  async function submit(): Promise<boolean> {
    validationError.value = '';
    failure.value = null;
    const input = form.value;
    // The server refuses a missing customer/cluster with INVALID_ARGUMENT and derives
    // the definition's NAME from release_name, so an empty one would create a nameless
    // row: all three are required here instead of spending a request.
    if (!input.customerId.trim() || !input.clusterId.trim() || !input.releaseName.trim()) {
      validationError.value = t('definitions.create.required');
      return false;
    }
    const ok = await options.create({
      customerId: input.customerId.trim(),
      clusterId: input.clusterId.trim(),
      namespace: input.namespace.trim(),
      releaseName: input.releaseName.trim(),
      chartName: input.chartName.trim(),
      enabled: input.enabled,
    });
    if (ok) {
      creating.value = false;
      return true;
    }
    failure.value = options.failureMessage();
    return false;
  }

  return { creating, form, validationError, failure, open, close, submit };
}
