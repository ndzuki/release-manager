import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { AuthService, OrganizationService } from '@/gen/auth/v1/auth_pb';
import { OrchestratorService } from '@/gen/orchestrator/v1/orchestrator_pb';

const csrfCookieName = 'rm_csrf';
const csrfHeaderName = 'X-CSRF-Token';

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

const sessionInterceptor: Interceptor = (next) => async (request) => {
  const csrfToken = readCookie(csrfCookieName);
  if (csrfToken) {
    request.header.set(csrfHeaderName, csrfToken);
  }

  try {
    return await next(request);
  } catch (error) {
    const connectError = ConnectError.from(error);
    if (connectError.code === Code.Unauthenticated || connectError.code === Code.PermissionDenied) {
      await authErrorHandler?.(connectError);
    }
    throw connectError;
  }
};

export const transport = createConnectTransport({
  baseUrl: import.meta.env.VITE_API_BASE ?? '',
  useBinaryFormat: true,
  fetch: (input, init) => fetch(input, { ...init, credentials: 'include' }),
  interceptors: [sessionInterceptor],
});

export const authClient = createClient(AuthService, transport);
export const organizationClient = createClient(OrganizationService, transport);
export const orchestratorClient = createClient(OrchestratorService, transport);

