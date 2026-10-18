import { ref, type Ref } from 'vue';
import type { AuthorizationAction } from '@/connect/binding-api';

/*
 * TASK-363: the capability-grant form's state moved out of BindingsPage.
 *
 * The contract has NO list-grants RPC, so this panel can only apply one
 * grant/revoke for a (subject, action) pair and report the versions the server
 * answers (rendered from the store's `grantVersions`). A successful submission
 * clears the subject but keeps the capability and the desired state: applying
 * several capabilities to the same person is the common case.
 *
 * `grant` is injected so the local validation and the submit path can be tested
 * without the store.
 */

export interface CapabilityGrantOptions {
  /** The store's setCapability; false when the server refused it. */
  grant: (subject: string, action: AuthorizationAction, revoked: boolean) => Promise<boolean>;
}

export interface CapabilityGrant {
  subject: Ref<string>;
  action: Ref<AuthorizationAction>;
  desiredState: Ref<'grant' | 'revoke'>;
  error: Ref<string>;
  submit: () => Promise<boolean>;
}

export function useCapabilityGrant(options: CapabilityGrantOptions): CapabilityGrant {
  const subject = ref('');
  const action = ref<AuthorizationAction>('release.emergency.resolve');
  const desiredState = ref<'grant' | 'revoke'>('grant');
  const error = ref('');

  async function submit(): Promise<boolean> {
    error.value = '';
    const target = subject.value.trim();
    if (!target) {
      error.value = '请填写主体（用户 ID）';
      return false;
    }
    const ok = await options.grant(target, action.value, desiredState.value === 'revoke');
    if (ok) subject.value = '';
    return ok;
  }

  return { subject, action, desiredState, error, submit };
}
