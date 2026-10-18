import { computed, ref, type Ref } from 'vue';
import type { BindingView } from '@/connect/binding-api';

/*
 * TASK-363: the revoke confirmation's state moved out of BindingsPage.
 *
 * TASK-295: the row button used to call the store directly, so one misclick
 * revoked the binding and its capability grant with no confirmation. The
 * destructive RPC now runs only from `confirm` — never from the row button.
 *
 * TASK-297: on failure the dialog stays open AND the reason is rendered inside
 * its body (`revoke-failure`). The page-level <ErrorState> sits behind the fixed
 * backdrop, so "stays open" alone showed the user an empty confirmation they had
 * to cancel out of before they could read why the revoke failed.
 *
 * TASK-303: arming and closing the confirmation drop the previous attempt's
 * reason, and on failure the reason is copied out of the store's shared slot, so
 * the panel can only ever render the revoke it belongs to. Sharing one slot with
 * the page-level failure meant arming the dialog surfaced another operation's
 * refusal.
 */

export interface BindingRevokeOptions {
  /** The store's versioned revoke; false when the server refused it. */
  revoke: (binding: BindingView) => Promise<boolean>;
  /** The mapped reason the store holds after a refusal. */
  failureMessage: () => string | null;
}

export interface BindingRevoke {
  pending: Ref<BindingView | null>;
  /** Rendered inside the still-open confirmation. */
  failure: Ref<string | null>;
  description: Ref<string>;
  arm: (binding: BindingView) => void;
  close: () => void;
  confirm: () => Promise<boolean>;
}

export function useBindingRevoke(options: BindingRevokeOptions): BindingRevoke {
  const pending = ref<BindingView | null>(null);
  const failure = ref<string | null>(null);
  const description = computed(() =>
    pending.value
      ? `确认后将立即撤销客户 ${pending.value.customerId} 的绑定，并收回该绑定授予的能力，此操作不可恢复。`
      : '',
  );

  /** Set by the row button; the revoke RPC waits for the dialog's confirmation. */
  function arm(binding: BindingView): void {
    failure.value = null;
    pending.value = binding;
  }

  function close(): void {
    failure.value = null;
    pending.value = null;
  }

  async function confirm(): Promise<boolean> {
    const binding = pending.value;
    if (!binding) return false;
    failure.value = null;
    if (!(await options.revoke(binding))) {
      // The store has just recorded the reason for THIS attempt; copy it into the
      // revoke's own slot instead of reading the shared ref from the template.
      failure.value = options.failureMessage();
      return false;
    }
    pending.value = null;
    return true;
  }

  return { pending, failure, description, arm, close, confirm };
}
