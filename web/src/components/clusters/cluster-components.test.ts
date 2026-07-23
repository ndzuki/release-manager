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
    expect(option.attributes('title')).toContain('capability testing');
    expect(wrapper.text()).toContain('Pull-through cache requires capability testing.');
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
