// Navigation-gap regression spec (plan §5.1 N1/N2/N4/N7).
//
// Same environment contract as e2e/emergency-smoke.spec.ts: a real backend is an
// explicit requirement, so the suite is skipped with a reason unless
// E2E_BACKEND=true — never silently shrunk.
//
// Required environment:
//   E2E_BACKEND=true
//   E2E_BASE_URL           console base (default http://127.0.0.1:5173)
//   E2E_ADMIN_A_USER/PASS  an account that may write (defaults dev-admin)
//   E2E_VIEWER_USER/PASS   optional read-only account; the N4 case skips without it
//   E2E_CUSTOMER_ID        optional customer id for the direct-URL breadcrumb case
import { expect, test } from '@playwright/test';

test.beforeEach(() => {
  test.skip(
    process.env.E2E_BACKEND !== 'true',
    'navigation spec requires the dev/backing stack (E2E_BACKEND=true) — same contract as the emergency smoke',
  );
});

const ADMIN = {
  username: process.env.E2E_ADMIN_A_USER ?? 'dev-admin',
  password: process.env.E2E_ADMIN_A_PASS ?? '',
};
const VIEWER = {
  username: process.env.E2E_VIEWER_USER ?? 'dev-reader',
  password: process.env.E2E_VIEWER_PASS ?? '',
};
const CUSTOMER_ID = process.env.E2E_CUSTOMER_ID ?? '';
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

async function login(page: import('@playwright/test').Page, username: string, password: string): Promise<void> {
  await page.goto('/login');
  await page.getByLabel(/用户名|Username/).fill(username);
  await page.getByLabel(/密码|Password/).fill(password);
  await page.getByRole('button', { name: /登录|Login|Sign in/i }).click();
  await page.waitForURL((url) => !url.pathname.startsWith('/login'));
}

// N1: the home page used to be a dead end (zero links, a single Dismiss action).
test('home page is a workbench whose tiles all resolve', async ({ page }) => {
  test.skip(ADMIN.password === '', 'E2E_ADMIN_A_PASS is required');
  await login(page, ADMIN.username, ADMIN.password);

  await page.goto('/');
  const tiles = page.locator('[data-testid^="home-tile-"]');
  await expect(tiles.first()).toBeVisible();
  expect(await tiles.count()).toBeGreaterThan(4);

  await page.getByTestId('home-tile-CustomerList').click();
  await expect(page).toHaveURL(/\/customers$/);
});

// N2: the customer subtree had no path to clusters at all.
test('every customer row links to its clusters, and the detail page too', async ({ page }) => {
  test.skip(ADMIN.password === '', 'E2E_ADMIN_A_PASS is required');
  await login(page, ADMIN.username, ADMIN.password);

  await page.goto('/customers');
  const rowLink = page.locator('a[href$="/clusters"]').first();
  await expect(rowLink).toBeVisible();
  const href = await rowLink.getAttribute('href');
  expect(href).toMatch(/\/customers\/.+\/clusters$/);

  await rowLink.click();
  await expect(page).toHaveURL(/\/clusters$/);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();

  if (CUSTOMER_ID) {
    await page.goto(`/customers/${CUSTOMER_ID}`);
    await expect(page.getByRole('link', { name: '集群' })).toBeVisible();
  }
});

// N4: a read-only role following a write route was redirected silently to the list.
test('a read-only role gets an explained refusal instead of a silent redirect', async ({ page }) => {
  test.skip(VIEWER.password === '', 'E2E_VIEWER_PASS is required for the refusal case');
  test.skip(CUSTOMER_ID === '', 'E2E_CUSTOMER_ID is required to build the write route');
  await login(page, VIEWER.username, VIEWER.password);

  await page.goto(`/customers/${CUSTOMER_ID}/clusters/new`);

  await expect(page).toHaveURL(/\/forbidden$/);
  await expect(page.locator('body')).toContainText('写权限');
});

// N7: a direct URL has no query names, and the breadcrumb used to print the raw UUID.
test('breadcrumbs resolve names instead of printing identifiers on direct entry', async ({ page }) => {
  test.skip(ADMIN.password === '', 'E2E_ADMIN_A_PASS is required');
  test.skip(CUSTOMER_ID === '', 'E2E_CUSTOMER_ID is required to build the inventory route');
  await login(page, ADMIN.username, ADMIN.password);

  // Find a cluster of that customer through the UI (no fixture coupling).
  await page.goto(`/customers/${CUSTOMER_ID}/clusters`);
  const clusterLink = page.locator('a[href*="/clusters/"]').first();
  await expect(clusterLink).toBeVisible();
  const clusterHref = (await clusterLink.getAttribute('href')) ?? '';
  const clusterId = clusterHref.split('/clusters/')[1]?.split(/[?#]/)[0] ?? '';
  expect(clusterId).not.toBe('');

  // Deliberately WITHOUT the customerName/clusterName query parameters.
  await page.goto(`/customers/${CUSTOMER_ID}/clusters/${clusterId}/releases`);
  const breadcrumb = page.locator('nav.breadcrumbs');
  await expect(breadcrumb).toBeVisible();
  const text = (await breadcrumb.innerText()).replace(/\s+/g, ' ').trim();

  expect(text).toContain('发布清单');
  expect(UUID.test(text), `breadcrumb still shows an identifier: ${text}`).toBe(false);
});
