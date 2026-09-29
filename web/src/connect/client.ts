import { t } from '@/i18n/messages';
import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { AuthService, AuthorizationService, BindingService, OrganizationService } from '@/gen/auth/v1/auth_pb';
import { AuditService } from '@/gen/audit/v1/audit_pb';
import { TrustService } from '@/gen/trust/v1/trust_pb';
import { CleanupService } from '@/gen/orchestrator/v1/cleanup_pb';
import { BundleService, OrchestratorService } from '@/gen/orchestrator/v1/orchestrator_pb';

const csrfCookieName = 'rm_csrf';
const csrfHeaderName = 'X-CSRF-Token';

/**
 * Deadline for a unary RPC (AC-178-06).
 *
 * Without it a dropped network took the console down permanently: the fetch
 * promise never settles (a blackholed packet path never sends RST), the awaited
 * `auth.initialize()` in main.ts never resolves, and the boot-failure page never
 * runs because it is only reached from a REJECTION.
 *
 * 15s is 30x REQ-058's server-side budget for the query RPCs (p95 <= 500 ms) and
 * 15x its budget for Execute acceptance (p95 <= 1 s), so a unary call that hits
 * this ceiling is already far outside the contract; anything slower should be a
 * stream (exempt below) or an accepted-then-polled operation.
 *
 * Tradeoff: bootstrap is bounded at 20s (see BOOT_TIMEOUT_MS), which is below
 * "three sequential unary calls each at this ceiling". A backend that is slow but
 * progressing therefore fails fast and asks the user to retry rather than
 * leaving them on a blank page.
 */
export const UNARY_RPC_TIMEOUT_MS = 15_000;

/**
 * Procedures whose response is an open-ended stream, so a deadline would abort a
 * healthy connection. Keep this list explicit: a new streaming RPC that misses it
 * would be silently cut off after UNARY_RPC_TIMEOUT_MS.
 */
export const STREAMING_PROCEDURES = ['/orchestrator.v1.OrchestratorService/WatchOperation'];

/**
 * Unary procedures whose server-side budget exceeds the default deadline.
 *
 * RunCleanup runs the retention collector synchronously and the contract says its
 * budget is "up to about an hour, so a client timeout must be generous" — the 15s
 * unary deadline would abort a healthy collection mid-flight.
 */
export const LONG_UNARY_PROCEDURES: Record<string, number> = {
  '/orchestrator.v1.CleanupService/RunCleanup': 65 * 60 * 1000,
};

export type AuthErrorHandler = (error: ConnectError) => void | Promise<void>;

let authErrorHandler: AuthErrorHandler | undefined;

export function setAuthErrorHandler(handler: AuthErrorHandler | undefined): void {
  authErrorHandler = handler;
}

export function readCookie(name: string): string | undefined {
  const encodedName = `${encodeURIComponent(name)}=`;
  for (const part of document.cookie.split(';')) {
    const cookie = part.trim();
    if (cookie.startsWith(encodedName)) {
      return decodeURIComponent(cookie.slice(encodedName.length));
    }
  }
  return undefined;
}

/**
 * True when the failure is OUR deadline, so it can be mapped to a stable
 * DeadlineExceeded instead of leaking "The operation was aborted" / an opaque
 * internal error.
 *
 * Our deadline aborts with `DOMException(t('client.rpcTimeout'), 'TimeoutError')`; a
 * caller-side cancellation stays `AbortError` and must keep Canceled semantics.
 * A `TimeoutError` raised by something else (AbortSignal.timeout, a library)
 * would also classify as a deadline — acceptable, since it means the same thing
 * to the user, but this is not a "ours only" test.
 */
export function isTimeoutError(error: unknown): boolean {
  let current: unknown = error;
  for (let depth = 0; current && depth < 5; depth++) {
    if (current instanceof DOMException && current.name === 'TimeoutError') return true;
    if (current instanceof Error && (current.name === 'TimeoutError' || current.name === 'AbortError')) {
      // Only our own deadline uses TimeoutError; a caller abort stays AbortError
      // and must keep its Canceled semantics.
      return current.name === 'TimeoutError';
    }
    current = (current as { cause?: unknown }).cause;
  }
  return false;
}

/*
 * Procedures whose UNAUTHENTICATED answer is a VERDICT, not "the session expired".
 * Both are defined by REQ-025's error model:
 *   /auth.v1.AuthService/ChangePassword -> invalid old password
 *   /auth.v1.AuthService/Login          -> invalid credentials
 * Routing them through the global handler cleared the user's session silently and
 * (for ChangePassword) showed no error at all — found by the dev-env E2E of
 * TASK-185. Matching the FULL procedure keeps a future service's same-named RPC
 * from being exempted by accident.
 */
const UNAUTHENTICATED_IS_VERDICT = new Set([
  '/auth.v1.AuthService/ChangePassword',
  '/auth.v1.AuthService/Login',
]);

/** Fully qualified procedure path, or "" when the request shape is unavailable. */
function procedureOf(request: { method?: { name?: string; parent?: { typeName?: string } } }): string {
  const name = request.method?.name;
  const service = request.method?.parent?.typeName;
  return name && service ? `/${service}/${name}` : '';
}

export const sessionInterceptor: Interceptor = (next) => async (request) => {
  const csrfToken = readCookie(csrfCookieName);
  if (csrfToken) {
    request.header.set(csrfHeaderName, csrfToken);
  }

  try {
    return await next(request);
  } catch (error) {
    if (isTimeoutError(error)) {
      // Note: a response whose HEADERS arrived (e.g. a 401) but whose body never
      // finished now reports DeadlineExceeded, so the session handler does not
      // run for that caller. Retrying re-establishes the session; keeping a dead
      // request open forever is the worse failure.
      throw new ConnectError(t('client.timeout'), Code.DeadlineExceeded);
    }
    const connectError = ConnectError.from(error);
    const verdictIsNotSessionLoss =
      connectError.code === Code.Unauthenticated && UNAUTHENTICATED_IS_VERDICT.has(procedureOf(request));
    if (!verdictIsNotSessionLoss && (connectError.code === Code.Unauthenticated || connectError.code === Code.PermissionDenied)) {
      await authErrorHandler?.(connectError);
    }
    throw connectError;
  }
};

function requestUrl(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.href;
  return input.url;
}

/**
 * Path of a request URL, for an exact procedure comparison: a substring match
 * would exempt any future `/WatchOperationHistory`-style procedure (or a query
 * string that merely contains the name).
 */
export function procedurePath(url: string): string {
  const path = url.startsWith('http://') || url.startsWith('https://') ? new URL(url).pathname : url;
  const query = path.indexOf('?');
  return query === -1 ? path : path.slice(0, query);
}

/**
 * fetch with a deadline, linked to the caller's own signal (a caller-side abort
 * must still win immediately).
 */
export function fetchWithTimeout(
  input: RequestInfo | URL,
  init: RequestInit | undefined,
  timeoutMs: number,
): Promise<Response> {
  const controller = new AbortController();
  const upstream = init?.signal ?? undefined;
  const abortUpstream = () => controller.abort(upstream?.reason);
  const timer = setTimeout(() => controller.abort(new DOMException(t('client.rpcTimeout'), 'TimeoutError')), timeoutMs);
  if (upstream) {
    if (upstream.aborted) abortUpstream();
    else upstream.addEventListener('abort', abortUpstream, { once: true });
  }
  return fetch(input, { ...init, credentials: 'include', signal: controller.signal }).finally(() => {
    clearTimeout(timer);
    upstream?.removeEventListener('abort', abortUpstream);
  });
}

export function browserFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = requestUrl(input);
  if (STREAMING_PROCEDURES.includes(procedurePath(url))) {
    // No deadline: the caller aborts when it tears the stream down.
    return fetch(input, { ...init, credentials: 'include' });
  }
  const longBudget = LONG_UNARY_PROCEDURES[procedurePath(url)];
  return fetchWithTimeout(input, init, longBudget ?? UNARY_RPC_TIMEOUT_MS);
}

export const transport = createConnectTransport({
  baseUrl: import.meta.env.VITE_API_BASE ?? '',
  useBinaryFormat: true,
  fetch: browserFetch,
  interceptors: [sessionInterceptor],
});

export const authClient = createClient(AuthService, transport);
export const organizationClient = createClient(OrganizationService, transport);
// A2/A3 (UX plan §6.1): bindings and capability grants had no console surface.
export const bindingClient = createClient(BindingService, transport);
export const authorizationClient = createClient(AuthorizationService, transport);
export const orchestratorClient = createClient(OrchestratorService, transport);
export const bundleClient = createClient(BundleService, transport);
export const auditClient = createClient(AuditService, transport);
// A5 (UX plan §6.1): trust roots had no console surface (REQ-012/REQ-043).
export const trustClient = createClient(TrustService, transport);
// A7 (UX plan §6.1): artifact lifecycle GC and bundle restore (REQ-069).
export const cleanupClient = createClient(CleanupService, transport);

