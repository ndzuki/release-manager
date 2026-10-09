import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { defineComponent, h } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import FormField from './FormField.vue';
import LocalUsersPage from '@/pages/LocalUsersPage.vue';
import * as api from '@/connect/local-user-api';
import { useAuthStore } from '@/stores/auth';

/*
 * FormField owns three relations, and every one of them is asserted on rendered
 * DOM rather than on props:
 *   1. <label for> === control id
 *   2. aria-describedby === the ids of the messages actually rendered
 *   3. aria-invalid === "true" exactly while an error is shown
 * The last describe block mounts the real page that adopted the component, because
 * the wiring is handed to the caller through slot props: a component test alone
 * would only prove that the caller *could* wire it, not that one does.
 */

vi.mock('@/connect/local-user-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listLocalUsers: vi.fn(), createLocalUser: vi.fn() };
});

const mockedList = vi.mocked(api.listLocalUsers);

/**
 * The slot props FormField hands to its control. Spelled out because `h()` does not
 * infer them from `defineSlots`, and the test must pass them on verbatim — that
 * pass-through IS the wiring under test.
 */
interface FieldSlotProps {
  id: string;
  describedBy: string | undefined;
  invalid: true | undefined;
  required: boolean;
  disabled: boolean;
}

/** Mounts the component the way a page consumes it: the slot props are passed through. */
function mountField(props: Record<string, unknown> = {}) {
  return mount(FormField, {
    props: { label: '发布名称', ...props },
    slots: {
      default: (slotProps: FieldSlotProps) =>
        h('input', {
          id: slotProps.id,
          'aria-describedby': slotProps.describedBy,
          'aria-invalid': slotProps.invalid,
          required: slotProps.required,
          disabled: slotProps.disabled,
        }),
    },
  });
}

describe('FormField', () => {
  it('points its label at the control with for/id', () => {
    const wrapper = mountField();

    const label = wrapper.get('label');
    const input = wrapper.get('input');

    expect(label.attributes('for')).toBeTruthy();
    expect(input.attributes('id')).toBe(label.attributes('for'));
    expect(label.text()).toContain('发布名称');
  });

  it('keeps the generated id stable per instance', () => {
    const host = defineComponent({
      render() {
        return h('div', [
          h(FormField, { label: '一' }, { default: (slotProps: FieldSlotProps) => h('input', { id: slotProps.id }) }),
          h(FormField, { label: '二' }, { default: (slotProps: FieldSlotProps) => h('input', { id: slotProps.id }) }),
        ]);
      },
    });
    const wrapper = mount(host);
    const [first, second] = wrapper.findAll('input');

    expect(first!.attributes('id')).toBeTruthy();
    expect(second!.attributes('id')).toBeTruthy();
    expect(first!.attributes('id')).not.toBe(second!.attributes('id'));
  });

  it('describes the control with the help text and leaves it valid', () => {
    const wrapper = mountField({ help: '不超过 63 个字符' });

    const input = wrapper.get('input');
    const describedBy = input.attributes('aria-describedby');

    expect(describedBy).toBeTruthy();
    expect(wrapper.get(`#${describedBy}`).text()).toBe('不超过 63 个字符');
    expect(input.attributes('aria-invalid')).toBeUndefined();
  });

  it('marks the control invalid and describes it with the alert when there is an error', () => {
    const wrapper = mountField({ error: '名称不能为空' });

    const input = wrapper.get('input');
    const describedBy = input.attributes('aria-describedby');

    expect(input.attributes('aria-invalid')).toBe('true');
    expect(describedBy).toBeTruthy();

    const alert = wrapper.get('[role="alert"]');
    expect(alert.attributes('id')).toBe(describedBy);
    expect(alert.text()).toBe('名称不能为空');
  });

  it('lists both messages in DOM order when an error and a hint are present', () => {
    const wrapper = mountField({ help: '帮助文本', error: '错误文本' });

    const ids = wrapper.get('input').attributes('aria-describedby')!.split(' ');

    expect(ids).toHaveLength(2);
    expect(ids[0]).toMatch(/-error$/);
    expect(ids[1]).toMatch(/-help$/);
    for (const id of ids) expect(wrapper.find(`#${id}`).exists()).toBe(true);
  });

  it('forwards required and disabled to the control', () => {
    const wrapper = mountField({ required: true, disabled: true });

    const input = wrapper.get('input');
    expect(input.element.hasAttribute('required')).toBe(true);
    expect(input.element.hasAttribute('disabled')).toBe(true);
    expect(wrapper.get('.form-field__required').text()).toBe('*');
  });

  it('honours an explicit id', () => {
    const wrapper = mountField({ id: 'release-name' });

    expect(wrapper.get('label').attributes('for')).toBe('release-name');
    expect(wrapper.get('input').attributes('id')).toBe('release-name');
  });
});

/*
 * The adoption proof. LocalUsersPage is the page replaced by this primitive, so its
 * rendered DOM must show the same three relations — this is what fails if the page
 * ignores the slot props, or if FormField stops providing them.
 */
describe('FormField wiring on the adopting page', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
  });

  function signIn(roles: string[]): void {
    useAuthStore().$patch({
      status: 'authenticated',
      initialized: true,
      user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles, activeOrgId: 'org-1' },
    });
  }

  async function mountPage() {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/settings/users', name: 'LocalUsers', component: LocalUsersPage },
        { path: '/', name: 'Home', component: { template: '<div />' } },
      ],
    });
    await router.push('/settings/users');
    await router.isReady();
    const wrapper = mount(LocalUsersPage, { global: { plugins: [router] } });
    await flushPromises();
    return wrapper;
  }

  it('associates the password control with its label, hint and error', async () => {
    signIn(['platform_admin']);
    mockedList.mockResolvedValue({ users: [], nextCursor: '' });

    const wrapper = await mountPage();
    const password = wrapper.get('input[name="password"]');
    const passwordId = password.attributes('id');

    expect(passwordId).toBeTruthy();
    expect(wrapper.get(`label[for="${passwordId}"]`).text()).toContain('初始密码');

    // With no error the hint is the description and the control is not invalid.
    const hintId = password.attributes('aria-describedby');
    expect(hintId).toMatch(/-help$/);
    expect(wrapper.get(`#${hintId}`).text()).toContain('bcrypt');
    expect(password.attributes('aria-invalid')).toBeUndefined();

    // An oversized value swaps the description to the alert and flips aria-invalid.
    await password.setValue('x'.repeat(80));

    const errorId = password.attributes('aria-describedby');
    expect(errorId).toMatch(/-error$/);
    expect(wrapper.get(`#${errorId}`).text()).toContain('密码过长');
    expect(password.attributes('aria-invalid')).toBe('true');
    expect(wrapper.get('[role="alert"]').attributes('id')).toBe(errorId);

    // The slot props the page must forward: required reaches both controls.
    expect(wrapper.get('input[name="username"]').attributes('required')).toBeDefined();
    expect(password.attributes('required')).toBeDefined();
  });
});
