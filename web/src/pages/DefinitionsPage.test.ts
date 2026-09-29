import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import DefinitionsPage from './DefinitionsPage.vue';
import * as api from '@/connect/definition-api';

vi.mock('@/connect/definition-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listDefinitions: vi.fn(), updateDefinition: vi.fn() };
});

const mockedList = vi.mocked(api.listDefinitions);
const mockedUpdate = vi.mocked(api.updateDefinition);

function definition(overrides: Partial<api.DefinitionView> = {}): api.DefinitionView {
  return {
    id: 'def-1',
    name: 'e2e-release-target',
    customerId: 'cust-1',
    clusterId: 'cluster-1',
    namespace: 'ns',
    releaseName: 'release-a',
    chartName: 'chart',
    status: 'active',
    version: 3n,
    hpaManaged: false,
    maxEmergencyReplicas: 0,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    ...overrides,
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/definitions', name: 'Definitions', component: DefinitionsPage }],
  });
  await router.push('/definitions');
  await router.isReady();
  const wrapper = mount(DefinitionsPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([definition()]);
  mockedUpdate.mockReset().mockResolvedValue(definition({ version: 4n }));
});

describe('DefinitionsPage', () => {
  it('lists definitions with their mapping count and version', async () => {
    mockedList.mockResolvedValue([
      definition({
        promotionMappings: [
          { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
        ],
      }),
    ]);
    const wrapper = await mountPage();

    const row = wrapper.get('[data-testid="definition-row-def-1"]');
    expect(row.text()).toContain('e2e-release-target');
    expect(row.text()).toContain('1 条');
    expect(row.text()).toContain('3');
  });

  // The proto calls an undecodable payload a contract violation: flag it and do not
  // offer an editor that would write the broken shape back.
  it('flags an undecodable mapping and disables its editor', async () => {
    mockedList.mockResolvedValue([definition({ promotionMappingsViolation: '无法解析' })]);
    const wrapper = await mountPage();

    expect(wrapper.get('[data-testid="definitions-violation"]').text()).toContain('契约违反');
    expect(wrapper.get<HTMLButtonElement>('[data-testid="definition-edit-def-1"]').element.disabled).toBe(true);
  });

  it('adds and removes mapping rows in the editor', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    expect(wrapper.findAll('[data-testid^="mapping-row-"]')).toHaveLength(0);

    await wrapper.get('[data-testid="mapping-add"]').trigger('click');
    await wrapper.get('[data-testid="mapping-add"]').trigger('click');
    expect(wrapper.findAll('[data-testid^="mapping-row-"]')).toHaveLength(2);

    await wrapper.get('[data-testid="mapping-remove-0"]').trigger('click');
    expect(wrapper.findAll('[data-testid^="mapping-row-"]')).toHaveLength(1);
  });

  it('refuses to submit a mapping with missing required fields', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await wrapper.get('[data-testid="mapping-add"]').trigger('click');

    await wrapper.get('form.definitions__editor form, .definitions__editor form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="definitions-validation"]').text()).toContain('第 1 行映射缺少必填项');
    expect(mockedUpdate).not.toHaveBeenCalled();
  });

  it('submits the mappings with the version it read and closes the editor', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await wrapper.get('[data-testid="mapping-add"]').trigger('click');

    await wrapper.get('input[name="kind-0"]').setValue('Deployment');
    await wrapper.get('input[name="name-0"]').setValue('api');
    await wrapper.get('input[name="field-0"]').setValue('image');
    await wrapper.get('input[name="valuesPath-0"]').setValue('image.tag');
    await wrapper.get('.definitions__editor form').trigger('submit');
    await flushPromises();

    expect(mockedUpdate).toHaveBeenCalledWith({
      definitionId: 'def-1',
      expectedVersion: 3n,
      namespace: 'ns',
      releaseName: 'release-a',
      chartName: 'chart',
      promotionMappings: [{ workloadKind: 'Deployment', workloadName: 'api', container: '', field: 'image', valuesPath: 'image.tag' }],
    });
    expect(wrapper.find('.definitions__editor').exists()).toBe(false);
    expect(wrapper.get('[data-testid="definitions-notice"]').text()).toContain('版本 4');
  });

  // TASK-214: clearing IS expressible now — the strings carry presence and the mappings
  // travel in a wrapper — so the page submits the clear instead of refusing it.
  it('submits an empty mapping list when the operator clears them all', async () => {
    mockedList.mockResolvedValue([
      definition({
        promotionMappings: [{ workloadKind: 'Deployment', workloadName: 'api', container: '', field: 'image', valuesPath: 'image.tag' }],
      }),
    ]);
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    await wrapper.get('[data-testid="mapping-remove-0"]').trigger('click');
    await wrapper.get('.definitions__editor form').trigger('submit');
    await flushPromises();

    expect(mockedUpdate).toHaveBeenCalledTimes(1);
    const sent = mockedUpdate.mock.calls[0][0];
    // The page's job is to stop refusing and submit the clear; turning it into the
    // presence-carrying wrapper is definition-api.ts's job, asserted in its own test.
    expect(sent.promotionMappings).toEqual([]);
  });

  it('clears a non-empty identifier field by sending an empty string', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    await wrapper.get('input[name="releaseName"]').setValue('');
    await wrapper.get('.definitions__editor form').trigger('submit');
    await flushPromises();

    expect(mockedUpdate).toHaveBeenCalledTimes(1);
    expect(mockedUpdate.mock.calls[0][0].releaseName).toBe('');
  });

  // After a conflict the list holds the fresh version; the open editor must adopt it,
  // otherwise the hinted retry fails forever with the same stale version.
  it('adopts the refreshed version after a conflict so a retry can succeed', async () => {
    mockedUpdate.mockRejectedValueOnce(new ConnectError('optimistic_lock_conflict: expected 3, current 4', Code.FailedPrecondition));
    mockedList.mockResolvedValue([definition({ version: 4n })]);
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await wrapper.get('.definitions__editor form').trigger('submit');
    await flushPromises();

    expect(wrapper.find('.definitions__editor').exists()).toBe(true);

    // The second attempt must carry the version the server now holds (4), not 3.
    mockedUpdate.mockResolvedValueOnce(definition({ version: 5n }));
    await wrapper.get('.definitions__editor form').trigger('submit');
    await flushPromises();

    expect(mockedUpdate).toHaveBeenLastCalledWith(expect.objectContaining({ expectedVersion: 4n }));
  });

  it('keeps the editor open and reports a version conflict', async () => {
    mockedUpdate.mockRejectedValue(new ConnectError('optimistic_lock_conflict: expected version 3, current 5', Code.FailedPrecondition));
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    await wrapper.get('.definitions__editor form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('版本冲突');
    expect(wrapper.find('.definitions__editor').exists()).toBe(true);
  });
});
