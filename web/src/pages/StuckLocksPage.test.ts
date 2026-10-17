import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
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

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  // Unmount before wiping the DOM: the focus trap keeps a module-level stack of open
  // overlays, and a component that is never unmounted leaves a stale entry behind.
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

/*
 * TASK-296. The release confirmation is an AppDialog panel, which teleports to
 * <body> (ADR-029), so a wrapper's scoped queries cannot see it; only page-level
 * nodes stay on the wrapper. Dialog content is read from the document — the pattern
 * the other page tests use (TrustPolicyPage.test.ts, BindingsPage.test.ts).
 * @testing-library is not a dependency here, so getDialog mirrors getByRole's
 * contract: the unique element carrying role dialog|alertdialog, and a throw when
 * there is none.
 */
function getDialog(): HTMLElement {
  const matches = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]'));
  if (matches.length !== 1) throw new Error(`expected exactly one dialog role, found ${matches.length}`);
  return matches[0]!;
}

/** The accessible name AppDialog gives its panel: aria-labelledby -> the heading. */
function accessibleName(element: HTMLElement): string {
  const id = element.getAttribute('aria-labelledby');
  const labelled = id ? document.getElementById(id) : null;
  return (labelled?.textContent ?? element.getAttribute('aria-label') ?? '').trim();
}

function byTestId<T extends HTMLElement = HTMLElement>(id: string): T {
  const element = document.querySelector<T>(`[data-testid="${id}"]`);
  if (!element) throw new Error(`missing data-testid=${id}`);
  return element;
}

/** Drives a teleported v-model textarea through the real DOM (setValue only sees the wrapper). */
function setField(name: string, value: string): void {
  const input = document.querySelector<HTMLTextAreaElement>(`textarea[name="${name}"]`);
  if (!input) throw new Error(`missing textarea ${name}`);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

function selectRadio(value: string): void {
  const input = document.querySelector<HTMLInputElement>(`input[type="radio"][value="${value}"]`);
  if (!input) throw new Error(`missing radio ${value}`);
  input.checked = true;
  input.dispatchEvent(new Event('change'));
}

function checkAcknowledged(): void {
  const input = document.querySelector<HTMLInputElement>('input[name="acknowledged"]');
  if (!input) throw new Error('missing acknowledged checkbox');
  input.checked = true;
  input.dispatchEvent(new Event('change'));
}

async function submitForm(): Promise<void> {
  const form = document.querySelector<HTMLFormElement>('form.stuck__form');
  if (!form) throw new Error('release form not rendered');
  form.dispatchEvent(new Event('submit'));
  await flushPromises();
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/emergency/stuck-locks', name: 'StuckLocks', component: StuckLocksPage }],
  });
  await router.push('/emergency/stuck-locks');
  await router.isReady();
  // attachTo body (not the default detached div): the dialog contract includes returning
  // focus to the trigger, and document.activeElement only resolves for attached elements.
  const wrapper = mount(StuckLocksPage, { attachTo: document.body, global: { plugins: [router] } });
  mounted.push(wrapper);
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

    await submitForm();
    expect(byTestId('stuck-validation').textContent ?? '').toContain('必须填写释放原因');

    setField('reason', 'because');
    await submitForm();
    expect(byTestId('stuck-validation').textContent ?? '').toContain('必须选择释放模式');

    selectRadio('AUDITED_OVERRIDE');
    await submitForm();
    expect(byTestId('stuck-validation').textContent ?? '').toContain('请先确认已核实集群效果');
    expect(mockedRelease).not.toHaveBeenCalled();
  });

  it('refuses a reason above the server limit of 1000 characters', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    setField('reason', 'x'.repeat(1001));
    selectRadio('NOT_APPLIED_PROVEN');
    checkAcknowledged();
    await submitForm();

    expect(byTestId('stuck-validation').textContent ?? '').toContain('最多 1000');
    expect(mockedRelease).not.toHaveBeenCalled();
  });

  it('refuses evidence above 500 characters', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    setField('reason', 'ok');
    setField('evidence', 'y'.repeat(501));
    selectRadio('NOT_APPLIED_PROVEN');
    checkAcknowledged();
    await submitForm();

    expect(byTestId('stuck-validation').textContent ?? '').toContain('最多 500');
    expect(mockedRelease).not.toHaveBeenCalled();
  });

  it('releases with the chosen mode and closes the dialog on success', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    setField('reason', 'cannot prove, taking over');
    setField('evidence', 'pods still old');
    selectRadio('AUDITED_OVERRIDE');
    checkAcknowledged();
    await submitForm();

    expect(mockedRelease).toHaveBeenCalledWith({
      intentId: 'intent-1',
      reason: 'cannot prove, taking over',
      mode: 'AUDITED_OVERRIDE',
      evidence: 'pods still old',
    });
    expect(document.querySelector('[data-testid="stuck-validation"]')).toBeNull();
    expect(document.querySelector('[role="dialog"], [role="alertdialog"]')).toBeNull();
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

    setField('reason', 'because');
    selectRadio('NOT_APPLIED_PROVEN');
    checkAcknowledged();
    await submitForm();

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

    setField('reason', 'because');
    selectRadio('NOT_APPLIED_PROVEN');
    checkAcknowledged();
    await submitForm();

    expect(wrapper.get('.error-state').text()).toContain('已被释放');
    expect(accessibleName(getDialog())).toContain('释放锁 Deployment');
  });
});

/*
 * TASK-296. The confirmation used to be a hand-rolled div that announced
 * role=alertdialog with a name but omitted aria-modal, the Tab trap, Escape and
 * focus return, and never associated its warning with the dialog. It now renders
 * through AppDialog; these assertions pin the parts the primitive supplies.
 */
describe('release dialog semantics (TASK-296)', () => {
  it('is a modal alertdialog named from the lock path', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    const dialog = getDialog();
    expect(dialog.getAttribute('role')).toBe('alertdialog');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(accessibleName(dialog)).toBe('释放锁 Deployment/api, container=app');
  });

  it('associates the irreversibility warning through aria-describedby', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    const dialog = getDialog();
    const describedBy = dialog.getAttribute('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy!)?.textContent ?? '').toContain('没有撤销');
  });

  it('keeps the validation message inside the dialog', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);

    await submitForm();
    const dialog = getDialog();
    const validation = byTestId('stuck-validation');
    expect(validation.textContent ?? '').toContain('必须填写释放原因');
    expect(dialog.contains(validation)).toBe(true);
  });

  it('closes on Escape without releasing the lock', async () => {
    const wrapper = await mountPage();
    await openDialog(wrapper);
    expect(getDialog()).toBeTruthy();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(mockedRelease).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"], [role="alertdialog"]')).toBeNull();
  });

  // A destructive confirm must stay open while its request is in flight; the owner
  // passes `close-on-escape="!store.releasing"` and the primitive honours it.
  it('keeps the dialog open on Escape while a release is in flight', async () => {
    let resolveRelease: (value: api.ReleaseLockResult) => void = () => {};
    mockedRelease.mockImplementation(
      () =>
        new Promise<api.ReleaseLockResult>((resolve) => {
          resolveRelease = resolve;
        }),
    );
    const wrapper = await mountPage();
    await openDialog(wrapper);

    setField('reason', 'because');
    selectRadio('NOT_APPLIED_PROVEN');
    checkAcknowledged();
    await submitForm();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();
    expect(getDialog()).toBeTruthy();

    resolveRelease({ intentId: 'intent-1', lockReleased: true, effectStatus: 'NOT_APPLIED', auditEventId: 'audit-1' });
    await flushPromises();
    expect(document.querySelector('[role="dialog"], [role="alertdialog"]')).toBeNull();
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
