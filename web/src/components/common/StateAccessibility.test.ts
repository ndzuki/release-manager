import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { defineComponent, h } from 'vue';
import EmptyState from './EmptyState.vue';
import ErrorState from './ErrorState.vue';
import ForbiddenState from './ForbiddenState.vue';

/*
 * UX-037 (W14): the three state primitives used FIXED DOM ids
 * (`empty-state-title` etc.). Two of the same state on one page (an empty list
 * inside an empty panel, an error under a stale banner) therefore produced
 * duplicate ids, and the second instance's aria-labelledby resolved to the first
 * instance's heading — or to nothing after the first unmounted.
 *
 * Each instance now derives its own id with Vue's useId(), so the pair (section
 * label reference, heading id) stays self-consistent per instance.
 */
function twoOf(component: unknown, props: Record<string, unknown> = {}) {
  const Host = defineComponent({
    render: () => h('div', [h(component as never, props), h(component as never, props)]),
  });
  return mount(Host);
}

function labelledPairs(wrapper: ReturnType<typeof mount>): Array<{ label: string; heading: string }> {
  return wrapper.findAll('section').map((section) => {
    const label = section.attributes('aria-labelledby') ?? '';
    const heading = section.find('h2').attributes('id') ?? '';
    return { label, heading };
  });
}

describe('state primitives with two instances on one page (UX-037)', () => {
  it('gives each EmptyState its own id pair', () => {
    const pairs = labelledPairs(twoOf(EmptyState));
    expect(pairs).toHaveLength(2);
    expect(new Set(pairs.map((pair) => pair.heading)).size).toBe(2);
    for (const pair of pairs) expect(pair.label).toBe(pair.heading);
  });

  it('gives each ErrorState its own id pair', () => {
    const pairs = labelledPairs(twoOf(ErrorState));
    expect(new Set(pairs.map((pair) => pair.heading)).size).toBe(2);
    for (const pair of pairs) expect(pair.label).toBe(pair.heading);
  });

  it('gives each ForbiddenState its own id pair', () => {
    const pairs = labelledPairs(twoOf(ForbiddenState));
    expect(new Set(pairs.map((pair) => pair.heading)).size).toBe(2);
    for (const pair of pairs) expect(pair.label).toBe(pair.heading);
  });
});
