import { ref, type Ref } from 'vue';
import type { BindingView } from '@/connect/binding-api';

/*
 * TASK-363: the "new binding" form's state moved out of BindingsPage.
 *
 * CreateBinding is idempotent on the (organization, customer) pair: an active pair
 * answers ALREADY_EXISTS, while reactivating a revoked one is the normal path. The
 * page therefore refuses only a duplicate against the ACTIVE rows, before spending
 * a request, and explains the same thing the server would.
 *
 * `bind` and `activeBindings` are injected so the local validation and the submit
 * path can be tested without the store.
 */

export interface BindingCreateOptions {
  /** The store's bind; false when the server refused it. */
  bind: (customerId: string) => Promise<boolean>;
  /** The current bindings, used for the local duplicate check (active rows only). */
  activeBindings: () => BindingView[];
}

export interface BindingCreate {
  customerId: Ref<string>;
  error: Ref<string>;
  submit: () => Promise<boolean>;
}

export function useBindingCreate(options: BindingCreateOptions): BindingCreate {
  const customerId = ref('');
  const error = ref('');

  async function submit(): Promise<boolean> {
    error.value = '';
    const target = customerId.value.trim();
    if (!target) {
      error.value = '请填写客户 ID';
      return false;
    }
    // The server refuses an active duplicate with ALREADY_EXISTS; catching it here
    // saves a request and gives the same explanation.
    if (options.activeBindings().some((binding) => binding.customerId === target)) {
      error.value = '该客户已绑定（正在生效）';
      return false;
    }
    if (await options.bind(target)) {
      customerId.value = '';
      return true;
    }
    return false;
  }

  return { customerId, error, submit };
}
