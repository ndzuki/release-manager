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
    expect(wrapper.get('[data-testid="rollback-validation"]').text()).toContain('必须小于当前版本');

    expect(mockedRollback).not.toHaveBeenCalled();
  });

  /*
   * H5 of docs/delivery-evidence.md §4.2 requires a form field error to carry an
   * `aria-describedby` association. Both directions are asserted on purpose: the
   * control the message is about must advertise the error id, and the other control
   * must NOT (marking every field described/invalid is as misleading as marking
   * none). Both branches are exercised because the message belongs to a different
   * control in each. Every read re-queries: rendering the error replaces the form
   * controls (true before this change as well), so a handle taken earlier goes stale.
   */
  it('associates the validation error with the control it is about', async () => {
    const wrapper = mountDialog();

    // reason branch: the empty-reason message describes the textarea, not the input.
    await wrapper.get('input[name="targetRevision"]').setValue('2');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    const reasonErrorId = wrapper.get('[data-testid="rollback-validation"]').attributes('id');
    expect(reasonErrorId, '校验错误必须有 id，否则 aria-describedby 无处可指').toBeTruthy();
    expect(wrapper.get('textarea[name="reason"]').attributes('aria-describedby')).toBe(reasonErrorId);
    expect(wrapper.get('textarea[name="reason"]').attributes('aria-invalid')).toBe('true');
    expect(wrapper.get('input[name="targetRevision"]').attributes('aria-describedby')).toBeUndefined();

    // targetRevision branch: the revision message describes the input, not the textarea.
    await wrapper.get('textarea[name="reason"]').setValue('because');
    await wrapper.get('input[name="targetRevision"]').setValue('0');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    const targetError = wrapper.get('[data-testid="rollback-validation"]');
    expect(targetError.text()).toContain('1 以上的整数');
    expect(wrapper.get('input[name="targetRevision"]').attributes('aria-describedby')).toBe(targetError.attributes('id'));
    expect(wrapper.get('input[name="targetRevision"]').attributes('aria-invalid')).toBe('true');
    expect(wrapper.get('textarea[name="reason"]').attributes('aria-describedby')).toBeUndefined();
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
