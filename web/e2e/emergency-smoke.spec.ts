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
  await page.getByLabel(/用户名|Username/).fill(username);
  await page.getByLabel(/密码|Password/).fill(password);
  await page.getByRole('button', { name: /登录|Login|Sign in/i }).click();
  // The console's post-login destination is the home route, which is `/` (the router's
  // `name: 'Home'`), not `/home`: a regex looking for those words never matches and every
  // scenario in this file timed out at login (found by actually running the suite, which
  // the handoff had recorded as "never run"). Wait for the session instead of a path.
  await page.waitForURL((url) => !url.pathname.startsWith('/login'));
}

// The console's IA is customers to customer detail, then clusters, then cluster detail,
// then the release inventory: the emergency and convergence entries live on the release
// ROW, and only for a row bound to a ReleaseDefinition. The spec used to skip the cluster
// hops entirely; every hop below asserts the URL it reached. (Written as line comments on
// purpose: the locator-hygiene gate strips those, and a block comment here would look
// like a regex locator carrying English copy.)
async function openReleases(page: import('@playwright/test').Page): Promise<void> {
  // The binding the emergency entry needs only exists on a cluster whose release was
  // installed THROUGH the orchestrator, so the operator picks the cluster explicitly
  // (E2E_CUSTOMER_ID / E2E_CLUSTER_ID, same convention as navigation.spec.ts) instead of
  // relying on whatever the first row happens to be.
  const customerId = process.env.E2E_CUSTOMER_ID ?? '';
  const clusterId = process.env.E2E_CLUSTER_ID ?? '';
  if (customerId !== '' && clusterId !== '') {
    await page.goto(`/customers/${customerId}/clusters/${clusterId}/releases`);
    await page.waitForURL(/\/releases(\?|$)/);
    return;
  }
  await page.goto('/customers');
  await page.getByRole('link', { name: '集群' }).first().click();
  await page.waitForURL(/\/clusters$/);
  await page.getByRole('link', { name: '查看' }).first().click();
  await page.waitForURL(/\/clusters\/[^/]+$/);
  await page.getByRole('link', { name: '发布清单' }).first().click();
  await page.waitForURL(/\/releases(\?|$)/);
}

// Precondition, stated instead of hidden: the emergency entry renders only for a release
// row whose projection resolves a ReleaseDefinition (otherwise the row reads 未绑定
// Definition). The binding comes from the operator's targeted inventory update after a
// deployment driven by this orchestrator, so a cluster whose releases were installed
// outside it has NO entry; the case must skip with this reason rather than pass by
// accident.
const BOUND_RELEASE_PRECONDITION =
  'no release on this cluster is bound to a ReleaseDefinition, so the emergency entry does not render (TASK-217: the binding is written by the operator after a deployment driven through the orchestrator, so run `make e2e-stage STAGES=release` first and point E2E_CLUSTER_ID at that cluster)';

test('Inventory → Emergency → Execute → Operation Detail (REQUIRE_PROMOTION)', async ({ page }) => {
  await login(page, CREDENTIALS.adminA.username, CREDENTIALS.adminA.password);
  await openReleases(page);

  // The inventory is loaded asynchronously: a precondition read before the table arrives
  // would skip every time. Wait for the rows (or the empty state), then judge.
  const emitted = page.locator('tbody tr');
  await page
    .locator('tbody tr, [data-testid="release-inventory-empty"]')
    .first()
    .waitFor({ state: 'attached', timeout: 20_000 })
    .catch(() => undefined);
  await emitted.first().waitFor({ state: 'attached', timeout: 20_000 }).catch(() => undefined);

  // The entry comes from the ReleaseSummary projection (AC-058-08).
  const emergencyLink = page.getByRole('link', { name: '紧急变更' }).first();
  test.skip((await emergencyLink.count()) === 0, BOUND_RELEASE_PRECONDITION);
  await expect(emergencyLink).toBeVisible();

  // D19=A: route entry → form operable within the TTI budget (REQ-058
  // performance table). The clock starts at navigation initiation (link
  // click) and stops when the first form control is enabled; the
  // service-side p95 stays with the observability takeover item.
  const ttiStart = Date.now();
  await emergencyLink.click();
  await page.waitForURL(/\/emergency$/);
  const targetRadio = page.getByRole('radio', { name: /DEPLOYMENT/i }).first();
  await expect(targetRadio).toBeVisible();
  await expect(targetRadio).toBeEnabled();
  const ttiMs = Date.now() - ttiStart;
  expect(
    ttiMs,
    `Emergency form TTI within ${TTI_BUDGET_MS}ms (measured ${ttiMs}ms)`,
  ).toBeLessThanOrEqual(TTI_BUDGET_MS);

  // Target → container → VERIFIED artifact → reason → policy → confirm.
  await targetRadio.check();
  // Selecting the container is what triggers the candidate-artifact load; the selector
  // renders "没有可用的 VERIFIED 候选制品" until a container is chosen, so an artifact
  // assertion must come after this step.
  await page.getByLabel('容器').selectOption({ index: 1 });
  const artifactRadio = page.getByRole('radiogroup', { name: '选择候选制品' }).getByRole('radio').first();
  await artifactRadio.waitFor({ state: 'visible', timeout: 20_000 });
  // NOT getByRole('radio').first(): that is the change TARGET, which is already checked.
  await artifactRadio.check();
  await page.getByPlaceholder('事故 ID / 现象 / 影响范围').fill('E2E 冒烟：验证镜像紧急变更');
  await page.getByRole('button', { name: '确认变更' }).click();
  await page.getByRole('checkbox', { name: /我已确认/ }).check();
  await page.getByRole('button', { name: '确认提交' }).click();

  // Transaction acceptance → Operation Detail, not the Operator result.
  //
  // This used to be unreachable: the trust gate looked the artifact up by image digest with
  // a hard-coded policy version ("v1") while the only writer stored the bundle digest under
  // the live version ("1"), so a correctly signed release was refused with
  // artifact_not_trusted (TASK-220). The gate now resolves the delivering bundle and reads
  // the live policy version, and this assertion is the end-to-end check that a signed
  // release can actually be changed in an emergency. It cannot run without a bound release
  // and a fresh observation; when those preconditions are missing the case skips above.
  await page.waitForURL(/\/operations\//);
  await expect(page.getByText('紧急变更结果')).toBeVisible();
  await expect(page.getByText('已受理（执行异步进行）')).toBeVisible();
});

test('Convergence: Prepare → ValuesEditor draft → Submit → cross-actor Approve', async ({ browser }) => {
  const contextA = await browser.newContext();
  const pageA = await contextA.newPage();
  await login(pageA, CREDENTIALS.adminA.username, CREDENTIALS.adminA.password);

  await openReleases(pageA);
  await pageA
    .locator('tbody tr')
    .first()
    .waitFor({ state: 'attached', timeout: 20_000 })
    .catch(() => undefined);
  const convergenceLink = pageA.getByRole('link', { name: /^收敛\s/ }).first();
  test.skip(
    (await convergenceLink.count()) === 0,
    'no release row has pending convergence tasks, so the convergence entry does not render (TASK-217: the fixture needs a bound release with a pending_promotion task)',
  );
  await convergenceLink.click();
  await pageA.waitForURL(/\/emergency\/convergence$/);

  await pageA.getByRole('checkbox').first().check();
  await pageA.getByRole('button', { name: '准备收敛' }).click();
  // URL carries ONLY mode=convergence&prepareToken (AC-058-35).
  await pageA.waitForURL(/\/values\?mode=convergence&prepareToken=/);
  const url = new URL(pageA.url());
  expect(url.searchParams.get('mode')).toBe('convergence');
  expect(url.searchParams.get('prepareToken')).toBeTruthy();

  await expect(pageA.getByText('收敛锁定路径（只读）')).toBeVisible();
  await pageA.getByRole('button', { name: '保存 Draft' }).click();
  await pageA.getByRole('button', { name: '提交' }).click();
  await expect(pageA.getByText('待审批')).toBeVisible();
  await contextA.close();

  // Cross-actor approval in a second context (AC-058-41).
  const contextB = await browser.newContext();
  const pageB = await contextB.newPage();
  await login(pageB, CREDENTIALS.adminB.username, CREDENTIALS.adminB.password);
  await pageB.goto(pageA.url().split('?')[0]);
  await pageB.getByRole('button', { name: '审批通过' }).click();
  await expect(pageB.getByText('已审批')).toBeVisible();
  await contextB.close();
});

test('Kill switch: new entry 404 while existing paths stay reachable (AC-058-05)', async ({ page }) => {
  // The gate under test only exists when the feature is switched off. The default dev
  // stack runs with it ENABLED, so this case states the precondition instead of
  // re-interpreting whatever the page happens to render (TASK-217 AC-217-02).
  test.skip(
    process.env.E2E_EMERGENCY_DISABLED !== 'true',
    'set E2E_EMERGENCY_DISABLED=true against a stack started with emergencyChangeEnabled=false',
  );
  await login(page, CREDENTIALS.adminA.username, CREDENTIALS.adminA.password);
  // When emergencyChangeEnabled=false the page-level gate must render the
  // not-found state for the new-emergency entry. In this SPA the dev server
  // answers HTTP 200, so the authoritative AC-05 signal is the rendered
  // NotFound UI (EmergencyChangePage gate === 'not_found'), not the HTTP
  // status; convergence remains reachable per capability.
  await page.goto('/customers/c1/clusters/c1/releases/def1/emergency');
  await expect(page.getByText(/紧急变更不可用|404/)).toBeVisible();
});
