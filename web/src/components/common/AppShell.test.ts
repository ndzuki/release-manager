import { RouterLinkStub, flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory, createRouter } from 'vue-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import AppShell from './AppShell.vue';
import { useAuthStore } from '@/stores/auth';

/*
 * AppShell contract (TASK-394).
 *
 * The direction-C rewrite turned a hand-written header into a routed sidebar, so the
 * properties worth pinning are the ones that rewrite could silently break:
 *
 *  1. NAVIGATION COMES FROM THE ROUTER. The test router registers only a subset of the
 *     real route names, and the entries whose names are missing must disappear — that is
 *     the `router.hasRoute` probe, and it is what keeps a switch-disabled surface out of
 *     the rail without a second list to maintain. (The release-operations entries are
 *     additionally gated by their build switch, because those routes stay registered and
 *     answer NotFound; that half is exercised through capabilities below.)
 *  2. CAPABILITIES STILL GATE the governance entries (viewer sees no bindings/users,
 *     platform_admin does).
 *  3. The rail carries the brand, the labelled nav landmark and the theme control; the
 *     shell renders no page title, so the page's own heading cannot be duplicated.
 */
function makeRouter() {
  const page = { template: '<div />' };
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'Home', component: page },
      // The logout handler navigates here; a missing record would reject after the store
      // call and turn an assertion about sign-out into an unhandled navigation error.
      { path: '/login', name: 'Login', component: page },
      { path: '/customers', name: 'CustomerList', component: page },
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: page },
      { path: '/audit', name: 'Audit', component: page },
      { path: '/settings/bindings', name: 'GovernanceBindings', component: page },
      { path: '/settings/users', name: 'LocalUsers', component: page },
    ],
  });
}

async function mountShell(options: { path?: string; roles?: string[] } = {}) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const auth = useAuthStore();
  // The shell reads only `username`, `roles` and `activeOrgId` from the session user, so a
  // structural stand-in keeps this test independent of protobuf codegen. `Object.assign`
  // bypasses the message's nominal type without weakening the assertions below.
  Object.assign(auth, {
    user: { username: 'alice', roles: options.roles ?? ['viewer'], activeOrgId: 'org-1' },
    organizations: [{ id: 'org-1', name: 'Acme' }],
  });

  const router = makeRouter();
  await router.push(options.path ?? '/customers');
  await router.isReady();

  const wrapper = mount(AppShell, { global: { plugins: [pinia, router], stubs: { RouterLink: RouterLinkStub } } });
  return { wrapper, auth };
}

function navLabels(wrapper: Awaited<ReturnType<typeof mountShell>>['wrapper']): string[] {
  return wrapper.findAll('.app-shell__link').map((link) => link.text());
}

afterEach(() => {
  delete document.documentElement.dataset.theme;
});

describe('AppShell', () => {
  it('renders the brand, the labelled nav and the main slot', async () => {
    const { wrapper } = await mountShell();

    expect(wrapper.get('.app-shell__brand').text()).toContain('Release Manager');
    expect(wrapper.get('nav').attributes('aria-label')).toBe('主导航');
    expect(wrapper.find('main').exists()).toBe(true);
  });

  it('does not repeat the page title in the shell', async () => {
    const { wrapper } = await mountShell();

    expect(wrapper.find('h1').exists()).toBe(false);
  });

  it('only links routes the router actually knows', async () => {
    // `Definitions`, `Bundles`, `OperationCenter` … are real routes in web/src/router but
    // not in this test router, so a hand-written list would still render them.
    const { wrapper } = await mountShell();

    expect(navLabels(wrapper)).toEqual(['客户', '审计']);
  });

  it('adds the cluster entry only when a customer is in the route', async () => {
    const { wrapper } = await mountShell({ path: '/customers/c-1/clusters' });

    expect(navLabels(wrapper)).toEqual(['客户', '集群', '审计']);
    const clusterLink = wrapper.findAllComponents(RouterLinkStub).find((link) => link.text() === '集群');
    expect(clusterLink?.props('to')).toMatchObject({ name: 'ClusterList', params: { customerId: 'c-1' } });
  });

  it('keeps the capability gates on the governance entries', async () => {
    const viewer = await mountShell();
    expect(navLabels(viewer.wrapper)).not.toContain('授权绑定');
    expect(navLabels(viewer.wrapper)).not.toContain('本地用户');

    // platform_admin holds `binding read` and the admin-only local-account surface.
    const admin = await mountShell({ roles: ['platform_admin'] });
    expect(navLabels(admin.wrapper)).toContain('授权绑定');
    expect(navLabels(admin.wrapper)).toContain('本地用户');
  });

  it('exposes the theme control with an accessible name that states the current theme', async () => {
    const { wrapper } = await mountShell();
    const button = wrapper.get('.app-shell__theme');

    expect(button.attributes('aria-label')).toMatch(/^切换主题（当前：/);
    expect(button.attributes('title')).toBe(button.attributes('aria-label'));
    // The glyph is decoration; the label carries the meaning.
    expect(button.find('[aria-hidden="true"]').exists()).toBe(true);
  });

  it('cycles the theme from the rail and writes documentElement.dataset.theme', async () => {
    const { wrapper } = await mountShell();
    const button = wrapper.get('.app-shell__theme');
    const before = button.attributes('aria-label');

    await button.trigger('click');

    expect(button.attributes('aria-label')).not.toBe(before);
    expect(['light', 'dark']).toContain(document.documentElement.dataset.theme);
  });

  /*
   * The rail has to survive 375px. Above 1024px it is a fixed column; below, the same
   * markup becomes a bar plus a drawer, and the ONLY state that can strand a reader is the
   * drawer staying open over the page it just navigated to — hence the two closing paths
   * below. `aria-expanded` is the state (not a CSS class), and the control is a native
   * button, so keyboard activation needs no handler of its own.
   */
  it('exposes the drawer control with aria-expanded and aria-controls', async () => {
    const { wrapper } = await mountShell();
    const toggle = wrapper.get('.app-shell__menu');

    expect(toggle.element.tagName).toBe('BUTTON');
    expect(toggle.attributes('type')).toBe('button');
    expect(toggle.attributes('aria-expanded')).toBe('false');
    expect(toggle.attributes('aria-controls')).toBe('app-shell-panel');
    // The glyph is decoration (aria-hidden), the label carries the name.
    expect(toggle.text()).toContain('导航菜单');
    expect(toggle.find('[aria-hidden="true"]').exists()).toBe(true);
    expect(wrapper.get('#app-shell-panel').classes()).not.toContain('app-shell__panel--open');
  });

  it('opens and closes the drawer, including on Escape', async () => {
    const { wrapper } = await mountShell();
    const toggle = wrapper.get('.app-shell__menu');

    await toggle.trigger('click');
    expect(toggle.attributes('aria-expanded')).toBe('true');
    expect(wrapper.get('#app-shell-panel').classes()).toContain('app-shell__panel--open');

    await toggle.trigger('click');
    expect(toggle.attributes('aria-expanded')).toBe('false');

    await toggle.trigger('click');
    await wrapper.get('.app-shell__aside').trigger('keydown', { key: 'Escape' });
    expect(toggle.attributes('aria-expanded')).toBe('false');
  });

  it('closes the drawer after a navigation', async () => {
    const { wrapper } = await mountShell();
    await wrapper.get('.app-shell__menu').trigger('click');
    expect(wrapper.get('.app-shell__menu').attributes('aria-expanded')).toBe('true');

    await wrapper.vm.$router.push('/audit');
    await flushPromises();

    expect(wrapper.get('.app-shell__menu').attributes('aria-expanded')).toBe('false');
  });

  it('signs out through the auth store', async () => {
    const { wrapper, auth } = await mountShell();
    const logout = vi.spyOn(auth, 'logout').mockResolvedValue(undefined);

    await wrapper.get('.app-shell__logout').trigger('click');

    expect(logout).toHaveBeenCalledTimes(1);
  });
});
