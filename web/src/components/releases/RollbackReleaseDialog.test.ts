import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import RollbackReleaseDialog from './RollbackReleaseDialog.vue';
import * as api from '@/connect/rollback-api';

vi.mock('@/connect/rollback-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, rollbackRelease: vi.fn() };
});

const mockedRollback = vi.mocked(api.rollbackRelease);

const CURRENT = 5;

function mountDialog() {
  return mount(RollbackReleaseDialog, {
    props: {
      open: true,
      releaseDefinitionId: 'def-1',
      releaseName: 'ns/release-a',
      currentRevision: CURRENT,
    },
    global: { stubs: { Teleport: true } },
  });
}

async function fill(wrapper: ReturnType<typeof mountDialog>, target: string, reason: string) {
  await wrapper.get('input[name="targetRevision"]').setValue(target);
  await wrapper.get('textarea[name="reason"]').setValue(reason);
}

beforeEach(() => {
  mockedRollback.mockReset().mockResolvedValue({
    operationId: 'op-9',
    fromRevision: CURRENT,
    toRevision: 2,
    state: 'pending',
  });
});

describe('RollbackReleaseDialog', () => {
  it('shows the release current revision and refuses to submit without a reason', async () => {
    const wrapper = mountDialog();

    expect(wrapper.get<HTMLInputElement>('input[name="currentRevision"]').element.value).toBe(String(CURRENT));

    await wrapper.get('input[name="targetRevision"]').setValue('2');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="rollback-validation"]').text()).toContain('回滚必须填写原因');
    expect(mockedRollback).not.toHaveBeenCalled();
  });

  // The server enforces target_revision >= 1 and target < expected_current_revision.
  it('mirrors the server revision rules before spending a request', async () => {
    const wrapper = mountDialog();

    await fill(wrapper, '0', 'because');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-testid="rollback-validation"]').text()).toContain('1 以上的整数');

    await fill(wrapper, String(CURRENT), 'because');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-testid="rollback-validation"]').text()).toContain('必须小于当前 Revision');

    expect(mockedRollback).not.toHaveBeenCalled();
  });

  it('sends the target, the expected current revision and a fresh idempotency key', async () => {
    const wrapper = mountDialog();

    await fill(wrapper, '2', '  rollback after bad config  ');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    const call = mockedRollback.mock.calls[0]![0];
    expect(call.releaseDefinitionId).toBe('def-1');
    expect(call.targetRevision).toBe(2);
    expect(call.expectedCurrentRevision).toBe(CURRENT);
    expect(call.reason).toBe('rollback after bad config');
    expect(call.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
  });

  it('emits the created operation so the owner can navigate to it', async () => {
    const wrapper = mountDialog();

    await fill(wrapper, '2', 'because');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.emitted('created')?.[0]).toEqual([{ operationId: 'op-9', fromRevision: 5, toRevision: 2 }]);
  });

  /*
   * The server replays the same operation for a repeated Idempotency-Key, so a retry
   * after a lost response must reuse the key; only a changed request may rotate it.
   * Rotating on every submit would let a retry queue a SECOND rollback.
   */
  it('reuses the idempotency key when the retired request is unchanged', async () => {
    mockedRollback.mockRejectedValueOnce(new ConnectError('backend down', Code.Unavailable));
    const wrapper = mountDialog();

    await fill(wrapper, '2', 'because');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    const first = mockedRollback.mock.calls[0]![0].idempotencyKey;

    mockedRollback.mockResolvedValueOnce({ operationId: 'op-9', fromRevision: 5, toRevision: 2, state: 'pending' });
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    const second = mockedRollback.mock.calls[1]![0].idempotencyKey;

    expect(second).toBe(first);
  });

  it('rotates the idempotency key when the request changes', async () => {
    mockedRollback.mockRejectedValueOnce(new ConnectError('backend down', Code.Unavailable));
    const wrapper = mountDialog();

    await fill(wrapper, '2', 'because');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    const first = mockedRollback.mock.calls[0]![0].idempotencyKey;

    mockedRollback.mockRejectedValueOnce(new ConnectError('backend down', Code.Unavailable));
    await wrapper.get('input[name="targetRevision"]').setValue('3');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedRollback.mock.calls[1]![0].idempotencyKey).not.toBe(first);
  });

  // FAILED_PRECONDITION means the cluster's revision view moved; the operator must
  // refresh rather than retry blindly with the same expectation.
  it('explains a stale revision view and offers a retry', async () => {
    // The server's real shape: a machine token followed by detail.
    mockedRollback.mockRejectedValue(
      new ConnectError('revision_conflict: expected revision 5, but current revision is 4', Code.FailedPrecondition),
    );
    const wrapper = mountDialog();

    await fill(wrapper, '2', 'because');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('视图过期');
    expect(wrapper.emitted('created')).toBeUndefined();
  });

  it('explains a pending convergence gate instead of a generic failure', async () => {
    mockedRollback.mockRejectedValue(new ConnectError('release_convergence_pending', Code.FailedPrecondition));
    const wrapper = mountDialog();

    await fill(wrapper, '2', 'because');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('收敛');
  });

  it('clears the previous attempt when reopened', async () => {
    const wrapper = mountDialog();
    await fill(wrapper, '2', 'first attempt');
    await wrapper.setProps({ open: false });
    await wrapper.setProps({ open: true });
    await flushPromises();

    expect(wrapper.get<HTMLInputElement>('input[name="targetRevision"]').element.value).toBe('');
    expect(wrapper.get<HTMLTextAreaElement>('textarea[name="reason"]').element.value).toBe('');
  });
});
