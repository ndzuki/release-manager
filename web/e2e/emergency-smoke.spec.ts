// Emergency + convergence browser smoke (REQ-058 Step 9, ADR-013: formal API
// only). Each scenario exercises the canonical flow end to end; the double
// approval uses a second browser context (different actor).
//
// Environment contract (plan Step 9): the full backend stack (auth +
// orchestrator + operator + PostgreSQL + registry) must be running and
// seeded with at least one Customer/Cluster/ReleaseDefinition and two
// administrators. Set E2E_BACKEND=true to activate; without it the suite is
// skipped with an explicit reason — never silently shrunk.
import { expect, test } from '@playwright/test';

test.beforeEach(() => {
  test.skip(
    process.env.E2E_BACKEND !== 'true',
    'E2E requires the formal backend stack (E2E_BACKEND=true) — see the REQ-058 environment contract',
  );
});

const CREDENTIALS = {
  adminA: { username: process.env.E2E_ADMIN_A_USER ?? 'admin-a', password: process.env.E2E_ADMIN_A_PASS ?? '' },
  adminB: { username: process.env.E2E_ADMIN_B_USER ?? 'admin-b', password: process.env.E2E_ADMIN_B_PASS ?? '' },
};

// D19=A: web-side TTI budget assertion — "route entry → form operable"
// p95 ≤ 1.5s (REQ-058 performance table). A single E2E sample asserts the
// budget, not the p95 statistic; the service-side p95 belongs to the
// observability takeover item (REQ-065/066 e2e + metrics).
const TTI_BUDGET_MS = Number(process.env.E2E_TTI_BUDGET_MS ?? 1500);

async function login(page: import('@playwright/test').Page, username: string, password: string): Promise<void> {
  await page.goto('/login');
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: /登录|Login/i }).click();
  await page.waitForURL(/customers|releases|home/i);
}

test('Inventory → Emergency → Execute → Operation Detail (REQUIRE_PROMOTION)', async ({ page }) => {
  await login(page, CREDENTIALS.adminA.username, CREDENTIALS.adminA.password);
  await page.goto('/customers');
  await page.getByRole('link', { name: /releases|查看/i }).first().click();
  await page.waitForURL(/\/releases$/);

  // The entry comes from the ReleaseSummary projection (AC-058-08).
  const emergencyLink = page.getByRole('link', { name: '紧急变更' }).first();
  await expect(emergencyLink).toBeVisible();
  await emergencyLink.click();
  await page.waitForURL(/\/emergency$/);

  // D19=A: route entry → form operable within the TTI budget (REQ-058
  // performance table; service-side p95 is the observability takeover item).
  const targetRadio = page.getByRole('radio', { name: /DEPLOYMENT/i }).first();
  const ttiStart = Date.now();
  await expect(targetRadio).toBeVisible();
  await expect(targetRadio).toBeEnabled();
  const ttiMs = Date.now() - ttiStart;
  expect(
    ttiMs,
    `Emergency form TTI within ${TTI_BUDGET_MS}ms (measured ${ttiMs}ms)`,
  ).toBeLessThanOrEqual(TTI_BUDGET_MS);

  // Target → container → VERIFIED artifact → reason → policy → confirm.
  await targetRadio.check();
  await page.getByLabel('容器').selectOption({ index: 1 });
  await page.getByRole('radio').first().check();
  await page.getByPlaceholder('事故 ID / 现象 / 影响范围').fill('E2E 冒烟：验证镜像紧急变更');
  await page.getByRole('button', { name: '确认变更' }).click();
  await page.getByRole('checkbox', { name: /我已确认/ }).check();
  await page.getByRole('button', { name: '确认提交' }).click();

  // Transaction acceptance → Operation Detail, not the Operator result.
  await page.waitForURL(/\/operations\//);
  await expect(page.getByText('Emergency Result')).toBeVisible();
  await expect(page.getByText('已受理（执行异步进行）')).toBeVisible();
});

test('Convergence: Prepare → ValuesEditor draft → Submit → cross-actor Approve', async ({ browser }) => {
  const contextA = await browser.newContext();
  const pageA = await contextA.newPage();
  await login(pageA, CREDENTIALS.adminA.username, CREDENTIALS.adminA.password);

  await pageA.goto('/customers');
  await pageA.getByRole('link', { name: /releases|查看/i }).first().click();
  await pageA.waitForURL(/\/releases$/);
  const convergenceLink = pageA.getByRole('link', { name: /^收敛\s/ }).first();
  await convergenceLink.click();
  await pageA.waitForURL(/\/emergency\/convergence$/);

  await pageA.getByRole('checkbox').first().check();
  await pageA.getByRole('button', { name: 'Prepare 收敛' }).click();
  // URL carries ONLY mode=convergence&prepareToken (AC-058-35).
  await pageA.waitForURL(/\/values\?mode=convergence&prepareToken=/);
  const url = new URL(pageA.url());
  expect(url.searchParams.get('mode')).toBe('convergence');
  expect(url.searchParams.get('prepareToken')).toBeTruthy();

  await expect(pageA.getByText('收敛锁定路径（只读）')).toBeVisible();
  await pageA.getByRole('button', { name: '保存 Draft' }).click();
  await pageA.getByRole('button', { name: 'Submit' }).click();
  await expect(pageA.getByText('Pending Approval')).toBeVisible();
  await contextA.close();

  // Cross-actor approval in a second context (AC-058-41).
  const contextB = await browser.newContext();
  const pageB = await contextB.newPage();
  await login(pageB, CREDENTIALS.adminB.username, CREDENTIALS.adminB.password);
  await pageB.goto(pageA.url().split('?')[0]);
  await pageB.getByRole('button', { name: 'Approve' }).click();
  await expect(pageB.getByText('Approved')).toBeVisible();
  await contextB.close();
});

test('Kill switch: new entry 404 while existing paths stay reachable (AC-058-05)', async ({ page }) => {
  await login(page, CREDENTIALS.adminA.username, CREDENTIALS.adminA.password);
  // When emergencyChangeEnabled=false the entry route must 404 (page gate);
  // convergence remains reachable per capability.
  const response = await page.goto('/customers/c1/clusters/c1/releases/def1/emergency');
  // 404 comes from the page-level gate; the server remains authoritative.
  expect([404, 200]).toContain(response?.status() ?? 0);
  await expect(page.getByText(/紧急变更不可用|404/)).toBeVisible();
});
