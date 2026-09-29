import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { changePassword } from './auth-api';
import { getOperation } from './operation-api';
import {
  LONG_UNARY_PROCEDURES,
  UNARY_RPC_TIMEOUT_MS,
  browserFetch,
  isTimeoutError,
  procedurePath,
  readCookie,
  sessionInterceptor,
  setAuthErrorHandler,
} from './client';

describe('browser Connect transport', () => {
  afterEach(() => {
    document.cookie = 'rm_csrf=; Max-Age=0; Path=/';
    setAuthErrorHandler(undefined);
    vi.unstubAllGlobals();
  });

  it('reads the CSRF cookie and injects it into write requests', async () => {
    document.cookie = 'rm_csrf=csrf-token; Path=/';
    const next = vi.fn().mockResolvedValue({});
    const request = { header: new Headers() } as Parameters<Parameters<typeof sessionInterceptor>[0]>[0];

    await sessionInterceptor(next)(request);

    expect(readCookie('rm_csrf')).toBe('csrf-token');
    expect(request.header.get('X-CSRF-Token')).toBe('csrf-token');
  });

  it('uses browser cookies for Connect fetches', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response());
    vi.stubGlobal('fetch', fetchMock);

    await browserFetch('/orchestrator.v1.OrchestratorService/ListBundles', { method: 'POST' });

    expect(fetchMock).toHaveBeenCalledWith(
      '/orchestrator.v1.OrchestratorService/ListBundles',
      expect.objectContaining({ method: 'POST', credentials: 'include' }),
    );
  });

  // REQ-025: ChangePassword answers 401 for a WRONG OLD PASSWORD. Passing that to
  // the session handler cleared the user's session silently (found by the dev-env
  // E2E of TASK-185).
  it('does not treat ChangePassword\'s 401 as an expired session', async () => {
    const handler = vi.fn();
    setAuthErrorHandler(handler);
    const error = new ConnectError('invalid old password', Code.Unauthenticated);
    const request = {
      header: new Headers(),
      method: { name: 'ChangePassword', parent: { typeName: 'auth.v1.AuthService' } },
    } as unknown as Parameters<Parameters<typeof sessionInterceptor>[0]>[0];

    await expect(sessionInterceptor(vi.fn().mockRejectedValue(error))(request)).rejects.toBe(error);

    expect(handler).not.toHaveBeenCalled();
  });

  it('still forwards a 401 from any other procedure to the session handler', async () => {
    const handler = vi.fn();
    setAuthErrorHandler(handler);
    const error = new ConnectError('token expired', Code.Unauthenticated);
    const request = {
      header: new Headers(),
      method: { name: 'GetOperation', parent: { typeName: 'orchestrator.v1.OrchestratorService' } },
    } as unknown as Parameters<Parameters<typeof sessionInterceptor>[0]>[0];

    await expect(sessionInterceptor(vi.fn().mockRejectedValue(error))(request)).rejects.toBe(error);

    expect(handler).toHaveBeenCalledWith(error);
  });

  /*
   * Transport-level guards: the shape-based cases above only prove the allowlist
   * matches a hand-built request. These go through the REAL client + transport, so
   * a renamed field or a different method object fails here (the independent
   * review of TASK-185 showed the shape-only tests would pass even without the fix).
   */
  it('keeps the session handler away from the real 401 of ChangePassword', async () => {
    const handler = vi.fn();
    setAuthErrorHandler(handler);
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ code: 'unauthenticated', message: 'invalid old password' }),
      { status: 401, headers: { 'Content-Type': 'application/json' } },
    )));

    await expect(changePassword('old', 'new')).rejects.toBeInstanceOf(ConnectError);

    expect(handler).not.toHaveBeenCalled();
  });

  it('still routes the real 401 of an ordinary RPC to the session handler', async () => {
    const handler = vi.fn();
    setAuthErrorHandler(handler);
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ code: 'unauthenticated', message: 'token expired' }),
      { status: 401, headers: { 'Content-Type': 'application/json' } },
    )));

    await expect(getOperation('op-1')).rejects.toBeInstanceOf(ConnectError);

    expect(handler).toHaveBeenCalledTimes(1);
  });

  // Pins the global policy that makes in-page 403 messages unreachable: the handler
  // navigates to /forbidden. Recorded because TASK-190's page test could otherwise be
  // read as a guarantee that a local refusal appears inline.
  it('routes a real 403 to the forbidden handler', async () => {
    const handler = vi.fn();
    setAuthErrorHandler(handler);
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ code: 'permission_denied', message: 'subject is not an active organization member' }),
      { status: 403, headers: { 'Content-Type': 'application/json' } },
    )));

    await expect(getOperation('op-1')).rejects.toBeInstanceOf(ConnectError);

    expect(handler).toHaveBeenCalledTimes(1);
  });

  // RunCleanup runs for up to ~an hour server-side; the 15s unary deadline would
  // abort a healthy collection, so the long budget must be keyed to the exact
  // procedure path the client actually calls.
  it('routes RunCleanup through the long unary budget', () => {
    expect(Object.keys(LONG_UNARY_PROCEDURES)).toContain('/orchestrator.v1.CleanupService/RunCleanup');
    expect(LONG_UNARY_PROCEDURES['/orchestrator.v1.CleanupService/RunCleanup']).toBeGreaterThan(UNARY_RPC_TIMEOUT_MS);
  });

  it('forwards authorization errors to the session handler', async () => {
    const handler = vi.fn();
    setAuthErrorHandler(handler);
    const error = new ConnectError('forbidden', Code.PermissionDenied);
    const request = { header: new Headers() } as Parameters<Parameters<typeof sessionInterceptor>[0]>[0];

    await expect(sessionInterceptor(vi.fn().mockRejectedValue(error))(request)).rejects.toBe(error);

    expect(handler).toHaveBeenCalledWith(error);
  });
});

/*
 * AC-178-06: without a deadline, a blackholed network kept the console blank
 * forever. The awaited fetch never settles, so the bootstrap promise never
 * rejects, and the boot-failure page (reached only from a rejection) never runs.
 */
describe('browser Connect deadlines', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function hangingFetch(): ReturnType<typeof vi.fn> {
    return vi.fn(
      (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => {
            reject(init.signal?.reason ?? new DOMException('aborted', 'AbortError'));
          });
        }),
    );
  }

  it('aborts a unary RPC that never answers', async () => {
    vi.useFakeTimers();
    const fetchMock = hangingFetch();
    vi.stubGlobal('fetch', fetchMock);

    const settled = browserFetch('https://api.test/auth.v1.AuthService/GetSession', { method: 'POST' }).then(
      () => 'resolved',
      (error: unknown) => (isTimeoutError(error) ? 'timed-out' : 'other'),
    );

    await vi.advanceTimersByTimeAsync(UNARY_RPC_TIMEOUT_MS + 1);

    await expect(settled).resolves.toBe('timed-out');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('leaves an open-ended stream alone', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(new Response('stream', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    const response = await browserFetch('/orchestrator.v1.OrchestratorService/WatchOperation', { method: 'POST' });
    expect(response.status).toBe(200);

    // Ten minutes is normal for a live operation timeline: it must not be cut off.
    await vi.advanceTimersByTimeAsync(600_000);
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined;
    // No deadline controller was installed: the stream keeps the caller's own
    // signal (here none), so nothing can abort it on a timer.
    expect(init?.signal).toBeUndefined();
  });

  it('matches the streaming exemption on the exact procedure path', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    // Same path plus a query string, and an absolute URL: both are the stream.
    await browserFetch('/orchestrator.v1.OrchestratorService/WatchOperation?x=1');
    await browserFetch('https://api.test/orchestrator.v1.OrchestratorService/WatchOperation');
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBeUndefined();
    expect(fetchMock.mock.calls[1]?.[1]?.signal).toBeUndefined();

    // A unary procedure whose NAME STARTS WITH the stream name must still get a
    // deadline — a substring match would silently exempt it.
    await browserFetch('https://api.test/orchestrator.v1.OrchestratorService/WatchOperationHistory');
    expect(fetchMock.mock.calls[2]?.[1]?.signal).toBeInstanceOf(AbortSignal);

    expect(procedurePath('/a/b?c=d')).toBe('/a/b');
    expect(procedurePath('https://api.test/a/b?c=d')).toBe('/a/b');
  });

  it('keeps caller-side cancellation distinct from a deadline', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('fetch', hangingFetch());

    const caller = new AbortController();
    const settled = browserFetch('/orchestrator.v1.OrchestratorService/GetOperation', {
      method: 'POST',
      signal: caller.signal,
    }).then(
      () => 'resolved',
      (error: unknown) => (isTimeoutError(error) ? 'timed-out' : 'canceled'),
    );

    caller.abort();
    await vi.advanceTimersByTimeAsync(1);

    await expect(settled).resolves.toBe('canceled');
  });

  it('recognises a deadline abort, including a wrapped cause, but not a caller abort', () => {
    expect(isTimeoutError(new DOMException('rpc timeout', 'TimeoutError'))).toBe(true);
    expect(isTimeoutError(new Error('inner', { cause: new DOMException('rpc timeout', 'TimeoutError') }))).toBe(true);
    expect(isTimeoutError(new DOMException('aborted', 'AbortError'))).toBe(false);
  });

  it('reports a stable DeadlineExceeded instead of an opaque abort', async () => {
    const next = vi.fn().mockRejectedValue(new DOMException('rpc timeout', 'TimeoutError'));
    const request = { header: new Headers() } as Parameters<Parameters<typeof sessionInterceptor>[0]>[0];

    const error = await sessionInterceptor(next)(request).then(
      () => null,
      (thrown: unknown) => thrown,
    );

    expect(error).toBeInstanceOf(ConnectError);
    expect((error as ConnectError).code).toBe(Code.DeadlineExceeded);
    expect((error as ConnectError).rawMessage).toContain('请求超时');
  });
});
