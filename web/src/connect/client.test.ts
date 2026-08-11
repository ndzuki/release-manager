import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { browserFetch, readCookie, sessionInterceptor, setAuthErrorHandler } from './client';

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

  it('forwards authorization errors to the session handler', async () => {
    const handler = vi.fn();
    setAuthErrorHandler(handler);
    const error = new ConnectError('forbidden', Code.PermissionDenied);
    const request = { header: new Headers() } as Parameters<Parameters<typeof sessionInterceptor>[0]>[0];

    await expect(sessionInterceptor(vi.fn().mockRejectedValue(error))(request)).rejects.toBe(error);

    expect(handler).toHaveBeenCalledWith(error);
  });
});
