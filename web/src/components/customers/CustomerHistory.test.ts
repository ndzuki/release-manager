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
