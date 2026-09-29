import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import StuckLocksPage from './StuckLocksPage.vue';
import * as api from '@/connect/stuck-lock-api';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/stuck-lock-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listStuckLocks: vi.fn(), releaseEmergencyLock: vi.fn() };
});

const mockedList = vi.mocked(api.listStuckLocks);
const mockedRelease = vi.mocked(api.releaseEmergencyLock);

function lock(intentId: string): api.StuckLockView {
  return {
    intentId,
    operationId: `op-${intentId}`,
    releaseDefinitionId: 'def-1',
    action: '设置容器镜像',
    lockPathSummary: 'Deployment/api, container=app',
    terminalAt: '2026-09-28T10:00:00Z',
    stuckSince: '2026-09-28T10:05:00Z',
    observeTimeoutDisplay: '5m0s',
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/emergency/stuck-locks', name: 'StuckLocks', component: StuckLocksPage }],
  });
  await router.push('/emergency/stuck-locks');
  await router.isReady();
  const wrapper = mount(StuckLocksPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

async function openDialog(wrapper: Awaited<ReturnType<typeof mountPage>>) {
  await wrapper.get('[data-testid="stuck-lock-intent-1"] button').trigger('click');
  await flushPromises();
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([lock('intent-1')]);
  mockedRelease.mockReset().mockResolvedValue({ intentId: 'intent-1', lockReleased: true, effectStatus: 'NOT_APPLIED', auditEventId: 'audit-1' });
});

describe('StuckLocksPage', () => {
  it('lists the stuck locks with their lock path and observation window', async () => {
    const wrapper = await mountPage();

    const row = wrapper.get('[data-testid="stuck-lock-intent-1"]');
    expect(row.text()).toContain('Deployment/api, container=app');
    expect(row.text()).toContain('设置容器镜像');
    expect(row.text()).toContain('5m0s');
    expect(row.text()).toContain('op-intent-1');
  });

  it('shows the steady state as an informational empty list', async () => {
    mockedList.mockResolvedValue([]);
    const wrapper = await mountPage();

    expect(wrapper.text()).toContain('没有卡住的锁');
    expect(wrapper.find('.error-state').exists()).toBe(false);
  });

  it('filters by release definition', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="releaseDefinitionId"]').setValue('def-7');
    await wrapper.get('form.stuck__filter').trigger('submit');
    await flushPromises();

    expect(mockedList).toHaveBeenLastCalledWith('def-7');
  });

  // Releasing is irreversible, so the dialog forces a mode and a reason.
  // Validation order mirrors the server's (reason -> evidence -> mode) so the first
  // message matches what a direct API call would report.
  it('requires a reason, then a mode, then the acknowledgement', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-testid="stuck-validation"]').text()).toContain('必须填写释放原因');

    await wrapper.get('textarea[name="reason"]').setValue('because');
    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-testid="stuck-validation"]').text()).toContain('必须选择释放模式');

    await wrapper.get('input[value="AUDITED_OVERRIDE"]').setValue();
    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-testid="stuck-validation"]').text()).toContain('请先确认已核实集群效果');
    expect(mockedRelease).not.toHaveBeenCalled();
  });

  it('refuses a reason above the server limit of 1000 characters', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    await wrapper.get('textarea[name="reason"]').setValue('x'.repeat(1001));
    await wrapper.get('input[value="NOT_APPLIED_PROVEN"]').setValue();
    await wrapper.get('input[name="acknowledged"]').setValue();
    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="stuck-validation"]').text()).toContain('最多 1000');
    expect(mockedRelease).not.toHaveBeenCalled();
  });

  it('refuses evidence above 500 characters', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    await wrapper.get('textarea[name="reason"]').setValue('ok');
    await wrapper.get('textarea[name="evidence"]').setValue('y'.repeat(501));
    await wrapper.get('input[value="NOT_APPLIED_PROVEN"]').setValue();
    await wrapper.get('input[name="acknowledged"]').setValue();
    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="stuck-validation"]').text()).toContain('最多 500');
    expect(mockedRelease).not.toHaveBeenCalled();
  });

  it('releases with the chosen mode and closes the dialog on success', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    await wrapper.get('textarea[name="reason"]').setValue('cannot prove, taking over');
    await wrapper.get('textarea[name="evidence"]').setValue('pods still old');
    await wrapper.get('input[value="AUDITED_OVERRIDE"]').setValue();
    await wrapper.get('input[name="acknowledged"]').setValue();
    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();

    expect(mockedRelease).toHaveBeenCalledWith({
      intentId: 'intent-1',
      reason: 'cannot prove, taking over',
      mode: 'AUDITED_OVERRIDE',
      evidence: 'pods still old',
    });
    expect(wrapper.find('[data-testid="stuck-validation"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('释放锁 Deployment');
  });

  // W5: the operator must be able to hand the server-side correlation id to the logs.
  it('shows the request id and reason code in the technical details', async () => {
    mockedRelease.mockRejectedValue(
      new ConnectError('refused', Code.FailedPrecondition, {
        'X-Reason-Code': 'release_busy',
        'X-Request-ID': 'req-det-7',
      }),
    );
    const wrapper = await mountPage();
    await openDialog(wrapper);

    await wrapper.get('textarea[name="reason"]').setValue('because');
    await wrapper.get('input[value="NOT_APPLIED_PROVEN"]').setValue();
    await wrapper.get('input[name="acknowledged"]').setValue();
    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();

    const details = wrapper.get('.error-state details').text();
    expect(details).toContain('requestId=req-det-7');
    expect(details).toContain('reason=release_busy');
    // and the copy comes from the shared catalog, not a local sentence
    expect(wrapper.get('.error-state').text()).toContain('有正在进行的操作');
  });

  it('keeps the dialog open and reports a refusal from the server', async () => {
    mockedRelease.mockRejectedValue(new ConnectError('not found', Code.NotFound, { 'X-Reason-Code': 'intent_not_found' }));
    const wrapper = await mountPage();
    await openDialog(wrapper);

    await wrapper.get('textarea[name="reason"]').setValue('because');
    await wrapper.get('input[value="NOT_APPLIED_PROVEN"]').setValue();
    await wrapper.get('input[name="acknowledged"]').setValue();
    await wrapper.get('.stuck__dialog form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('已被释放');
    expect(wrapper.text()).toContain('释放锁 Deployment');
  });
});

describe('write affordance gating', () => {
  it('hides the release-lock button from a viewer', async () => {
    useAuthStore().$patch({
      status: 'authenticated',
      initialized: true,
      user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-reader', roles: ['viewer'], activeOrgId: 'org-1' },
    });
    mockedList.mockResolvedValue([lock('i-1')]);

    const wrapper = await mountPage();

    expect(wrapper.text()).not.toContain('释放锁');
  });

  it('offers it to a deployer', async () => {
    useAuthStore().$patch({
      status: 'authenticated',
      initialized: true,
      user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-deployer', roles: ['deployer'], activeOrgId: 'org-1' },
    });
    mockedList.mockResolvedValue([lock('i-1')]);

    const wrapper = await mountPage();

    expect(wrapper.text()).toContain('释放锁');
  });
});
