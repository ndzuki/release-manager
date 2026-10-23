import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import BundlesPage from './BundlesPage.vue';
import * as api from '@/connect/bundle-api';
import * as definitionApi from '@/connect/definition-api';
import { BundleStatus } from '@/gen/common/v1/domain_pb';

vi.mock('@/connect/bundle-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listBundles: vi.fn(), getBundle: vi.fn() };
});

vi.mock('@/connect/definition-api', async (importOriginal) => {
  const original = await importOriginal<typeof definitionApi>();
  return { ...original, listDefinitions: vi.fn() };
});

const mockedList = vi.mocked(api.listBundles);
const mockedDefinitions = vi.mocked(definitionApi.listDefinitions);

function definitionView(id: string, name: string): definitionApi.DefinitionView {
  return {
    id,
    name,
    customerId: 'cust-1',
    clusterId: 'cluster-1',
    namespace: 'apps',
    releaseName: name,
    chartName: 'chart',
    status: 'active',
    version: 1n,
    hpaManaged: false,
    maxEmergencyReplicas: 0,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    approvedAnnotationKeys: [],
    approvedAnnotationKeysViolation: null,
  };
}
const mockedDetail = vi.mocked(api.getBundle);

function summary(id: string): api.BundleSummaryView {
  return {
    id,
    name: `bundle-${id}`,
    digest: 'sha256:abc',
    status: BundleStatus.VALIDATED,
    statusLabel: '已校验',
    chartRef: 'chart',
    chartVersion: '1.0.0',
    chartDigest: 'sha256:def',
    images: [{ ref: 'registry/api', digest: 'sha256:img', valuesPath: 'image.tag' }],
    createdAt: '2026-09-28T00:00:00Z',
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/bundles', name: 'Bundles', component: BundlesPage }],
  });
  await router.push('/bundles');
  await router.isReady();
  const wrapper = mount(BundlesPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedDefinitions.mockReset().mockResolvedValue([
    definitionView('def-1', 'definition-one'),
    definitionView('def-7', 'definition-seven'),
  ]);
  mockedList.mockReset().mockResolvedValue({ bundles: [summary('b1')], nextPageToken: '', totalSize: 1 });
  mockedDetail.mockReset().mockResolvedValue({
    ...summary('b1'),
    gitCommit: 'abc123',
    pipelineId: 'p-1',
    signatureDigest: 'sig',
    sbomDigest: 'sbom',
    provenanceDigest: 'prov',
    signatureRef: 'ref-sig',
    sbomRef: 'ref-sbom',
    provenanceRef: 'ref-prov',
  });
});

describe('BundlesPage', () => {
  // Without the definition the server answers PERMISSION_DENIED (it is the scope AND
  // the authorization key), so the page prompts instead of spending that request.
  it('asks for a release definition before calling the server', async () => {
    const wrapper = await mountPage();

    expect(wrapper.get('[data-testid="bundles-needs-definition"]').text()).toContain('发布定义 ID');
    expect(mockedList).not.toHaveBeenCalled();
  });

  /*
   * TASK-394 (consistency wave 3). The hand-rolled `.eyebrow` + `<h1>` is gone: the
   * shared PageHeader owns both, keeping the same category word. The table keeps its
   * seven columns and gains the direction-C frame wrapper.
   */
  it('renders the shared header with the 发布 eyebrow and a framed seven-column table', async () => {
    const wrapper = await mountPage();

    expect(wrapper.find('.eyebrow').exists()).toBe(false);
    expect(wrapper.get('.page-header__eyebrow').text()).toBe('发布');
    expect(wrapper.get('.page-header__title').text()).toBe('发布 Bundle');

    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-1');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    const table = wrapper.get('.table-wrap.bundles__table-wrap');
    expect(table.findAll('thead th').map((th) => th.text())).toEqual([
      '名称',
      '状态',
      '摘要',
      'Chart',
      '镜像绑定',
      '接收时间',
      '操作',
    ]);
  });

  it('lists bundles once a definition is given', async () => {
    const wrapper = await mountPage();
    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-1');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    const row = wrapper.get('[data-testid="bundle-row-b1"]');
    expect(mockedList).toHaveBeenCalledWith(expect.objectContaining({ releaseDefinitionId: 'def-1' }), '');
    expect(row.text()).toContain('已校验');
    expect(row.text()).toContain('sha256:abc');
    expect(row.text()).toContain('1 个');
    expect(wrapper.get('[data-testid="bundles-total"]').text()).toContain('共 1 个');
  });

  // A refusal must not render as the empty state.
  it('shows the permission refusal instead of the empty state', async () => {
    mockedList.mockRejectedValue(new ConnectError('nope', Code.PermissionDenied));
    const wrapper = await mountPage();
    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-1');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('无权查看');
    expect(wrapper.text()).not.toContain('没有 Bundle');
  });

  it('marks an empty catalogue as empty', async () => {
    mockedList.mockResolvedValue({ bundles: [], nextPageToken: '', totalSize: 0 });
    const wrapper = await mountPage();
    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-1');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    expect(wrapper.text()).toContain('没有 Bundle');
    expect(wrapper.find('.error-state').exists()).toBe(false);
  });

  it('opens the detail panel with provenance and evidence', async () => {
    const wrapper = await mountPage();
    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-1');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    await wrapper.get('[data-testid="bundle-detail-b1"]').trigger('click');
    await flushPromises();

    expect(mockedDetail).toHaveBeenCalledWith('b1', 'def-1');
    expect(wrapper.get('[data-testid="detail-git"]').text()).toBe('abc123');
    expect(wrapper.get('[data-testid="detail-evidence"]').text()).toContain('sbom');

    await wrapper.findAll('button').filter((b) => b.text() === '关闭明细')[0]!.trigger('click');
    expect(wrapper.find('[data-testid="bundle-detail-panel"]').exists()).toBe(false);
  });

  it('loads the next page only when the server offered a cursor', async () => {
    mockedList.mockResolvedValue({ bundles: [summary('b1')], nextPageToken: '', totalSize: 1 });
    const wrapper = await mountPage();
    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-1');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[data-testid="bundles-load-more"]').exists()).toBe(false);

    mockedList.mockResolvedValue({ bundles: [summary('b1'), summary('b2')], nextPageToken: 'token-2', totalSize: 5 });
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    expect(wrapper.find('[data-testid="bundles-load-more"]').exists()).toBe(true);
  });

  it('restarts from page 1 when the filter changes', async () => {
    mockedList.mockResolvedValueOnce({ bundles: [summary('b1')], nextPageToken: 'token-1', totalSize: 3 });
    const wrapper = await mountPage();
    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-1');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    mockedList.mockResolvedValueOnce({ bundles: [summary('b7')], nextPageToken: '', totalSize: 1 });
    await wrapper.get('select[name="releaseDefinitionId"]').setValue('def-7');
    await wrapper.get('form.bundles__filters').trigger('submit');
    await flushPromises();

    // The cursor from the previous filter must never be carried over.
    expect(mockedList).toHaveBeenLastCalledWith({ releaseDefinitionId: 'def-7', chartNameFilter: '', status: null }, '');
    expect(wrapper.find('[data-testid="bundle-row-b7"]').exists()).toBe(true);
  });
});
