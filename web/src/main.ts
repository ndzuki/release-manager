import { createApp } from 'vue';
import { createPinia } from 'pinia';
import App from './App.vue';
import router from './router';
import { useAuthStore } from './stores/auth';

async function bootstrap(): Promise<void> {
  const app = createApp(App);
  const pinia = createPinia();

  app.use(pinia);
  await useAuthStore(pinia).initialize();
  app.use(router);
  await router.isReady();
  app.mount('#app');
}

void bootstrap();
