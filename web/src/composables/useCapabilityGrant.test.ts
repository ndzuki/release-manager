import { describe, expect, it, vi } from 'vitest';
import type { AuthorizationAction } from '@/connect/binding-api';
import { useCapabilityGrant } from './useCapabilityGrant';

/*
 * TASK-363: the capability-grant form, pinned at the composable seam.
 * The contract has no list-grants RPC, so what matters is the submitted pair and
 * the revoked flag, plus "the subject is trimmed and cleared only on success".
 */

function setup(overrides: {
  grant?: (subject: string, action: AuthorizationAction, revoked: boolean) => Promise<boolean>;
} = {}) {
  const grant = vi.fn(overrides.grant ?? (async () => true));
  const state = useCapabilityGrant({ grant });
  return { state, grant };
}

describe('useCapabilityGrant', () => {
  it('defaults to the emergency-resolve capability and a grant', async () => {
    const { state } = setup();

    expect(state.action.value).toBe('release.emergency.resolve');
    expect(state.desiredState.value).toBe('grant');
  });

  it('requires a non-blank subject without spending a request', async () => {
    const { state, grant } = setup();

    expect(await state.submit()).toBe(false);
    expect(state.error.value).toBe('请填写主体（用户 ID）');

    state.subject.value = '  ';
    expect(await state.submit()).toBe(false);
    expect(grant).not.toHaveBeenCalled();
  });

  it('trims the subject, passes a grant and clears the subject on success', async () => {
    const { state, grant } = setup();
    state.subject.value = '  user-9  ';

    expect(await state.submit()).toBe(true);
    expect(grant).toHaveBeenCalledWith('user-9', 'release.emergency.resolve', false);
    expect(state.subject.value).toBe('');
  });

  it('passes revoked=true for the revoke desired state and keeps it selected', async () => {
    const { state, grant } = setup();
    state.subject.value = 'user-9';
    state.desiredState.value = 'revoke';
    state.action.value = 'release.values.approve';

    await state.submit();

    expect(grant).toHaveBeenCalledWith('user-9', 'release.values.approve', true);
    expect(state.desiredState.value).toBe('revoke');
    expect(state.action.value).toBe('release.values.approve');
  });

  it('keeps the subject and stays quiet on a server refusal', async () => {
    const { state } = setup({ grant: async () => false });
    state.subject.value = 'user-9';

    expect(await state.submit()).toBe(false);
    expect(state.subject.value).toBe('user-9');
  });

  it('clears the previous validation error on the next submit', async () => {
    const { state } = setup();
    await state.submit();
    expect(state.error.value).not.toBe('');

    state.subject.value = 'user-9';
    await state.submit();

    expect(state.error.value).toBe('');
  });
});
