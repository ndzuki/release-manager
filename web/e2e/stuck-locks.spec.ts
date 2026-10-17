// Stuck-lock console E2E (TASK-328 / REQ-087 §4.2, AC-087-08/09/10).
//
// Why this file exists: the console lane covered rollback, emergency + convergence and the
// H1–H5 delivery criteria, but not the stuck-lock page (web/src/pages/StuckLocksPage.vue) —
// the only console surface for `ReleaseEmergencyLock`, the RPC that reopens a definition
// whose cluster effect is still unknown. A routing, rendering or wiring regression there
// could ship unseen, which is the gap TASK-328 records.
//
// Environment contract: identical to e2e/rollback.spec.ts. E2E_BACKEND is the ONLY skip
// gate; once the stack is declared up, a missing credential, route, rendered state or entry
// point is a HARD FAILURE with diagnostics — never a skip. Every case here is declared in
// test/e2e/prerequisite/console-expected-cases.json (TASK-307), so a silent skip fails the
// lane instead of passing it.
//
// What is real, and what is rewritten in flight:
//   * S1 runs entirely against the real stack. It does NOT require a stuck lock to exist: it
//     asserts the page settles into exactly one terminal state (the empty state or the lock
//     table) with no error state — the invariant that holds on any stack, whether it has a
//     stuck lock or not.
//   * A REAL stuck lock cannot be produced by this lane. It needs a terminal EMERGENCY whose
//     effect is still UNKNOWN after `emergency.effect_observe_timeout`
//     (internal/orchestrator/emergency_stuck.go:73-107), i.e. the operator must miss the
//     observation window; this lane deliberately keeps the operator online and converging
//     (smoke.sh drives the emergency path to a terminal effect). So S2/S3 rewrite the
//     `ListStuckLocks` / `ReleaseEmergencyLock` responses in flight — the same technique the
//     H4 case (e2e/delivery-criteria.spec.ts) and the illegal-rollback case
//     (e2e/rollback.spec.ts) use. S2 mocks only the READ and asserts that no release request
//     leaves the browser; S3 decodes the request the page actually sends, so the form → RPC
//     wiring is asserted rather than assumed. The server's release semantics are covered by
//     internal/orchestrator/emergency_stuck_handler_test.go.
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';
import { create, fromBinary, toBinary } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import {
  EmergencyAction,
  EmergencyEffectStatus,
  ListStuckLocksResponseSchema,
  ReleaseEmergencyLockRequestSchema,
  ReleaseEmergencyLockResponseSchema,
  ReleaseMode,
  StuckLockSchema,
  type ReleaseEmergencyLockRequest,
} from '../src/gen/orchestrator/v1/orchestrator_pb';

test.beforeEach(() => {
  test.skip(
    process.env.E2E_BACKEND !== 'true',
    'stuck-locks spec requires the dev/backing stack (E2E_BACKEND=true) — same contract as the rollback and emergency specs',
  );
});

const ADMIN = {
  username: process.env.E2E_ADMIN_A_USER ?? 'dev-admin',
  password: process.env.E2E_ADMIN_A_PASS ?? '',
};

// Method names only: a literal camel-case procedure path would be read as an English locator
// by the locator-hygiene gate (src/i18n/e2e-locator.usage.test.ts), and matching on the
// method suffix is exact enough (no other procedure ends with either name).
const STUCK_LOCKS_METHOD = 'ListStuckLocks';
const RELEASE_LOCK_METHOD = 'ReleaseEmergencyLock';

const STUCK_LOCKS_PATH = '/emergency/stuck-locks';
const LOCK_ID = 'e2e-stuck-lock-1';
const REASON = 'E2E TASK-328: cluster observation confirms the command never took effect';

// The dialog closing is not evidence that nothing was submitted: a request can leave a beat
// later. The count is therefore read across this bounded quiet window (same reasoning as the
// H3 rollback case).
const QUIET_MS = 500;

// E2E_BACKEND=true means the environment was declared up: a missing credential is a
// misconfiguration that must fail, not silently skip the whole case.
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

function lockMessage() {
  const terminalAt = new Date(Date.now() - 10 * 60_000);
  return create(StuckLockSchema, {
    intentId: LOCK_ID,
    operationId: 'e2e-stuck-operation',
    releaseDefinitionId: process.env.E2E_DEFINITION_ID ?? 'e2e-release-target',
    action: EmergencyAction.SET_REPLICAS,
    lockPathSummary: 'Deployment/api, container=app',
    effectStatus: EmergencyEffectStatus.UNKNOWN,
    terminalAt: timestampFromDate(terminalAt),
    stuckSince: timestampFromDate(terminalAt),
    observeTimeoutDisplay: '5m0s',
  });
}

// serveList installs the in-flight ListStuckLocks rewrite. With hideAfterFirstRead the lock
// is absent from the SECOND read, which is what the store does after a release (it re-reads
// the list before reporting), so the row disappearing is the page's own behaviour rather
// than an artefact of the mock.
async function serveList(page: Page, hideAfterFirstRead = false): Promise<void> {
  const lock = lockMessage();
  let reads = 0;
  await page.route(`**/${STUCK_LOCKS_METHOD}`, async (route) => {
    reads += 1;
    const locks = hideAfterFirstRead && reads > 1 ? [] : [lock];
    const message = create(ListStuckLocksResponseSchema, { locks });
    await route.fulfill({
      status: 200,
      headers: { 'content-type': 'application/proto' },
      body: Buffer.from(toBinary(ListStuckLocksResponseSchema, message)),
    });
  });
}

test('S1 解锁页可达：从工作台进入，且渲染空态或有锁态（不同屏、不落错误态）', async ({ page }) => {
  requireCredentials();
  await login(page, ADMIN.username, ADMIN.password);

  // The entry is the workbench tile, not a typed URL: the tile is the visible entry a
  // routing regression would hide (TASK-240's class of defect).
  await page.goto('/');
  await page.getByTestId('home-tile-StuckLocks').click();
  await page.waitForURL((url) => url.pathname === STUCK_LOCKS_PATH);
  await expect(page.getByRole('heading', { level: 1, name: '卡住的紧急锁' })).toBeVisible();

  // Exactly one terminal state, whichever the real stack holds. Waiting for the first of
  // the two is what keeps this case honest on a stack with no stuck lock.
  const empty = page.locator('.empty-state');
  const table = page.locator('.stuck__table');
  await expect(empty.or(table).first()).toBeVisible({ timeout: 20_000 });
  const states = (await empty.count()) + (await table.count());
  expect(states, '空态与有锁态必须恰好渲染一个').toBe(1);
  await expect(page.locator('.error-state')).toHaveCount(0);
  await expect(page.locator('.forbidden-state')).toHaveCount(0);
});

test('S2 释放确认：有锁态打开 alertdialog，Esc 取消且不发出释放请求', async ({ page }) => {
  requireCredentials();
  await serveList(page);
  await login(page, ADMIN.username, ADMIN.password);

  let releaseRequests = 0;
  page.on('request', (request) => {
    if (request.url().endsWith(RELEASE_LOCK_METHOD)) releaseRequests += 1;
  });

  await page.goto(STUCK_LOCKS_PATH);
  const row = page.getByTestId(`stuck-lock-${LOCK_ID}`);
  await expect(row).toBeVisible({ timeout: 20_000 });
  // The row shows the lock's identity, not a placeholder.
  await expect(row).toContainText('Deployment/api, container=app');

  const trigger = row.getByRole('button', { name: '释放锁' });
  await expect(trigger).toBeVisible();
  await trigger.click();

  const panel = page.locator('.app-dialog__panel');
  await expect(panel).toBeVisible();
  // A custom dialog announced as the irreversible one — not a native confirm().
  await expect(panel).toHaveAttribute('role', 'alertdialog');
  await expect(panel).toHaveAttribute('aria-modal', 'true');
  await expect(panel.locator('.app-dialog__title')).toContainText('Deployment/api, container=app');
  // The irreversibility warning must be associated with the dialog, not a loose paragraph.
  await expect(panel).toHaveAttribute('aria-describedby', /.+/);

  await page.keyboard.press('Escape');
  await expect(panel).toBeHidden();
  // The quiet window: only a window can tell "no request" apart from "not yet".
  await page.waitForTimeout(QUIET_MS);
  expect(releaseRequests, 'Esc 取消后不得发出 ReleaseEmergencyLock 请求').toBe(0);
  // Cancelling must also leave the list untouched.
  await expect(row).toBeVisible();
});

test('S3 释放提交：空原因被服务前守卫拦住，提交后载荷与可见反馈一致', async ({ page }) => {
  requireCredentials();
  await serveList(page, true);

  let sent: ReleaseEmergencyLockRequest | null = null;
  await page.route(`**/${RELEASE_LOCK_METHOD}`, async (route) => {
    const body = route.request().postDataBuffer();
    if (body) sent = fromBinary(ReleaseEmergencyLockRequestSchema, new Uint8Array(body));
    const message = create(ReleaseEmergencyLockResponseSchema, {
      intentId: LOCK_ID,
      lockReleased: true,
      effectStatus: EmergencyEffectStatus.NOT_APPLIED,
      auditEventId: 'e2e-stuck-audit-1',
    });
    await route.fulfill({
      status: 200,
      headers: { 'content-type': 'application/proto' },
      body: Buffer.from(toBinary(ReleaseEmergencyLockResponseSchema, message)),
    });
  });

  await login(page, ADMIN.username, ADMIN.password);
  await page.goto(STUCK_LOCKS_PATH);
  const row = page.getByTestId(`stuck-lock-${LOCK_ID}`);
  await expect(row).toBeVisible({ timeout: 20_000 });
  await row.getByRole('button', { name: '释放锁' }).click();

  const panel = page.locator('.app-dialog__panel');
  await expect(panel).toBeVisible();

  // Guard first: releasing a lock is not replayable, so an unjustified submission must be
  // refused by the page itself and must not reach the server.
  await page.getByTestId('stuck-submit').click();
  await expect(page.getByTestId('stuck-validation')).toBeVisible();
  expect(sent, '空原因不得发出 ReleaseEmergencyLock 请求').toBeNull();

  await page.locator('textarea[name="reason"]').fill(REASON);
  await page.locator('input[name="mode"][value="AUDITED_OVERRIDE"]').check();
  await page.locator('input[name="acknowledged"]').check();
  await page.getByTestId('stuck-submit').click();

  await expect(panel).toBeHidden();
  const notice = page.getByTestId('stuck-notice');
  await expect(notice).toBeVisible();
  await expect(notice).toContainText(`已释放锁 ${LOCK_ID}`);

  // The request the page really sent is the evidence that the form is wired to the RPC.
  const request = sent as unknown as ReleaseEmergencyLockRequest;
  expect(request.intentId).toBe(LOCK_ID);
  expect(request.reason).toBe(REASON);
  expect(request.mode).toBe(ReleaseMode.AUDITED_OVERRIDE);
  expect(request.evidence).toBe('');

  // The store's own re-read is what removes the row; the empty state takes its place and no
  // error state shares the screen (UX-010).
  await expect(row).toHaveCount(0);
  await expect(page.locator('.empty-state')).toBeVisible();
  await expect(page.locator('.error-state')).toHaveCount(0);
});
