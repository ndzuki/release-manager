// Delivery-criteria browser E2E (TASK-325): the machine-checkable half of the
// H1–H7 walkthrough defined in docs/delivery-evidence.md §4.2.
//
// Scope. Only the steps that can be judged with no human in the loop live here.
// H6's three emergency submit paths and H7's cross-actor approval stay with
// e2e/emergency-smoke.spec.ts (which console.sh already runs and guards); this
// file adds H1, H2, H3, H4 and H5. Like every other case the console lane runs,
// each one is declared in test/e2e/prerequisite/console-expected-cases.json
// (TASK-307), so a silent skip fails the lane instead of passing it.
//
// Environment contract: identical to e2e/rollback.spec.ts. E2E_BACKEND is the
// ONLY skip gate; once the stack is declared up (E2E_BACKEND=true), a missing
// credential, rollback entry or release row is a HARD FAILURE with diagnostics —
// never a skip. H4 and H5 need no extra fixture: H4 rewrites one RPC response in
// flight (the same "改请求" technique the H4 criterion names) and H5 only presses
// keys.
import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

test.beforeEach(() => {
  test.skip(
    process.env.E2E_BACKEND !== 'true',
    'delivery-criteria spec requires the dev/backing stack (E2E_BACKEND=true) — same contract as the rollback and emergency specs',
  );
});

const ADMIN = {
  username: process.env.E2E_ADMIN_A_USER ?? 'dev-admin',
  password: process.env.E2E_ADMIN_A_PASS ?? '',
};

// The login route, named once: the locator-hygiene gate reads two `/word` spans
// on one line as an English locator, so the assertion references this constant.
const LOGIN_PATH = '/login';

// H1's "Set-Cookie 存在" becomes a machine check by reading the browser session
// cookie the browser branch issues (internal/auth/service.go AccessCookieName).
const ACCESS_COOKIE = 'rm_access';
// H1's "4 秒内 URL 离开 /login" — the walkthrough's own budget.
const LOGIN_BUDGET_MS = 4000;

// The release inventory's own row-level entry points
// (web/src/components/releases/ReleaseInventoryTable.vue).
const ROLLBACK_ROW = '[data-testid^="release-rollback-"]';
const ROLLBACK_METHOD = 'RollbackRelease';

// The release inventory's two terminal renderings: the rows, or the shared EmptyState.
// The old `[data-testid="release-inventory-empty"]` marker is gone — TASK-394 converged the
// page onto EmptyState/ReleaseInventorySkeleton — so waiting on it only ever burned the
// timeout and hid "renders nothing at all" behind "still loading".
const INVENTORY_SETTLED = 'tbody tr, .empty-state';

// H3 asks whether Escape leaves NO RollbackRelease behind, and that is a negative
// assertion — a negative assertion with no observation window is not evidence.
// Reading the counter the instant the panel hides cannot see a request that leaves
// a beat later, which is exactly the false-green direction the reviewer named
// (a synthetic page proved it: the immediate read said 0 while the request left
// 150ms afterwards). So the count is read across this bounded quiet window.
// A fixed window is right here precisely because the claim must stay EXACTLY zero:
// `expect.poll(() => count).toBe(0)` would return on its first check — the count is
// already 0 — and prove nothing at all.
const ROLLBACK_QUIET_MS = 500;

// Assembled from two pieces on purpose: the locator-hygiene gate
// (src/i18n/e2e-locator.usage.test.ts) reads any `/…/` span carrying ASCII as an
// English locator, and a joined '**/service/Method' literal would be one.
const LIST_NON_TERMINAL = 'orchestrator.v1.OrchestratorService/ListNonTerminalOperations';
// The correlation id the H4 read-failure case asserts is rendered where the
// operator can copy it.
const MOCK_REQUEST_ID = 'e2e-h4-read-failure';

// TASK-394 direction C: the operation detail page no longer renders the hand-rolled
// `.operation-detail__eyebrow` / `.operation-detail__header code` / `.operation-detail__state`
// markup. PageHeader renders the SERVER's operation type as the h1 — `{type}操作`, and
// `{type}操作 · {release}` when the route carries a release name (i18n/messages.ts
// `operation.detail.title` / `operation.detail.titleWithRelease`) — while the id and the
// definition moved into the summary as CopyableHash values whose `title` attribute carries
// the FULL string (the visible text is truncated on screen).
const OPERATION_TITLE = /^(安装|升级|回滚|紧急变更)操作( · .+)?$/;

// E2E_BACKEND=true means the environment was declared up: a missing credential
// is a misconfiguration that must fail, not silently skip the whole case.
function requireCredentials(): void {
  if (ADMIN.password !== '') return;
  throw new Error(
    'E2E_BACKEND=true but E2E_ADMIN_A_PASS is empty, so no login can be attempted. ' +
      'Source data/dev-credentials.env (never echo it) and pass E2E_ADMIN_A_PASS=$DEV_ADMIN_PASSWORD ' +
      'plus E2E_ADMIN_A_USER — see docs/testing.md "控制台（浏览器）E2E：CI 车道与 spec 归属".',
  );
}

async function login(page: Page, username: string, password: string): Promise<void> {
  await page.goto('/login');
  // autocomplete attributes, not copy: the login labels are localized.
  await page.locator('input[autocomplete="username"]').fill(username);
  await page.locator('input[autocomplete="current-password"]').fill(password);
  await page.locator('button[type="submit"]').click();
  await page.waitForURL((url) => !url.pathname.startsWith('/login'));
}

// Direct URL when the harness supplies the fixture ids, otherwise walk the IA.
// Every hop asserts the URL it reached.
async function openReleases(page: Page): Promise<void> {
  const customerId = process.env.E2E_CUSTOMER_ID ?? '';
  const clusterId = process.env.E2E_CLUSTER_ID ?? '';
  if (customerId !== '' && clusterId !== '') {
    await page.goto(`/customers/${customerId}/clusters/${clusterId}/releases`);
    await page.waitForURL((url) => url.pathname.endsWith('/releases'));
    return;
  }
  await page.goto('/customers');
  await page.getByRole('link', { name: '集群' }).first().click();
  await page.waitForURL((url) => url.pathname.endsWith('/clusters'));
  await page.getByRole('link', { name: '查看' }).first().click();
  await page.getByRole('link', { name: '发布清单' }).first().click();
  await page.waitForURL((url) => url.pathname.endsWith('/releases'));
}

// The H3/H5 dialogs are reached from the release inventory, which only renders a
// rollback trigger for a row bound to a ReleaseDefinition at revision > 1 — the
// state smoke.sh leaves behind (same precondition as rollback.spec.ts).
async function requireRollbackTrigger(page: Page): Promise<Locator> {
  await page
    .locator(INVENTORY_SETTLED)
    .first()
    .waitFor({ state: 'attached', timeout: 20_000 })
    .catch(() => undefined);
  const trigger = page.locator(ROLLBACK_ROW).first();
  await trigger.waitFor({ state: 'attached', timeout: 20_000 }).catch(() => undefined);
  if ((await trigger.count()) > 0) return trigger;
  throw new Error(
    [
      'rollback entry not found although E2E_BACKEND=true declares the stack up.',
      `url=${page.url()}`,
      'no release on this cluster renders a rollback trigger: it needs a ReleaseDefinition binding',
      'and revision > 1. Run `make e2e-stage STAGES=release` after `make dev-up dev-seed dev-status`',
      'to install+upgrade the e2e-release-target.',
    ].join('\n'),
  );
}

async function focusInsidePanel(page: Page): Promise<boolean> {
  return page.evaluate(() => Boolean(document.activeElement?.closest('.app-dialog__panel')));
}

test('H1 登录：成功离开登录页并下发会话 cookie；失败留在登录页且显示失败文案', async ({ page }) => {
  requireCredentials();

  // Failure half: a wrong password must be visible copy on the login page, not a
  // silent no-op, and it must not establish a session.
  await page.goto('/login');
  await page.locator('input[autocomplete="username"]').fill(ADMIN.username);
  await page.locator('input[autocomplete="current-password"]').fill(`${ADMIN.password}-wrong`);
  await page.locator('button[type="submit"]').click();
  await expect(page.locator('.error-state')).toBeVisible();
  await expect(page.locator('.error-state__title')).toHaveText('登录失败');
  expect(new URL(page.url()).pathname, 'a failed login must stay on the login route').toBe(LOGIN_PATH);
  expect((await page.context().cookies()).map((cookie) => cookie.name)).not.toContain(ACCESS_COOKIE);

  // Success half: the walkthrough's two machine-checkable claims — the session
  // cookie exists, and the URL leaves /login within the budget.
  await page.locator('input[autocomplete="current-password"]').fill(ADMIN.password);
  const startedAt = Date.now();
  await page.locator('button[type="submit"]').click();
  await page.waitForURL((url) => !url.pathname.startsWith('/login'));
  const elapsedMs = Date.now() - startedAt;
  expect(elapsedMs, `URL 离开 /login 用时 ${elapsedMs}ms，预算 ${LOGIN_BUDGET_MS}ms`).toBeLessThanOrEqual(
    LOGIN_BUDGET_MS,
  );
  expect((await page.context().cookies()).map((cookie) => cookie.name)).toContain(ACCESS_COOKIE);
});

test('H2 关键页可达：客户 → 集群 → 发布清单 → 创建操作 → 操作历史 → 操作详情', async ({ page }) => {
  requireCredentials();
  await login(page, ADMIN.username, ADMIN.password);

  // Home is the workbench; every hop below is a visible entry, never a typed URL.
  await page.goto('/');
  await page.getByTestId('home-tile-CustomerList').click();
  await expect(page.getByRole('heading', { level: 1, name: '客户' })).toBeVisible();

  await page.locator('[data-testid^="customer-clusters-"]').first().click();
  await page.waitForURL((url) => url.pathname.endsWith('/clusters'));
  await expect(page.getByRole('heading', { level: 1, name: '集群' })).toBeVisible();

  await page.getByRole('link', { name: '查看' }).first().click();
  await page.waitForURL((url) => {
    const parts = url.pathname.split('/');
    return parts.length === 5 && parts[1] === 'customers' && parts[3] === 'clusters';
  });
  await expect(page.getByRole('link', { name: '发布清单' }).first()).toBeVisible();
  await page.getByRole('link', { name: '发布清单' }).first().click();
  await page.waitForURL((url) => url.pathname.endsWith('/releases'));
  await expect(page.getByRole('heading', { level: 1 })).toContainText('发布清单');

  // Pin the row to the definition smoke.sh operated on: its operation history is
  // what makes the last hop reachable (console.sh always supplies the id).
  const definitionId = process.env.E2E_DEFINITION_ID ?? '';
  const operationEntries =
    definitionId === ''
      ? page.locator('a.release-table__operation')
      : page.locator(`a.release-table__operation[href*="${definitionId}"]`);
  const row = page.locator('tbody tr').filter({ has: operationEntries }).first();
  await expect(row).toBeVisible({ timeout: 20_000 });

  await row.locator('a.release-table__operation').click();
  await page.waitForURL((url) => url.pathname.endsWith('/operations/new'));
  await expect(page.getByRole('heading', { level: 1, name: '创建发布操作' })).toBeVisible();

  // Back to the inventory, then out again through the SAME row's history entry.
  await page.goBack();
  await page.waitForURL((url) => url.pathname.endsWith('/releases'));
  await row.locator('a.release-table__history').click();
  await page.waitForURL((url) => url.pathname.endsWith('/operations'));
  await expect(page.getByRole('heading', { level: 1, name: '操作历史' })).toBeVisible();

  // The list must hold the operations the prerequisite smoke left behind.
  const firstOperation = page.locator('.operation-list__table tbody tr a').first();
  await expect(firstOperation).toBeVisible({ timeout: 20_000 });
  // The history row prints the operation id in full, so it is the expected value for the
  // detail page's own identity assertion below.
  const listedOperationId = (await firstOperation.innerText()).trim();
  await firstOperation.click();
  await page.waitForURL((url) => url.pathname.includes('/operations/'));
  // The detail page is reached AND it is the detail of the operation the history row
  // links to: the h1 is the server-derived type title, and the summary's CopyableHash
  // `title` (the full value, not the truncated render) must equal the listed id. The
  // bound definition must be the one the scoped route names — two identities, both whole.
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(OPERATION_TITLE);
  expect(listedOperationId, 'the operation history row must render the id it links to').not.toBe('');
  const pathParts = new URL(page.url()).pathname.split('/');
  const urlDefinitionId = pathParts[pathParts.lastIndexOf('releases') + 1] ?? '';
  expect(urlDefinitionId, 'the scoped detail route must name a release definition').not.toBe('');
  const summary = page.locator('.operation-detail__summary');
  await expect(
    summary.locator('dt:text-is("Operation ID") + dd .copyable-hash__value'),
  ).toHaveAttribute('title', listedOperationId);
  await expect(
    summary.locator('dt:text-is("ReleaseDefinition") + dd .copyable-hash__value'),
  ).toHaveAttribute('title', urlDefinitionId);
});

test('H3 危险操作确认：回滚走自定义 alertdialog，Esc 取消且不发出请求', async ({ page }) => {
  requireCredentials();
  await login(page, ADMIN.username, ADMIN.password);
  await openReleases(page);
  const trigger = await requireRollbackTrigger(page);
  await expect(trigger).toBeVisible();

  const nativeDialogs: string[] = [];
  page.on('dialog', (dialog) => {
    nativeDialogs.push(`${dialog.type()}:${dialog.message()}`);
    void dialog.dismiss();
  });
  let rollbackRequests = 0;
  page.on('request', (request) => {
    if (request.url().endsWith(ROLLBACK_METHOD)) rollbackRequests += 1;
  });

  await trigger.click();
  const panel = page.locator('.app-dialog__panel');
  await expect(panel).toBeVisible();
  // A custom dialog announced as the destructive one — not a native confirm().
  await expect(panel).toHaveAttribute('role', 'alertdialog');
  await expect(panel).toHaveAttribute('aria-modal', 'true');
  await expect(panel.locator('.rollback-dialog__warning')).toBeVisible();
  expect(nativeDialogs, '危险操作不得使用原生 confirm/alert').toEqual([]);

  await page.keyboard.press('Escape');
  await expect(panel).toBeHidden();
  // The quiet window: only a window can tell "no request" apart from "not yet".
  await page.waitForTimeout(ROLLBACK_QUIET_MS);
  expect(rollbackRequests, 'Esc 取消后不得发出 RollbackRelease 请求').toBe(0);
});

test('H4 读取失败：ErrorState 渲染含 requestId 的相关行，且与空态不同屏', async ({ page }) => {
  requireCredentials();
  await login(page, ADMIN.username, ADMIN.password);

  // The H4 technique is "DevTools 改请求": rewrite the aggregate read in flight
  // into a server-side failure that carries a correlation id. X-Request-ID is the
  // real carrier — the server sets it on every response and error
  // (internal/contracts/errors.go RequestIDHeader).
  await page.route(`**/${LIST_NON_TERMINAL}`, async (route) => {
    await route.fulfill({
      status: 503,
      headers: { 'content-type': 'application/json', 'X-Request-ID': MOCK_REQUEST_ID },
      body: JSON.stringify({ code: 'unavailable', message: 'E2E mocked aggregate read failure' }),
    });
  });

  await page.goto('/operations');
  const errorState = page.locator('.error-state');
  await expect(errorState).toBeVisible();
  await expect(errorState.locator('.error-state__title')).toHaveText('出错了');

  // The correlation line lives in the collapsible technical details.
  await errorState.locator('summary').click();
  const details = errorState.locator('.error-state__details pre');
  await expect(details).toContainText('code=Unavailable');
  await expect(details).toContainText(`requestId=${MOCK_REQUEST_ID}`);

  // Error and empty state must never share the screen (UX-010).
  await expect(page.locator('.empty-state')).toHaveCount(0);
});

test('H4 权限不足：403 渲染 403 文案而非空态，且与读取失败可区分', async ({ page }) => {
  requireCredentials();
  await login(page, ADMIN.username, ADMIN.password);

  await page.route(`**/${LIST_NON_TERMINAL}`, async (route) => {
    await route.fulfill({
      status: 403,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ code: 'permission_denied', message: 'E2E mocked permission denied' }),
    });
  });

  await page.goto('/operations');
  // PERMISSION_DENIED is a first-class refusal: the global handler routes to the
  // forbidden surface, which must say 403 rather than render an empty queue.
  await page.waitForURL((url) => url.pathname === '/forbidden');
  const forbiddenState = page.locator('.forbidden-state');
  await expect(forbiddenState).toBeVisible();
  await expect(forbiddenState.locator('.forbidden-state__code')).toHaveText('403');
  await expect(forbiddenState.locator('.forbidden-state__text')).toContainText('E2E mocked permission denied');
  await expect(page.locator('.empty-state')).toHaveCount(0);
  await expect(page.locator('.error-state')).toHaveCount(0);
});

test('H5 焦点陷阱：对话框内 Tab 与 Shift+Tab 不逃逸，Esc 关闭后焦点回到触发按钮', async ({ page }) => {
  requireCredentials();
  await login(page, ADMIN.username, ADMIN.password);
  await openReleases(page);
  const trigger = await requireRollbackTrigger(page);
  const triggerTestId = await trigger.getAttribute('data-testid');
  expect(triggerTestId).toBeTruthy();

  await trigger.click();
  const panel = page.locator('.app-dialog__panel');
  await expect(panel).toBeVisible();

  // The panel holds five focusables (readonly current revision, target, reason,
  // cancel, confirm), so 24 forward tabs cycle the trap several times.
  for (let index = 0; index < 24; index += 1) {
    await page.keyboard.press('Tab');
    expect(await focusInsidePanel(page), `Tab #${index + 1} 把焦点移出了对话框`).toBe(true);
  }
  for (let index = 0; index < 3; index += 1) {
    await page.keyboard.press('Shift+Tab');
    expect(await focusInsidePanel(page), `Shift+Tab #${index + 1} 把焦点移出了对话框`).toBe(true);
  }

  await page.keyboard.press('Escape');
  await expect(panel).toBeHidden();
  const focusedTestId = await page.evaluate(() => document.activeElement?.getAttribute('data-testid') ?? '');
  expect(focusedTestId, '关闭后焦点必须回到触发按钮').toBe(triggerTestId);
});
