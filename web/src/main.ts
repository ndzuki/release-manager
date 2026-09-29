import { createApp } from 'vue';
import { createPinia } from 'pinia';
import App from './App.vue';
import router from './router';
import { renderBootFailure, withTimeout } from './bootstrap';
import { useAuthStore } from './stores/auth';
// Design baseline (ADR-029): tokens first, then the global reset/typography.
import './styles/tokens.css';
import './styles/base.css';

// B2: the failure surface must work when the store bootstrap is what failed, so
// the mount target is resolved defensively.
function mountTarget(): Element {
  return document.getElementById('app') ?? document.body;
}

async function bootstrap(): Promise<void> {
  const app = createApp(App);
  const pinia = createPinia();

  app.use(pinia);
  // A rejected initialize() used to leave the page blank: it awaited BEFORE
  // app.mount, so the console rendered nothing at all and offered no retry.
  // The deadline covers the other half: a request that never settles at all
  // (blackholed network) would otherwise hang here forever.
  await withTimeout(useAuthStore(pinia).initialize());
  app.use(router);
  await router.isReady();
  app.mount('#app');
  // The inline guard in index.html reports module-level failures; once the app is
  // mounted it must stop replacing the console on late, unrelated errors.
  (window as unknown as { __rmBootMounted?: boolean }).__rmBootMounted = true;
}

void bootstrap().catch((error: unknown) => {
  renderBootFailure(mountTarget(), error);
});
