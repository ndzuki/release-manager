
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
    // Playwright E2E specs live in e2e/ and are executed by `npm run test:e2e`
    // (ADR-013: browser flows go through the formal API), never by vitest —
    // vitest's default include `**/*.spec.ts` would otherwise pick them up and
    // fail the full `npm test` gate.
    exclude: ['e2e/**', 'playwright/**', 'node_modules/**'],
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
      // AuditService is served by release-api. 8087 is release-web itself in
      // the dev cluster (release-api is 8088), so pointing this at 8087 sent
      // audit calls back into the SPA and answered 405 (B5).
      '/audit.v1.AuditService': {
        target: 'http://127.0.0.1:8088',
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
