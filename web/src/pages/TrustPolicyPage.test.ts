import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import TrustPolicyPage from './TrustPolicyPage.vue';
import { useAuthStore } from '@/stores/auth';
import * as api from '@/connect/trust-api';

vi.mock('@/connect/trust-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    getTrustPolicy: vi.fn(),
    createTrustRoot: vi.fn(),
    rotateTrustRoot: vi.fn(),
    endGrace: vi.fn(),
    retireTrustRoot: vi.fn(),
    revokeTrustRoot: vi.fn(),
  };
});

const mockedPolicy = vi.mocked(api.getTrustPolicy);
const mockedCreate = vi.mocked(api.createTrustRoot);
const mockedRotate = vi.mocked(api.rotateTrustRoot);
const mockedRevoke = vi.mocked(api.revokeTrustRoot);

const PUBLIC_PEM = '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE=\n-----END PUBLIC KEY-----';

function root(keyId: string, state: api.TrustRootStateName): api.TrustRootView {
  return {
    id: `root-${keyId}`,
    keyId,
    issuer: 'CN=dev',
    subjectPattern: 'release-manager',
    publicKeyPem: 'pem',
    state,
    validFrom: '2026-09-28T00:00:00Z',
    graceUntil: state === 'grace' ? '2026-10-05T00:00:00Z' : null,
    createdAt: null,
    updatedAt: null,
    revokedAt: null,
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
 * TASK-294. The trust editor and the revoke confirmation are AppDialog panels, which
 * teleport to <body> (ADR-029), so a wrapper's scoped queries cannot see them; only
 * page-level nodes stay on the wrapper. Dialog content is read from the document — the
 * pattern the other page tests use (DefinitionsPage.test.ts, BindingsPage.test.ts).
 * @testing-library is not a dependency here, so getDialog mirrors getByRole's contract:
 * the unique element carrying role dialog|alertdialog, and a throw when there is none.
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

/** Drives a teleported v-model input through the real DOM (setValue only sees the wrapper). */
function setField(id: string, value: string): void {
  const input = byTestId<HTMLInputElement>(id);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

async function submitEditor(): Promise<void> {
  const form = document.querySelector<HTMLFormElement>('form.trust-editor');
  if (!form) throw new Error('trust editor form not rendered');
  form.dispatchEvent(new Event('submit'));
  await flushPromises();
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/settings/trust', name: 'TrustPolicy', component: TrustPolicyPage }],
  });
  await router.push('/settings/trust');
  await router.isReady();
  // attachTo body (not the default detached div): the dialog contract includes returning
  // focus to the trigger, and document.activeElement only resolves for attached elements.
  const wrapper = mount(TrustPolicyPage, { attachTo: document.body, global: { plugins: [router] } });
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  useAuthStore().$patch({ status: 'authenticated', initialized: true, user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles: ['platform_admin'], activeOrgId: 'org-1' } });
  mockedPolicy.mockReset().mockResolvedValue({
    environment: 'staging',
    version: 5n,
    revocationEpoch: 2n,
    roots: [root('key-new', 'active'), root('key-old', 'grace'), root('key-gone', 'revoked')],
  });
  mockedRevoke.mockReset().mockResolvedValue(undefined);
  mockedCreate.mockReset().mockResolvedValue(root('key-3', 'active'));
  mockedRotate.mockReset().mockResolvedValue({ oldRoot: root('key-new', 'grace'), newRoot: root('key-3', 'active') });
});

describe('TrustPolicyPage', () => {
  it('shows the policy header, every root and the live count', async () => {
    const wrapper = await mountPage();

    expect(mockedPolicy).toHaveBeenCalledWith('staging');
    const text = wrapper.text();
    expect(text).toContain('策略版本');
    expect(text).toContain('5');
    expect(text).toContain('吊销代数');
    expect(wrapper.find('[data-testid="trust-root-key-new"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-root-key-old"]').text()).toContain('宽限期');
    expect(wrapper.find('[data-testid="trust-root-key-gone"]').text()).toContain('已吊销');
  });

  // The action set is the state machine: active can retire/revoke, grace adds
  // end_grace, retired/revoked offer nothing.
  it('offers only the actions the root state accepts', async () => {
    const wrapper = await mountPage();

    expect(wrapper.find('[data-testid="trust-retire-key-new"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-revoke-key-new"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-end_grace-key-new"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="trust-end_grace-key-old"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-retire-key-gone"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="trust-revoke-key-gone"]').exists()).toBe(false);
  });

  it('treats an environment without roots as an informational empty state', async () => {
    mockedPolicy.mockResolvedValue({ environment: 'production', version: 1n, revocationEpoch: 0n, roots: [] });
    const wrapper = await mountPage();

    expect(wrapper.text()).toContain('还没有信任根');
    expect(wrapper.find('.error-state').exists()).toBe(false);
  });

  /*
   * TASK-394 (consistency wave 3). The hand-rolled `.eyebrow` + `<h1>` is gone: the
   * shared PageHeader owns both, keeping the same category word. The environment
   * selector moved out of the header sentence into the filter card, and the table
   * keeps its five columns with the direction-C frame wrapper.
   */
  it('renders the shared header with the 治理 eyebrow and a framed five-column table', async () => {
    const wrapper = await mountPage();

    expect(wrapper.find('.eyebrow').exists()).toBe(false);
    expect(wrapper.get('.page-header__eyebrow').text()).toBe('治理');
    expect(wrapper.get('.page-header__title').text()).toBe('签名信任根');

    const table = wrapper.get('.table-wrap.trust__table-wrap');
    expect(table.findAll('thead th').map((th) => th.text())).toEqual([
      '密钥 ID',
      '状态',
      '签发者 / 主体',
      '生效自 / 宽限至',
      '操作',
    ]);
  });

  it('requires confirmation before revoking, and revokes with the row id', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-revoke-key-new"]').trigger('click');
    expect(mockedRevoke).not.toHaveBeenCalled();

    const dialog = getDialog();
    expect(dialog.getAttribute('role')).toBe('alertdialog');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(accessibleName(dialog)).toBe('确认吊销信任根 key-new？');

    byTestId('trust-confirm-revoke').click();
    await flushPromises();

    expect(mockedRevoke).toHaveBeenCalledWith('staging', 'root-key-new', 'dev-admin');
  });

  it('cancels the revoke confirmation without calling the RPC', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-revoke-key-new"]').trigger('click');
    const cancel = Array.from(getDialog().querySelectorAll('button')).find((b) => b.textContent?.trim() === '取消');
    if (!cancel) throw new Error('cancel button not found in the confirmation dialog');
    cancel.click();
    await flushPromises();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"], [role="alertdialog"]')).toBeNull();
  });

  // Refusing to remove the last live root is a documented server rule; the row must
  // not even offer the action.
  it('disables removal when the environment has a single live root', async () => {
    mockedPolicy.mockResolvedValue({
      environment: 'staging',
      version: 2n,
      revocationEpoch: 0n,
      roots: [root('only', 'active'), root('old', 'retired')],
    });
    const wrapper = await mountPage();

    const retire = wrapper.get<HTMLButtonElement>('[data-testid="trust-retire-only"]');
    expect(retire.element.disabled).toBe(true);
  });

  /*
   * TASK-269. The environment selector sat in the middle of a sentence ("环境 <select>")
   * with no label at all: the prose next to a control is not an accessible name. The
   * word is now the control's <label>, and the assertion is that the reference resolves
   * to the real selector — not merely that a `for` attribute exists.
   */
  it('associates the environment selector with its label', async () => {
    const wrapper = await mountPage();

    const label = wrapper.get('label[for="trust-environment"]');
    const select = wrapper.get<HTMLSelectElement>('#trust-environment');

    expect(label.text()).toBe('环境');
    expect(select.element.tagName).toBe('SELECT');
    expect(select.attributes('name')).toBe('environment');
    // …and it is the selector the policy was loaded for, not a look-alike.
    expect(select.element.value).toBe('staging');
    expect([...select.element.options].map((option) => option.value)).toEqual(['staging', 'production']);
  });
});

describe('trust-root write gating', () => {
  it('hides the rotate/retire/revoke buttons from a non-admin', async () => {
    useAuthStore().$patch({
      status: 'authenticated',
      initialized: true,
      user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-deployer', roles: ['deployer'], activeOrgId: 'org-1' },
    });

    const wrapper = await mountPage();

    // The server marks those procedures adminOnly, so a deployer would get a 403.
    expect(wrapper.findAll('[data-testid^="trust-rotate-"]').length).toBe(0);
    expect(wrapper.findAll('[data-testid^="trust-revoke-"]').length).toBe(0);
    expect(wrapper.find('[data-testid="trust-create-entry"]').exists()).toBe(false);
  });
});

/*
 * TASK-280 D11. Create and rotate were the only two TrustService RPCs with no console
 * entry point. The material contract (api/proto/trust/v1/trust.proto:78,94;
 * internal/trust/types.go:112-114) is PUBLIC key only, so the form submits a pasted
 * public PEM and refuses anything marked PRIVATE before the RPC is reached.
 */
describe('trust-root create and rotate', () => {
  // The editor is teleported by AppDialog, so its fields are driven through the real DOM
  // (the DefinitionsPage.test.ts pattern) rather than through the wrapper.
  async function fillAndSubmit(keyId = 'key-3', issuer = 'CN=next'): Promise<void> {
    setField('trust-editor-key-id', keyId);
    setField('trust-editor-issuer', issuer);
    setField('trust-editor-public-key', PUBLIC_PEM);
    await submitEditor();
  }

  it('creates a root with the environment and operator from the session', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await fillAndSubmit();

    expect(mockedCreate).toHaveBeenCalledWith({
      environment: 'staging',
      operator: 'dev-admin',
      keyId: 'key-3',
      issuer: 'CN=next',
      subjectPattern: '',
      publicKeyPem: PUBLIC_PEM,
    });
    // Success closes the panel; the page reports it through the notice line.
    expect(document.querySelector('[data-testid="trust-editor-submit"]')).toBeNull();
    expect(wrapper.get('[data-testid="trust-notice"]').text()).toContain('key-3');
  });

  it('rotates the clicked root id and carries a grace window for the old key', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-rotate-key-new"]').trigger('click');
    expect(accessibleName(getDialog())).toBe('轮换信任根 key-new');
    await fillAndSubmit();

    const sent = mockedRotate.mock.calls[0]![0];
    expect(sent.oldRootId).toBe('root-key-new');
    expect(sent.environment).toBe('staging');
    expect(sent.operator).toBe('dev-admin');
    expect(sent.publicKeyPem).toBe(PUBLIC_PEM);
    expect(sent.graceUntil).toBeInstanceOf(Date);
    expect(sent.graceUntil!.getTime()).toBeGreaterThan(Date.now());
  });

  it('never sends a private key and names the mistake', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    setField('trust-editor-key-id', 'key-3');
    setField('trust-editor-issuer', 'CN=next');
    setField('trust-editor-public-key', '-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----');
    await submitEditor();

    expect(mockedCreate).not.toHaveBeenCalled();
    const error = document.querySelector('.form-field__error');
    if (!error) throw new Error('expected the private-key field error to render');
    expect(error.textContent).toContain('私钥');
  });

  it('reports a permission refusal to the operator', async () => {
    mockedCreate.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied));
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await fillAndSubmit();

    expect(wrapper.text()).toContain('无权修改信任根');
  });

  /*
   * TASK-267. The notice line is the shared Toast primitive now. Toast hides itself
   * on dismiss and never turns that flag back on, so a notice handed to the SAME
   * instance after a dismissal renders nothing (the documented single-instance limit,
   * pinned in Toast.test.ts).
   *
   * A browser check of "the box disappears on close" cannot see the second half, so
   * this case pins the whole sequence: create succeeds -> dismiss -> create succeeds
   * again, with a different key id so the second notice cannot be the stale text.
   *
   * Boundary worth stating: this page has two routes out of the hidden instance —
   * the page's own `@dismiss="store.notice = ''"` and the store clearing `notice` at
   * the start of every write (stores/trustPolicy.ts createRoot/rotateRoot/apply). The
   * case therefore pins the user-visible sequence, not that one specific line: it
   * stays green if either route survives, and goes red only when both are removed.
   * The page-line-only guard lives in the ValuesEditorPage notice-lifetime case,
   * whose store never clears its source.
   */
  it('shows the trust notice again after it is dismissed and the next create succeeds', async () => {
    mockedCreate
      .mockResolvedValueOnce(root('key-3', 'active'))
      .mockResolvedValueOnce(root('key-4', 'active'));
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await fillAndSubmit('key-3', 'CN=next');
    expect(wrapper.get('[data-testid="trust-notice"]').text()).toContain('key-3');

    await wrapper.get('[data-testid="trust-notice-dismiss"]').trigger('click');
    expect(wrapper.find('[data-testid="trust-notice"]').exists()).toBe(false);

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await fillAndSubmit('key-4', 'CN=next-2');
    const notice = wrapper.get('[data-testid="trust-notice"]');
    expect(notice.text()).toContain('key-4');
    expect(notice.text()).not.toContain('key-3');
  });
});

/*
 * TASK-294. These were the page's two hand-rolled panels: the create/rotate editor (no
 * role, no aria-modal, no accessible name, no Tab trap, Escape inert) and the revoke
 * confirmation (a role and a name, but still no aria-modal, trap, Escape or focus
 * return). Both are AppDialog panels now, so the assertions below are the dialog
 * contract, plus the zero-side-effect check on Escape.
 */
describe('trust panel dialog semantics', () => {
  it('opens the create editor as a labelled modal dialog', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await flushPromises();

    const dialog = getDialog();
    // create is reversible, so it must NOT claim the urgent alertdialog role
    expect(dialog.getAttribute('role')).toBe('dialog');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(accessibleName(dialog)).toBe('新建信任根');
    expect(dialog.querySelector('form.trust-editor')).not.toBeNull();
    // It really left the page tree; a bare inline panel would still be in the wrapper.
    expect(wrapper.find('form.trust-editor').exists()).toBe(false);
  });

  it('closes the create editor on Escape and returns focus to its trigger', async () => {
    const wrapper = await mountPage();
    const trigger = wrapper.get<HTMLButtonElement>('[data-testid="trust-create-entry"]');
    trigger.element.focus();
    await trigger.trigger('click');
    await flushPromises();
    expect(getDialog()).toBeTruthy();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(document.querySelector('[role="dialog"], [role="alertdialog"]')).toBeNull();
    expect(document.querySelector('[data-testid="trust-editor-submit"]')).toBeNull();
    expect(document.activeElement).toBe(trigger.element);
  });

  it('Escape cancels the revoke confirmation without calling the RPC', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-revoke-key-new"]').trigger('click');
    await flushPromises();
    const dialog = getDialog();
    expect(dialog.getAttribute('role')).toBe('alertdialog'); // irreversible: urgent role
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(accessibleName(dialog)).toBe('确认吊销信任根 key-new？');

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(document.querySelector('[role="dialog"], [role="alertdialog"]')).toBeNull();

    // Positive control: the same entry point + explicit confirm DOES revoke, so the
    // assertion above cannot pass by the confirmation being broken outright.
    await wrapper.get('[data-testid="trust-revoke-key-new"]').trigger('click');
    await flushPromises();
    byTestId('trust-confirm-revoke').click();
    await flushPromises();
    expect(mockedRevoke).toHaveBeenCalledWith('staging', 'root-key-new', 'dev-admin');
  });
});
