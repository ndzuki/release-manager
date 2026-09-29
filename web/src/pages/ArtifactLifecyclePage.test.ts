import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ArtifactLifecyclePage from './ArtifactLifecyclePage.vue';
import * as api from '@/connect/cleanup-api';

vi.mock('@/connect/cleanup-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, runCleanup: vi.fn(), unarchiveBundle: vi.fn() };
});

const mockedRun = vi.mocked(api.runCleanup);
const mockedUnarchive = vi.mocked(api.unarchiveBundle);

function result(overrides: Partial<api.CleanupResult> = {}): api.CleanupResult {
  return { deletedBundles: 2, deletedCandidates: 0, deletedPreflights: 1, skippedBundles: 4, errors: [], ...overrides };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/artifacts/lifecycle', name: 'ArtifactLifecycle', component: ArtifactLifecyclePage }],
  });
  await router.push('/artifacts/lifecycle');
  await router.isReady();
  const wrapper = mount(ArtifactLifecyclePage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedRun.mockReset().mockResolvedValue(result());
  mockedUnarchive.mockReset().mockResolvedValue({ bundleId: 'b1', previousStatus: 'validated' });
});

describe('ArtifactLifecyclePage', () => {
  it('states the irreversible consequences before the operator commits', async () => {
    const wrapper = await mountPage();

    const warning = wrapper.get('.lifecycle__warning').text();
    expect(warning).toContain('不可撤销');
    expect(warning).toContain('行数');
    expect(warning).toContain('同步');
  });

  // The action is irreversible: it must not run on a single click.
  it('requires an acknowledgement before it will run the collector', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="cleanup-run"]').trigger('click');
    await flushPromises();

    expect(mockedRun).not.toHaveBeenCalled();
    expect(wrapper.get('[data-testid="cleanup-validation"]').text()).toContain('不可撤销');
  });

  // Retrying the SAME attempt reuses its key so the server can recognise it.
  it('reuses the same idempotency key when the operator retries', async () => {
    mockedRun.mockRejectedValueOnce(new ConnectError('cleanup is already in progress', Code.AlreadyExists));
    const wrapper = await mountPage();
    await wrapper.get('input[name="acknowledged"]').setValue();
    await wrapper.get('[data-testid="cleanup-run"]').trigger('click');
    await flushPromises();

    const first = mockedRun.mock.calls[0]![0];
    mockedRun.mockResolvedValueOnce(result());
    await wrapper.get('.error-state button').trigger('click');
    await flushPromises();

    expect(mockedRun.mock.calls[1]![0]).toBe(first);
  });

  it('runs the collector with a fresh key and shows every counter', async () => {
    const wrapper = await mountPage();
    await wrapper.get('input[name="acknowledged"]').setValue();

    await wrapper.get('[data-testid="cleanup-run"]').trigger('click');
    await flushPromises();

    const key = mockedRun.mock.calls[0]![0];
    expect(key).toMatch(/^[0-9a-f-]{36}$/);
    const counters = wrapper.get('[data-testid="cleanup-result"]').text();
    expect(counters).toContain('删除 bundle');
    expect(wrapper.get('[data-testid="cleanup-bundles"]').text()).toBe('2');
    expect(counters).toContain('跳过 bundle');
  });

  // "Done" and "no problems" are different statements: the errors list must be visible.
  it('surfaces the non-fatal errors of a successful run', async () => {
    mockedRun.mockResolvedValue(result({ errors: ['bundle phase: lock timeout', 'preflight phase: timeout'] }));
    const wrapper = await mountPage();
    await wrapper.get('input[name="acknowledged"]').setValue();

    await wrapper.get('[data-testid="cleanup-run"]').trigger('click');
    await flushPromises();

    const problems = wrapper.get('[data-testid="cleanup-errors"]').text();
    expect(problems).toContain('非致命错误（2）');
    expect(problems).toContain('lock timeout');
    expect(wrapper.get('[data-testid="lifecycle-notice"]').text()).toContain('非致命错误');
  });

  it('explains a repeated idempotency key refusal', async () => {
    mockedRun.mockRejectedValue(new ConnectError('cleanup_already_requested', Code.AlreadyExists));
    const wrapper = await mountPage();
    await wrapper.get('input[name="acknowledged"]').setValue();

    await wrapper.get('[data-testid="cleanup-run"]').trigger('click');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('24 小时');
  });

  it('requires a bundle id and restores with the stored origin status', async () => {
    const wrapper = await mountPage();

    // happy-dom does not synthesize a form submit from a button click.
    await wrapper.get('.lifecycle__panel form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-testid="unarchive-validation"]').text()).toContain('请填写 Bundle ID');
    expect(mockedUnarchive).not.toHaveBeenCalled();

    await wrapper.get('input[name="bundleId"]').setValue('b1');
    await wrapper.get('.lifecycle__panel form').trigger('submit');
    await flushPromises();

    expect(mockedUnarchive).toHaveBeenCalledWith('b1');
    expect(wrapper.get('[data-testid="unarchive-result"]').text()).toContain('validated');
  });
});
