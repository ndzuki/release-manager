import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import ClusterTargetSelect from './ClusterTargetSelect.vue';
import RouteRuleEditor from './RouteRuleEditor.vue';
import type { ClusterSummary, RouteRuleInput } from '@/types/cluster';

const endpoints = {
  cacheEndpoint: 'cache.example.com',
  registryEndpoint: 'registry.example.com',
};

function rule(overrides: Partial<RouteRuleInput> = {}): RouteRuleInput {
  return {
    id: 'rule-1',
    clientKey: 'rule-1',
    artifactType: 'image',
    mode: 'direct',
    sourcePrefix: 'docker.io/library/',
    targetPrefix: 'harbor.example.com/proxy/',
    ...overrides,
  };
}

describe('RouteRuleEditor', () => {
  it('highlights the conflicting rule id', () => {
    const wrapper = mount(RouteRuleEditor, {
      props: {
        title: 'Image routes',
        artifactType: 'image',
        rules: [rule(), rule({ id: 'rule-2', clientKey: 'rule-2', sourcePrefix: 'quay.io/acme/' })],
        conflictingRuleId: 'rule-2',
        endpoints,
      },
    });

    expect(wrapper.get('[data-rule-id="rule-2"]').classes()).toContain('rule-card--conflict');
    expect(wrapper.get('[data-rule-id="rule-1"]').classes()).not.toContain('rule-card--conflict');
  });

  it('highlights a conflicting new rule from its field violation', () => {
    const wrapper = mount(RouteRuleEditor, {
      props: {
        title: 'Image routes',
        artifactType: 'image',
        rules: [rule({ id: undefined, clientKey: 'new-rule' })],
        violations: [{
          field: 'imageRules[0].sourcePrefix',
          description: 'Route source prefix conflicts with another rule',
        }],
        endpoints,
      },
    });

    expect(wrapper.get('[data-rule-id="new-rule"]').classes()).toContain('rule-card--conflict');
  });

  it('renders Chart pull-through cache as a disabled option with guidance', () => {
    const wrapper = mount(RouteRuleEditor, {
      props: {
        title: 'Chart routes',
        artifactType: 'chart',
        rules: [rule({ artifactType: 'chart', mode: 'direct' })],
        endpoints,
      },
    });

    const option = wrapper.get('option[value="pull_through_cache"]');
    expect(option.attributes('disabled')).toBeDefined();
    expect(option.attributes('title')).toContain('能力测试');
    expect(wrapper.text()).toContain('Pull-through cache 需要先通过能力测试。');
  });

  it('does not render a credential input', () => {
    const wrapper = mount(RouteRuleEditor, {
      props: {
        title: 'Image routes',
        artifactType: 'image',
        rules: [rule()],
        endpoints,
      },
    });

    expect(wrapper.find('input[name*="credential" i]').exists()).toBe(false);
    expect(wrapper.find('input[name*="token" i]').exists()).toBe(false);
    expect(wrapper.text().toLowerCase()).not.toContain('bearer token');
  });
});

describe('RouteRuleEditor branches the review found uncovered', () => {
  // The empty state, the read-only mode and the inline field error were untested.
  it('shows the empty state from the catalog', () => {
    const wrapper = mount(RouteRuleEditor, {
      props: { title: '镜像路由', artifactType: 'image', rules: [], endpoints, violations: [], readonly: false },
    });

    expect(wrapper.text()).toContain('未配置规则。');
    expect(wrapper.text()).toContain('新增规则');
  });

  it('hides the add and remove controls in read-only mode', () => {
    const wrapper = mount(RouteRuleEditor, {
      props: { title: '镜像路由', artifactType: 'image', rules: [rule()], endpoints, violations: [], readonly: true },
    });

    expect(wrapper.text()).not.toContain('新增规则');
    expect(wrapper.text()).not.toContain('移除');
  });

  it('renders a field violation next to the rule heading', () => {
    const wrapper = mount(RouteRuleEditor, {
      props: {
        title: '镜像路由',
        artifactType: 'image',
        rules: [rule()],
        endpoints,
        violations: [{ field: 'imageRules[0].sourcePrefix', description: 'Route source prefix conflicts with another rule' }],
        readonly: false,
      },
    });

    expect(wrapper.text()).toContain('规则 1');
    // TASK-271 moved the error into FormField's error element (role="alert"), which is
    // a sibling of the label instead of a child of it.
    expect(wrapper.find('.form-field__error').exists()).toBe(true);
  });
});

/*
 * TASK-271 (A11y subset ②): each of the three error-bearing rule fields renders its
 * violation OUTSIDE the `label for`, referenced by aria-describedby. Inside the old
 * wrapping <label> the same text was part of the control's accessible NAME, so a
 * describedby pointing at it would have announced the message twice. Both halves are
 * asserted per field: the name carries only the label copy, the description carries the
 * error.
 */
describe('RouteRuleEditor field errors are described, not named', () => {
  // FormField roots in DOM order: mode, provider, sourcePrefix, targetPrefix.
  const cases = [
    { field: 'mode', description: '模式不受支持', control: 'select', fieldIndex: 0 },
    { field: 'sourcePrefix', description: '来源前缀与另一条规则冲突', control: 'input', fieldIndex: 2 },
    { field: 'targetPrefix', description: '目标前缀不是合法的 URI', control: 'input', fieldIndex: 3 },
  ] as const;

  it.each(cases)('describes the $field control with its alert', ({ field, description, control, fieldIndex }) => {
    const wrapper = mount(RouteRuleEditor, {
      props: {
        title: '镜像路由',
        artifactType: 'image',
        rules: [rule()],
        endpoints,
        violations: [{ field: `imageRules[0].${field}`, description }],
      },
    });

    const fieldRoot = wrapper.findAll('.form-field')[fieldIndex]!;
    const label = fieldRoot.get('label');
    const controlElement = fieldRoot.get(control);
    const describedBy = controlElement.attributes('aria-describedby');

    // ① the accessible name is the label text only — the error is not in it ...
    expect(label.attributes('for')).toBe(controlElement.attributes('id'));
    expect(label.text()).not.toContain(description);
    expect(label.find('.form-field__error').exists()).toBe(false);
    // ② ... it is reachable as the control's description instead.
    expect(describedBy).toBeTruthy();
    expect(fieldRoot.get(`#${describedBy}`).text()).toContain(description);
    expect(fieldRoot.get(`#${describedBy}`).attributes('role')).toBe('alert');
    expect(controlElement.attributes('aria-invalid')).toBe('true');
  });
});

describe('ClusterTargetSelect', () => {
  it('marks disabled clusters and prevents selecting them', () => {
    const clusters: ClusterSummary[] = [
      { id: 'active', name: 'Active cluster', customerId: 'customer-1', enabled: true, version: 1, routeCount: 0 },
      { id: 'disabled', name: 'Disabled cluster', customerId: 'customer-1', enabled: false, version: 2, routeCount: 0 },
    ];
    const wrapper = mount(ClusterTargetSelect, {
      props: { clusters, modelValue: '' },
    });

    const disabled = wrapper.get('option[value="disabled"]');
    expect(disabled.attributes('disabled')).toBeDefined();
    expect(disabled.text()).toContain('disabled');
  });
});
