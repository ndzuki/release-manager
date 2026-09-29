import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it } from 'vitest';
import { createMemoryHistory } from 'vue-router';
import HomePage from './HomePage.vue';
import { createAppRouter } from '@/router';
import { useAuthStore } from '@/stores/auth';

/*
 * Plan N1: the home page was a dead end (one welcome empty state, a Dismiss button and
 * zero RouterLinks). These cases pin the workbench and — more usefully — that every
 * tile names a route the app actually defines.
 */
async function mountHome(roles: string[]) {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/');
  await router.isReady();
  const wrapper = mount(HomePage, { global: { plugins: [router] } });
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles, activeOrgId: 'org-1' },
  });
  await flushPromises();
  return { wrapper, router };
}

beforeEach(() => {
  setActivePinia(createPinia());
});

describe('HomePage workbench', () => {
  it('offers real destinations instead of a dead end', async () => {
    const { wrapper } = await mountHome(['platform_admin']);

    const tiles = wrapper.findAll('[data-testid^="home-tile-"]');
    expect(tiles.length).toBeGreaterThan(5);
    expect(wrapper.find('[data-testid="home-tile-CustomerList"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="home-tile-Audit"]').exists()).toBe(true);
  });

  // A tile whose route name is misspelled would render and then explode on click; this
  // resolves every one of them.
  it('only links to routes that exist', async () => {
    const { wrapper, router } = await mountHome(['platform_admin']);

    for (const tile of wrapper.findAll('[data-testid^="home-tile-"]')) {
      const name = tile.attributes('data-testid')!.replace('home-tile-', '');
      expect(() => router.resolve({ name }), `${name} is not a route`).not.toThrow();
    }
  });

  it('hides capability-gated tiles from a read-only role', async () => {
    const { wrapper } = await mountHome(['viewer']);

    expect(wrapper.find('[data-testid="home-tile-ArtifactLifecycle"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="home-tile-Bundles"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="home-tile-LocalUsers"]').exists()).toBe(false);
    // …but the always-open entries stay.
    expect(wrapper.find('[data-testid="home-tile-CustomerList"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="home-tile-TrustPolicy"]').exists()).toBe(true);
  });
});
