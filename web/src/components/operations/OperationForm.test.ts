import { flushPromises, mount, type DOMWrapper, type VueWrapper } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import OperationForm from './OperationForm.vue';
import * as operationApi from '@/connect/operation-api';
import * as valuesApi from '@/connect/values-revision';
import { t } from '@/i18n/messages';
import { useOperationFormStore } from '@/stores/operationForm';
import type { BundleSummary } from '@/types/operation';
import type { ValuesRevision } from '@/types/valuesRevision';

/*
 * REQ-056 D10 (TASK-280). The release-operation form used to take the approved
 * ValuesRevision as a free-text UUID; an operator could therefore submit an id the
 * server refuses (values_not_approved) without ever seeing what was approved. The
 * assertions here are behavioural: the selector renders the loader's revisions, the
 * chosen id is the id that reaches CreateOperation, and an empty list is a stated
 * empty state rather than a silent blank control.
 */
vi.mock('@/connect/operation-api', async (importOriginal) => {
  const original = await importOriginal<typeof operationApi>();
  return { ...original, loadOperationOptions: vi.fn(), createOperation: vi.fn() };
});

vi.mock('@/connect/values-revision', async (importOriginal) => {
  const original = await importOriginal<typeof valuesApi>();
  return { ...original, listApprovedValuesRevisions: vi.fn() };
});

const mockedOptions = vi.mocked(operationApi.loadOperationOptions);
const mockedCreate = vi.mocked(operationApi.createOperation);
const mockedApproved = vi.mocked(valuesApi.listApprovedValuesRevisions);

function bundle(): BundleSummary {
  return {
    bundleId: 'bundle-1',
    name: 'app',
    digest: 'sha256:bundle',
    status: 'validated',
    chartRef: 'oci://registry/app',
    chartVersion: '1.0.0',
    chartDigest: 'sha256:chart',
    images: [{ ref: 'registry/app:v1', digest: 'sha256:image', valuesPath: 'image' }],
    createdAt: null,
  };
}

function revision(id: string, version: number): ValuesRevision {
  return {
    id,
    releaseDefinitionId: 'def-1',
    revision: version,
    stateVersion: '3',
    document: '{}',
    valuesDigest: `sha256:${id}`,
    status: 'approved',
    parentRevisionId: null,
    secretRefs: [],
    createdByUserId: 'u-1',
    createdAt: '2026-10-01T00:00:00Z',
    convergenceTaskIds: [],
    lockedPaths: [],
  };
}

async function mountForm() {
  const store = useOperationFormStore();
  await store.setScope('def-1');
  const wrapper = mount(OperationForm);
  await flushPromises();
  return { wrapper, store };
}

beforeEach(() => {
  sessionStorage.clear();
  setActivePinia(createPinia());
  mockedOptions.mockReset().mockResolvedValue({ bundles: [bundle()] });
  mockedApproved.mockReset().mockResolvedValue([revision('vr-1', 1), revision('vr-2', 2)]);
  mockedCreate.mockReset().mockResolvedValue({
    operationId: 'op-created',
    state: 'preflight',
    preflightId: 'pf-1',
    acceptedAt: null,
  });
});

describe('OperationForm approved ValuesRevision selector', () => {
  it('renders one option per approved revision and submits the selected id', async () => {
    const { wrapper, store } = await mountForm();

    const select = wrapper.get<HTMLSelectElement>('[data-testid="operation-values-revision"]');
    const labels = [...select.element.options].map((option) => option.textContent?.trim() ?? '');
    expect(labels).toEqual([t('operation.form.selectRevision'), 'v1 · vr-1', 'v2 · vr-2']);

    await wrapper.get('[data-testid="operation-bundle"]').setValue('bundle-1');
    await select.setValue('vr-2');
    expect(store.fields.valuesRevisionId).toBe('vr-2');

    await wrapper.get('form').trigger('submit');
    expect(store.step).toBe('confirm');
    await store.submit();
    await flushPromises();

    expect(mockedCreate).toHaveBeenCalledTimes(1);
    expect(mockedCreate.mock.calls[0]![0]).toMatchObject({
      releaseDefinitionId: 'def-1',
      operationType: 'INSTALL',
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-2',
    });
  });

  it('states the empty case and keeps the control out of the tab order', async () => {
    mockedApproved.mockResolvedValue([]);
    const { wrapper } = await mountForm();

    const select = wrapper.get<HTMLSelectElement>('[data-testid="operation-values-revision"]');
    expect(select.element.disabled).toBe(true);
    expect(wrapper.text()).toContain('还没有已审批的 ValuesRevision');

    // The status element is the control's describedby target, not a detached hint.
    const describedBy = select.attributes('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(wrapper.get(`#${describedBy}`).text()).toContain('还没有已审批的 ValuesRevision');
  });

  it('reports a failed revision load as an alert instead of an empty selector', async () => {
    mockedApproved.mockRejectedValue(new Error('revision list unavailable'));
    const { wrapper } = await mountForm();

    const alert = wrapper.get('[role="alert"]');
    expect(alert.text()).toContain('已审批配置版本加载失败');
    expect(alert.text()).toContain('revision list unavailable');
  });

  // REQ-056 D10 invariant at the form boundary: a draft from the old free-text form
  // must not survive behind the disabled, empty selector and become submittable.
  it('does not submit a restored draft revision when nothing is approved', async () => {
    mockedApproved.mockResolvedValue([]);
    sessionStorage.setItem('op-draft:def-1', JSON.stringify({
      operationType: 'INSTALL',
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-typed-before-upgrade',
      patch: [],
      targetRevision: null,
    }));

    const { wrapper, store } = await mountForm();

    const select = wrapper.get<HTMLSelectElement>('[data-testid="operation-values-revision"]');
    expect(select.element.disabled).toBe(true);
    expect(store.fields.valuesRevisionId).toBeNull();

    await wrapper.get('form').trigger('submit');
    expect(store.validate()).toEqual({ valuesRevisionId: '请选择已审批的配置版本' });
    expect(store.step).toBe('form');
    expect(mockedCreate).not.toHaveBeenCalled();
  });
});

/*
 * TASK-275 (A11y subset ②, continued). The bundle / expected-current-revision /
 * target-revision errors used to render inside an implicitly wrapping <label> (they
 * used <span>, so TASK-269's <small>-based scan missed them), which made the sentence
 * part of the control's accessible NAME — pointing aria-describedby at the same node
 * would then announce it twice. FormField (TASK-268) owns the clean shape. Both halves
 * are asserted per field: the name carries only the label copy, the description carries
 * the alert.
 */
function labelContaining(wrapper: VueWrapper, fieldLabel: string): DOMWrapper<Element> {
  const label = wrapper.findAll('label').find((candidate) => candidate.text().includes(fieldLabel));
  if (!label) throw new Error(`no label containing "${fieldLabel}"`);
  return label;
}

function expectErrorDescribesNotNames(wrapper: VueWrapper, fieldLabel: string, description: string): void {
  const label = labelContaining(wrapper, fieldLabel);
  const id = label.attributes('for');
  expect(id, `label "${fieldLabel}" must point at its control with for`).toBeTruthy();
  const control = wrapper.get(`[id="${id}"]`);

  // ① the accessible name is the label text only — the error is not inside it ...
  expect(label.text()).not.toContain(description);
  expect(label.find('.form-field__error').exists()).toBe(false);
  // ② ... the control reaches it as a description instead.
  const describedBy = control.attributes('aria-describedby');
  expect(describedBy).toBeTruthy();
  expect(describedBy!.split(' ')).toContain(`${id}-error`);
  const errorNode = wrapper.get(`[id="${id}-error"]`);
  expect(errorNode.text()).toContain(description);
  expect(errorNode.attributes('role')).toBe('alert');
  expect(control.attributes('aria-invalid')).toBe('true');
}

describe('OperationForm field errors are described, not named', () => {
  it('describes the bundle control with its alert', async () => {
    const { wrapper } = await mountForm();

    await wrapper.get('form').trigger('submit');

    expect(wrapper.get('[data-testid="operation-bundle"]').element.tagName).toBe('SELECT');
    expectErrorDescribesNotNames(wrapper, '制品 Bundle', '请选择制品');
  });

  it('describes the expected-current-revision control with its alert', async () => {
    const { wrapper, store } = await mountForm();
    store.setOperationType('UPGRADE');

    await wrapper.get('form').trigger('submit');

    expectErrorDescribesNotNames(wrapper, '当前 Revision', '无法确定当前 Revision');
  });

  it('describes the target-revision control with its alert', async () => {
    const { wrapper, store } = await mountForm();
    store.setOperationType('ROLLBACK');

    await wrapper.get('form').trigger('submit');

    expectErrorDescribesNotNames(wrapper, '回滚目标 Revision', '请填写回滚目标 Revision');
  });
});
