import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import CustomerHistory from './CustomerHistory.vue';

/*
 * W17 follow-up (review of 512b09f1): the customer history used to print
 * `new Date(...).toLocaleString()` with seconds, and the shared formatter's
 * default `short` style dropped them. A customer's timeline is evidence too, so
 * two events inside one minute must stay distinguishable.
 */
describe('CustomerHistory timestamps (W17 follow-up)', () => {
  it('renders the event time with seconds', () => {
    const at = new Date(2026, 9, 17, 20, 2, 46);
    const wrapper = mount(CustomerHistory, {
      props: {
        events: [{
          id: 'e-1',
          customerId: 'customer-1',
          eventType: 'customer_created',
          createdAt: at.toISOString(),
        }],
      },
    });

    expect(wrapper.get('time').text()).toMatch(/20:02:46/);
  });

  it('omits the time element when the event carries no timestamp', () => {
    const wrapper = mount(CustomerHistory, {
      props: {
        events: [{ id: 'e-2', customerId: 'customer-1', eventType: 'customer_created' }],
      },
    });

    expect(wrapper.find('time').exists()).toBe(false);
  });
});

/*
 * W15 (TASK-325): ListCustomerEvents has no cursor, so a long-lived customer's
 * history is loaded whole. Above the threshold the list windows its rows; the
 * payload is unchanged and the list still reports its real size.
 */
describe('CustomerHistory list capacity (W15)', () => {
  function events(count: number) {
    return Array.from({ length: count }, (_, index) => ({
      id: `e-${index}`,
      customerId: 'customer-1',
      eventType: index % 2 === 0 ? 'customer_created' : 'customer_disabled',
      createdAt: new Date(2026, 9, 17, 20, 2, 46).toISOString(),
    }));
  }

  it('renders every event while the history is small', () => {
    const wrapper = mount(CustomerHistory, { props: { events: events(4) } });

    const viewport = wrapper.get('.customer-history__viewport');
    expect(wrapper.findAll('li')).toHaveLength(4);
    expect(viewport.classes()).not.toContain('customer-history__viewport--virtual');
    expect(viewport.attributes('tabindex')).toBeUndefined();
  });

  it('windows a long history and announces the full size', () => {
    const wrapper = mount(CustomerHistory, { props: { events: events(120) } });

    const viewport = wrapper.get('.customer-history__viewport');
    expect(viewport.classes()).toContain('customer-history__viewport--virtual');
    expect(viewport.attributes('role')).toBe('region');
    expect(viewport.attributes('tabindex')).toBe('0');

    const rendered = wrapper.findAll('li');
    expect(rendered.length).toBeLessThan(120);
    expect(rendered[0]!.attributes('aria-setsize')).toBe('120');
    expect(rendered[0]!.attributes('aria-posinset')).toBe('1');
  });

  it('keeps the announced position in step with the scroll offset', async () => {
    const wrapper = mount(CustomerHistory, { props: { events: events(120) } });

    const viewport = wrapper.get('.customer-history__viewport');
    viewport.element.scrollTop = 1440; // 20 rows of 72px (64px estimate + 8px gap)
    await viewport.trigger('scroll');

    const rendered = wrapper.findAll('li');
    expect(Number(rendered[0]!.attributes('aria-posinset'))).toBeGreaterThan(1);
    expect(Number.parseInt(wrapper.get<HTMLElement>('.customer-history__list').element.style.paddingTop || '0', 10)).toBeGreaterThan(0);
  });
});
