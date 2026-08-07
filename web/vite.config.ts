
import vue from '@vitejs/plugin-vue';
import { defineConfig } from 'vitest/config';
import { fileURLToPath } from 'node:url';

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  test: {
    environment: 'happy-dom',
    globals: true,
    restoreMocks: true,
  },
  server: {
    port: 5173,
    proxy: {
      // Proxy Connect API calls to the development service ports.
      '/auth.v1.AuthService': {
        target: 'http://127.0.0.1:8085',
        changeOrigin: true,
      },
      '/auth.v1.OrganizationService': {
        target: 'http://127.0.0.1:8085',
        changeOrigin: true,
      },
      '/auth.v1.BindingService': {
        target: 'http://127.0.0.1:8085',
        changeOrigin: true,
      },
      '/orchestrator.v1.OrchestratorService': {
        target: 'http://127.0.0.1:8083',
        changeOrigin: true,
      },
      '/operator.v1.OperatorService': {
        target: 'http://127.0.0.1:8084',
        changeOrigin: true,
      },
      '/audit.v1.AuditService': {
        target: 'http://127.0.0.1:8087',
        changeOrigin: true,
      },
      '/notifier.v1.NotifierService': {
        target: 'http://127.0.0.1:8086',
        changeOrigin: true,
      },
      '/webhook.v1.WebhookService': {
        target: 'http://127.0.0.1:8082',
        changeOrigin: true,
      },
    },
  },
});
