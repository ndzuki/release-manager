// Playwright E2E configuration (REQ-058 Step 9, decisions D5/D11).
// E2E runs against the REAL backend API (ADR-013) — never mocked services.
// The backend stack is an explicit environment requirement: scenarios are
// gated by E2E_BACKEND=true so a missing stack fails loudly instead of
// silently shrinking the AC coverage (plan Step 9 risk note).
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  fullyParallel: false,
  retries: 0,
  reporter: [['list']],
  use: {
    // Formal API base: the web dev server proxies Connect to the service
    // ports; override with E2E_BASE_URL for staging environments.
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:5173',
    trace: 'retain-on-failure',
  },
  // The dev server is started by the operator/CI environment (the web proxy
  // targets the service ports); `npm run dev` can be used locally.
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
});
