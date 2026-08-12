import { defineStore } from 'pinia';
import { computed, shallowRef } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import { authClient, setAuthErrorHandler } from '@/connect/client';
import type { Organization, SessionUser } from '@/gen/auth/v1/auth_pb';

export type AuthStatus = 'idle' | 'initializing' | 'anonymous' | 'authenticated' | 'expired';

interface SessionPayload {
  user?: SessionUser;
  organizations: Organization[];
  expiresAt: bigint;
}

export const organizationChangedEvent = 'release-manager:organization-changed';

let forbiddenNavigator: ((message: string) => Promise<void>) | undefined;

export function setForbiddenNavigator(navigator: ((message: string) => Promise<void>) | undefined): void {
  forbiddenNavigator = navigator;
}

export const useAuthStore = defineStore('auth', () => {
  const status = shallowRef<AuthStatus>('idle');
  const initialized = shallowRef<boolean | null>(null);
  const user = shallowRef<SessionUser | null>(null);
  const organizations = shallowRef<Organization[]>([]);
  const expiresAt = shallowRef<number | null>(null);
  const returnUrl = shallowRef<string | null>(null);
  const forbiddenMessage = shallowRef<string | null>(null);

  const isAuthenticated = computed(() => status.value === 'authenticated' && user.value !== null);
  const activeOrganization = computed(
    () => organizations.value.find((organization) => organization.id === user.value?.activeOrgId) ?? null,
  );
  const canWrite = computed(() => !user.value?.roles.some((role) => role.toLowerCase() === 'viewer'));
	const canCreateReleaseOperation = computed(
		() => user.value?.roles.some((role) => ['platform_admin', 'release_admin'].includes(role.toLowerCase())) === true,
	);

  function applySession(payload: SessionPayload): void {
    if (!payload.user) {
      clearSession('anonymous');
      return;
    }
    user.value = payload.user;
    organizations.value = [...payload.organizations];
    expiresAt.value = Number(payload.expiresAt);
    status.value = 'authenticated';
    forbiddenMessage.value = null;
  }

  function clearSession(nextStatus: AuthStatus = 'anonymous'): void {
    user.value = null;
    organizations.value = [];
    expiresAt.value = null;
    status.value = nextStatus;
  }

  async function initialize(): Promise<void> {
    if (status.value !== 'idle') return;
    status.value = 'initializing';

    const initStatus = await authClient.getInitStatus({});
    initialized.value = initStatus.initialized;
    if (!initStatus.initialized) {
      clearSession('anonymous');
      return;
    }

    try {
      const session = await authClient.validateToken({});
      applySession(session);
    } catch (error) {
      if (ConnectError.from(error).code !== Code.Unauthenticated) throw error;
      try {
        const refreshed = await authClient.refreshToken({});
        applySession(refreshed);
      } catch (refreshError) {
        if (ConnectError.from(refreshError).code !== Code.Unauthenticated) throw refreshError;
        clearSession('anonymous');
      }
    }
  }

  async function initializeSystem(
    username: string,
    password: string,
    organizationName: string,
  ): Promise<void> {
    const session = await authClient.initialize({ username, password, organizationName });
    initialized.value = true;
    applySession(session);
  }

  async function login(username: string, password: string): Promise<void> {
    const session = await authClient.login({ username, password });
    applySession(session);
  }

  async function logout(): Promise<void> {
    try {
      await authClient.logout({});
    } finally {
      clearSession('anonymous');
    }
  }

  async function refreshSession(): Promise<boolean> {
    try {
      const session = await authClient.refreshToken({});
      applySession(session);
      return true;
    } catch (error) {
      if (ConnectError.from(error).code !== Code.Unauthenticated) throw error;
      clearSession('expired');
      return false;
    }
  }

  async function switchOrganization(organizationId: string): Promise<void> {
    const session = await authClient.switchOrganization({ orgId: organizationId });
    applySession(session);
    globalThis.dispatchEvent?.(new CustomEvent(organizationChangedEvent, { detail: { organizationId } }));
  }

  async function handleAuthError(error: ConnectError): Promise<void> {
    if (error.code === Code.Unauthenticated) {
      clearSession(status.value === 'authenticated' ? 'expired' : 'anonymous');
      return;
    }
    if (error.code === Code.PermissionDenied) {
      const message = error.rawMessage || 'You do not have permission to perform this action.';
      forbiddenMessage.value = message;
      await forbiddenNavigator?.(message);
    }
  }

  async function handleConnectError(error: ConnectError): Promise<void> {
    await handleAuthError(error);
  }

  function setReturnUrl(url: string): void {
    returnUrl.value = url;
  }

  function clearReturnUrl(): void {
    returnUrl.value = null;
  }

  function consumeForbiddenMessage(): string | null {
    const message = forbiddenMessage.value;
    forbiddenMessage.value = null;
    return message;
  }

  setAuthErrorHandler(handleAuthError);

  return {
    status,
    initialized,
    user,
    organizations,
    expiresAt,
    returnUrl,
    forbiddenMessage,
    isAuthenticated,
    activeOrganization,
    canWrite,
		canCreateReleaseOperation,
    initialize,
    initializeSystem,
    login,
    logout,
    refreshSession,
    switchOrganization,
    clearSession,
    setReturnUrl,
    clearReturnUrl,
    consumeForbiddenMessage,
    handleConnectError,
  };
});
