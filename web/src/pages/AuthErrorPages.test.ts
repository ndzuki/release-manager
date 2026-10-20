import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import { defineComponent, type Component } from 'vue';
import ForbiddenPage from './ForbiddenPage.vue';
import InitPage from './InitPage.vue';
import LoginPage from './LoginPage.vue';
import NotFoundPage from './NotFoundPage.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import { t } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';

/*
 * Authentication and error surfaces (TASK-394, W6).
 *
 * These four pages are the console's edges: login/setup run before the shell exists, and
 * the 404/403 pages stand in for every unknown URL and refused action. Two properties are
 * easy to break while restyling them and invisible in a screenshot-only review:
 *
 *   - the page heading. NotFoundPage used to render h2 + h3 (no h1 at all) and the
 *     forbidden surface lives in a shared component whose default heading is h2, because
 *     14 other pages already own an h1. ForbiddenPage therefore asks for h1 explicitly;
 *     these cases pin "exactly one h1" on all four pages, and the shared default for the
 *     embedded call sites.
 *   - the failure copy. The pages show the server's message verbatim, so the wave may not
 *     swap it for generic text; the assertions keep both the fixed title and the detail.
 */

const Stub = defineComponent({ template: '<div />' });

function createTestRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/init', name: 'Init', component: Stub },
      { path: '/login', name: 'Login', component: Stub },
      { path: '/forbidden', name: 'Forbidden', component: Stub },
      { path: '/:pathMatch(.*)*', name: 'NotFound', component: Stub },
      { path: '/', name: 'Home', component: Stub },
    ],
  });
}

async function mountAt(component: Component, path: string) {
  const router = createTestRouter();
  await router.push(path);
  await router.isReady();
  const wrapper = mount(component, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

function h1Texts(wrapper: ReturnType<typeof mount>): string[] {
  return wrapper.findAll('h1').map((heading) => heading.text());
}

beforeEach(() => {
  setActivePinia(createPinia());
});

describe('login page', () => {
  it('renders one h1 and keeps the server failure message on screen', async () => {
    const auth = useAuthStore();
    vi.spyOn(auth, 'login').mockRejectedValue(new Error('凭据无效'));

    const { wrapper } = await mountAt(LoginPage, '/login');

    expect(h1Texts(wrapper)).toEqual([t('login.title')]);

    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.text()).toContain(t('login.failedTitle'));
    expect(wrapper.text()).toContain('凭据无效');
  });

  it('still announces an expired session', async () => {
    const { wrapper } = await mountAt(LoginPage, '/login?reason=expired');

    expect(wrapper.get('[role="status"]').text()).toBe(t('login.sessionExpired'));
  });
});

describe('init page', () => {
  it('keeps the password constraint the server will enforce', async () => {
    const { wrapper } = await mountAt(InitPage, '/init');

    const password = wrapper.get('input[type="password"]');
    expect(password.attributes('minlength')).toBe('12');
    expect(password.attributes('required')).toBeDefined();
  });

  it('renders one h1 and keeps the setup failure message on screen', async () => {
    const auth = useAuthStore();
    vi.spyOn(auth, 'initializeSystem').mockRejectedValue(new Error('密码至少 12 位'));

    const { wrapper } = await mountAt(InitPage, '/init');

    expect(h1Texts(wrapper)).toEqual([t('init.title')]);

    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.text()).toContain(t('init.failed'));
    expect(wrapper.text()).toContain('密码至少 12 位');
  });
});

describe('not found page', () => {
  it('uses the page title as its only h1 and keeps the code decorative', async () => {
    const { wrapper } = await mountAt(NotFoundPage, '/missing');

    expect(h1Texts(wrapper)).toEqual([t('notFound.titleText')]);

    const code = wrapper.get('.not-found-page__code');
    expect(code.text()).toBe('404');
    expect(code.attributes('aria-hidden')).toBe('true');
    expect(wrapper.text()).toContain(t('notFound.body'));
  });

  it('keeps the way back home', async () => {
    const { wrapper, router } = await mountAt(NotFoundPage, '/missing');

    expect(wrapper.get('button').text()).toBe(t('notFound.home'));
    await wrapper.get('button').trigger('click');
    await flushPromises();

    expect(router.currentRoute.value.name).toBe('Home');
  });
});

describe('forbidden page', () => {
  it('promotes the shared state heading to the page h1 exactly once', async () => {
    useAuthStore().setForbiddenMessage('需要写权限才能创建集群');

    const { wrapper } = await mountAt(ForbiddenPage, '/forbidden');

    expect(h1Texts(wrapper)).toEqual([t('state.accessDenied')]);
    expect(wrapper.findAll('h2')).toHaveLength(0);
    expect(wrapper.text()).toContain('需要写权限才能创建集群');
  });

  it('keeps the way back home', async () => {
    const { wrapper, router } = await mountAt(ForbiddenPage, '/forbidden');

    await wrapper.get('button').trigger('click');
    await flushPromises();

    expect(router.currentRoute.value.name).toBe('Home');
  });

  it('leaves the embedded default at h2 so other pages keep a single h1', () => {
    // 14 pages render this state inside their own h1-owning layout; the default must stay
    // h2 or every one of them would suddenly have two top-level headings.
    const wrapper = mount(ForbiddenState);

    expect(wrapper.findAll('h2')).toHaveLength(1);
    expect(wrapper.findAll('h1')).toHaveLength(0);
  });
});
