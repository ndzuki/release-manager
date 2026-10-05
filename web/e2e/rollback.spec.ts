// Rollback console E2E (TASK-240 / REQ-056 AC-056-08, AC-067-16).
//
// Scope note (TASK-240 复核收窄): the server RPC path is already covered for
// real by test/e2e/prerequisite/smoke.sh (RollbackRelease with the canonical
// payload + a terminal wait). What had NO end-to-end coverage is the BROWSER
// path: open the rollback form from the release inventory, fill
// target revision + reason, submit, and land on the created operation's detail.
// This spec adds exactly that, plus the missing negative control (an illegal
// target_revision must be refused by the SERVER, not only by the form).
//
// Environment contract: same as e2e/emergency-smoke.spec.ts. The full dev stack
// (auth + orchestrator + operator + Postgres + registry) must be up and seeded,
// and the target release must be installed at revision > 1. Set
// E2E_BACKEND=true to activate; without it every case skips with an explicit
// reason — never silently shrunk.
//
//   E2E_BACKEND=true
//   E2E_BASE_URL           console base (default http://127.0.0.1:5173)
//   E2E_ADMIN_A_USER/PASS  a write-capable account (default dev-admin)
//   E2E_CUSTOMER_ID        optional; with E2E_CLUSTER_ID it skips the UI hops
//   E2E_CLUSTER_ID         optional; see above
//
// Precondition for revision > 1: `make e2e-stage STAGES=release` performs one
// UPGRADE on the e2e-release-target definition (baseline 1 -> 2) after
// `make dev-up dev-seed dev-status`.
import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';
import { fromBinary, toBinary } from '@bufbuild/protobuf';
import {
  RollbackReleaseRequestSchema,
  RollbackReleaseResponseSchema,
} from '../src/gen/orchestrator/v1/orchestrator_pb';

test.beforeEach(() => {
  test.skip(
    process.env.E2E_BACKEND !== 'true',
    'rollback console spec requires the dev/backing stack (E2E_BACKEND=true) — same contract as the emergency smoke',
  );
});

const ADMIN = {
  username: process.env.E2E_ADMIN_A_USER ?? 'dev-admin',
  password: process.env.E2E_ADMIN_A_PASS ?? '',
};

// The RPC method name only: a literal camel-case procedure path would itself be
// read as an English locator by the locator-hygiene gate (see
// src/i18n/e2e-locator.usage.test.ts). Matching on the method suffix keeps the
// spec free of that coupling and stays exact enough (no other procedure ends
// with it).
const ROLLBACK_METHOD = 'RollbackRelease';

// The statuses RollbackRelease accepts into: it CASes the new operation to
// preflight and answers with that state (internal/orchestrator/rollback.go
// builds the response before launching the coordinator).
const ACCEPTED_STATES = /^(pending|preflight)$/;

const ROLLBACK_ROW = '[data-testid^="release-rollback-"]';

const PRE_REVISION_PRECONDITION =
  'no release on this cluster is bound to a ReleaseDefinition at revision > 1, so the rollback entry does not render (run `make e2e-stage STAGES=release` after `make dev-up dev-seed dev-status` to install+upgrade the e2e-release-target)';

async function login(page: Page, username: string, password: string): Promise<void> {
  await page.goto('/login');
  // autocomplete attributes, not copy: the login labels are localized, and a
  // text locator here would be a coupling to copy that keeps changing.
  await page.locator('input[autocomplete="username"]').fill(username);
  await page.locator('input[autocomplete="current-password"]').fill(password);
  await page.locator('button[type="submit"]').click();
  await page.waitForURL((url) => !url.pathname.startsWith('/login'));
}

// Direct URL when the harness supplies the fixture ids, otherwise walk the IA
// (customers -> clusters -> release inventory). Every hop asserts the URL it
// reached, so a silently wrong page cannot masquerade as the inventory.
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
  // Split, not a path regex: a spaced regex literal here reads as an English
  // locator to the locator-hygiene gate.
  await page.waitForURL((url) => {
    const parts = url.pathname.split('/');
    return parts.length === 5 && parts[1] === 'customers' && parts[3] === 'clusters';
  });
  await page.getByRole('link', { name: '发布清单' }).first().click();
  await page.waitForURL((url) => url.pathname.endsWith('/releases'));
}

// Waits for the inventory to settle, then returns the first rollback trigger.
// A count of 0 is a real precondition failure, not a pass: the caller skips with
// PRE_REVISION_PRECONDITION.
async function rollbackTrigger(page: Page): Promise<Locator> {
  await page
    .locator('tbody tr, [data-testid="release-inventory-empty"]')
    .first()
    .waitFor({ state: 'attached', timeout: 20_000 })
    .catch(() => undefined);
  const trigger = page.locator(ROLLBACK_ROW).first();
  await trigger.waitFor({ state: 'attached', timeout: 20_000 }).catch(() => undefined);
  return trigger;
}

function scopeFromUrl(page: Page): { customerId: string; clusterId: string } {
  const parts = new URL(page.url()).pathname.split('/');
  return { customerId: parts[2] ?? '', clusterId: parts[4] ?? '' };
}

// The row's bound definition id, read from the operation-create link it renders
// (same projection the rollback trigger is derived from — no fixture coupling).
async function definitionIdForRow(page: Page): Promise<string> {
  const row = page.locator('tbody tr').filter({ has: page.locator(ROLLBACK_ROW) }).first();
  const href = await row.locator('a.release-table__operation').getAttribute('href');
  return href?.split('/releases/')[1]?.split('/')[0] ?? '';
}

function isRollbackRequest(url: string): boolean {
  return url.endsWith(ROLLBACK_METHOD);
}

test('Rollback from the release inventory creates a ROLLBACK operation and lands on its detail (AC-240-02)', async ({ page }) => {
  test.skip(ADMIN.password === '', 'E2E_ADMIN_A_PASS is required');
  await login(page, ADMIN.username, ADMIN.password);
  await openReleases(page);

  const trigger = await rollbackTrigger(page);
  test.skip((await trigger.count()) === 0, PRE_REVISION_PRECONDITION);
  await expect(trigger).toBeVisible();

  const scope = scopeFromUrl(page);
  const definitionId = await definitionIdForRow(page);
  expect(definitionId).not.toBe('');

  await trigger.click();

  // The dialog reports the CURRENT revision it read from the inventory; the
  // previous revision is the only target the form will accept.
  const currentRevision = Number(await page.locator('input[name="currentRevision"]').inputValue());
  expect(currentRevision).toBeGreaterThan(1);
  const targetRevision = currentRevision - 1;
  await page.locator('input[name="targetRevision"]').fill(String(targetRevision));
  await page.locator('textarea[name="reason"]').fill('E2E TASK-240: console rollback to the previous revision');

  const requestPromise = page.waitForRequest((request) => isRollbackRequest(request.url()));
  const responsePromise = page.waitForResponse((response) => isRollbackRequest(response.url()));
  await page.getByTestId('rollback-submit').click();
  const [request, response] = await Promise.all([requestPromise, responsePromise]);

  // ── Evidence from the REQUEST (what the console actually put on the wire) ──
  const wireRequest = fromBinary(
    RollbackReleaseRequestSchema,
    new Uint8Array(request.postDataBuffer() ?? new ArrayBuffer(0)),
  );
  expect(wireRequest.releaseDefinitionId).toBe(definitionId);
  expect(wireRequest.targetRevision).toBe(targetRevision);
  expect(wireRequest.expectedCurrentRevision).toBe(currentRevision);
  expect(request.headers()['idempotency-key']).toBeTruthy();
  // AC-240-02(b) request half: the deprecated values fields are never sent, so
  // rollback_values_not_allowed has nothing to reject.
  expect(wireRequest.valuesRevisionId).toBe('');
  expect(wireRequest.valuesPatch).toBe('');

  // ── Evidence from the RESPONSE (the server accepted this request shape) ──
  expect(response.status()).toBe(200);
  const wireResponse = fromBinary(
    RollbackReleaseResponseSchema,
    new Uint8Array(await response.body()),
  );
  expect(wireResponse.operationId).not.toBe('');
  expect(wireResponse.state).toMatch(ACCEPTED_STATES);
  // AC-240-02(b) response half: a 200 with an operation id is the opposite of
  // an INVALID_ARGUMENT rollback_values_not_allowed refusal.
  expect(await response.text()).not.toContain('rollback_values_not_allowed');

  // ── AC-240-02(a)+(c): the PAGE, after the request, is the operation's detail ──
  await page.waitForURL((url) => url.pathname.endsWith(`/operations/${wireResponse.operationId}`));
  const finalPath = new URL(page.url()).pathname;
  expect(finalPath).toBe(
    `/customers/${scope.customerId}/clusters/${scope.clusterId}/releases/${definitionId}/operations/${wireResponse.operationId}`,
  );
  // (a) the type comes from the server-rendered operation on the detail page:
  // ROLLBACK, not INSTALL/UPGRADE. The status is one of the operation statuses.
  await expect(page.locator('.operation-detail__eyebrow')).toHaveText('回滚操作');
  await expect(page.locator('.operation-detail__header code')).toHaveText(wireResponse.operationId);
  await expect(page.locator('.operation-detail__state')).toHaveText(
    /^(等待中|预检|排队中|执行中|取消中|成功|失败|已取消|超时)$/,
  );
});

test('an illegal target revision is refused by the SERVER, not only by the form guard (AC-240-03)', async ({ page }) => {
  test.skip(ADMIN.password === '', 'E2E_ADMIN_A_PASS is required');
  await login(page, ADMIN.username, ADMIN.password);
  await openReleases(page);

  const trigger = await rollbackTrigger(page);
  test.skip((await trigger.count()) === 0, PRE_REVISION_PRECONDITION);
  await expect(trigger).toBeVisible();
  await trigger.click();

  const currentRevision = Number(await page.locator('input[name="currentRevision"]').inputValue());
  expect(currentRevision).toBeGreaterThan(1);

  // Part 1, the client guard, stated so the two are not confused: the form
  // itself refuses target == current and sends nothing. This is the guard the
  // server check must not be confused with.
  let rollbackRequests = 0;
  page.on('request', (request) => {
    if (isRollbackRequest(request.url())) rollbackRequests += 1;
  });
  await page.locator('input[name="targetRevision"]').fill(String(currentRevision));
  await page.locator('textarea[name="reason"]').fill('E2E TASK-240: negative control');
  await page.getByTestId('rollback-submit').click();
  await expect(page.locator('.rollback-dialog__error')).toContainText('目标 Revision 必须小于当前 Revision');
  expect(rollbackRequests, 'the client guard must not send an illegal target').toBe(0);

  // Part 2, the SERVER guard. The form will never send target >= expected, so
  // the only way to prove the server rejects it is to rewrite the outgoing
  // payload in flight: the client sends a legal-looking request, the real server
  // receives target == expected and must answer INVALID_ARGUMENT. Both the
  // rejection and the rendered error below are real.
  let tampered: { targetRevision: number; expectedCurrentRevision: number } | null = null;
  await page.route(`**/${ROLLBACK_METHOD}`, async (route) => {
    const raw = route.request().postDataBuffer();
    if (!raw) {
      await route.continue();
      return;
    }
    const message = fromBinary(RollbackReleaseRequestSchema, new Uint8Array(raw));
    message.targetRevision = message.expectedCurrentRevision;
    tampered = {
      targetRevision: message.targetRevision,
      expectedCurrentRevision: message.expectedCurrentRevision,
    };
    await route.continue({
      postData: Buffer.from(toBinary(RollbackReleaseRequestSchema, message)),
    });
  });

  const responsePromise = page.waitForResponse((response) => isRollbackRequest(response.url()));
  // Clear the client-side validation error by submitting a target the client
  // accepts; the route handler rewrites it to the equal one the server refuses.
  await page.locator('input[name="targetRevision"]').fill(String(currentRevision - 1));
  await page.getByTestId('rollback-submit').click();
  const response = await responsePromise;

  expect(tampered).not.toBeNull();
  const sent = tampered as unknown as { targetRevision: number; expectedCurrentRevision: number };
  expect(sent.targetRevision).toBe(sent.expectedCurrentRevision);

  // Evidence from the RESPONSE: Connect InvalidArgument is HTTP 400, and the
  // server's machine message names the field it refused.
  expect(response.status()).toBe(400);
  expect(await response.text()).toContain('target_revision');

  // Evidence from the PAGE: the refusal is user-visible, and no operation was
  // created (no detail navigation).
  await expect(page.locator('.error-state__title')).toHaveText('回滚未成功');
  await expect(page.locator('.error-state__text')).toContainText('目标 Revision 不合法');
  expect(new URL(page.url()).pathname.endsWith('/releases')).toBe(true);
});
