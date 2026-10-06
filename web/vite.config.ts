
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
    // Proxy Connect API calls to the development service ports.
    //
    // Keys are PACKAGE prefixes, mirroring web/nginx.conf's `location ^~ /auth.v1.`
    // style, not individual services. Enumerating services by hand is what broke
    // the dev console: `/auth.v1.AuthorizationService` was never listed, so it fell
    // through to the SPA, the authorization snapshot never became fresh,
    // `writeBlocked` turned true and every write entry point (rollback, new
    // operation, emergency change) silently stopped rendering — production was
    // fine because nginx proxies the whole package. Adding a service to an
    // existing proto package must not require touching this file.
    //
    // Keep this list in step with web/nginx.conf: one entry per proto package plus
    // the probe endpoints. `/health`, `/readyz` and `/environment` belong to
    // orchestrator, and the SPA router does not own those paths.
    proxy: {
      '/auth.v1.': {
        target: 'http://127.0.0.1:8085',
        changeOrigin: true,
      },
      '/orchestrator.v1.': {
        target: 'http://127.0.0.1:8083',
        changeOrigin: true,
      },
      '/operator.v1.': {
        target: 'http://127.0.0.1:8084',
        changeOrigin: true,
      },
      // AuditService is served by release-api. 8087 is release-web itself in
      // the dev cluster (release-api is 8088), so pointing this at 8087 sent
      // audit calls back into the SPA and answered 405 (B5).
      '/audit.v1.': {
        target: 'http://127.0.0.1:8088',
        changeOrigin: true,
      },
      '/notifier.v1.': {
        target: 'http://127.0.0.1:8086',
        changeOrigin: true,
      },
      '/webhook.v1.': {
        target: 'http://127.0.0.1:8082',
        changeOrigin: true,
      },
      '/trust.v1.': {
        target: 'http://127.0.0.1:8083',
        changeOrigin: true,
      },
      '/health': {
        target: 'http://127.0.0.1:8083',
        changeOrigin: true,
      },
      '/readyz': {
        target: 'http://127.0.0.1:8083',
        changeOrigin: true,
      },
      '/environment': {
        target: 'http://127.0.0.1:8083',
        changeOrigin: true,
      },
    },
  },
});
