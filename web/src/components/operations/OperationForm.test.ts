import { flushPromises, mount } from '@vue/test-utils';
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
});
