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
  reporter: [
    ['list'],
    // TASK-307: a browsable HTML report next to the console lane's JSON evidence.
    // `open: 'never'` keeps the reporter from starting a static server (CI sets
    // CI=true, which already suppresses auto-open; this states it unconditionally).
    // The output folder is the Playwright default, `web/playwright-report/`, which
    // `.gitignore` covers and CI uploads as an artifact.
    ['html', { open: 'never', outputFolder: 'playwright-report' }],
  ],
  use: {
    // Formal API base: the web dev server proxies Connect to the service
    // ports; override with E2E_BASE_URL for staging environments.
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:5173',
    trace: 'retain-on-failure',
    // TASK-307: a screenshot of the failing step. 'only-on-failure' rather than
    // 'on' because the retained trace already carries a per-step screenshot/DOM
    // timeline; capturing every step of every case would only inflate the uploaded
    // artifact, while this is what makes the report readable at a glance.
    screenshot: 'only-on-failure',
    // CI installs the bundled Chromium; a developer machine already has Google
    // Chrome and this repository's convention is to reuse it rather than download
    // another browser. Set E2E_CHANNEL=chrome to run against the system browser.
    channel: process.env.E2E_CHANNEL === 'chrome' ? 'chrome' : undefined,
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
