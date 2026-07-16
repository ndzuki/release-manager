import { defineStore } from 'pinia';
import { ref, computed } from 'vue';

// ---------------------------------------------------------------------------
// Types — mirror auth.v1.AuthService proto messages.
// Replace with generated types when buf TypeScript codegen is wired.
// ---------------------------------------------------------------------------

interface LoginRequest {
  username: string;
  password: string;
}

interface LoginResponse {
  accessToken: string;
  refreshToken: string;
  expiresAt: string;
  tokenType: string;
}

interface RefreshTokenRequest {
  refreshToken: string;
}

interface RefreshTokenResponse {
  accessToken: string;
  refreshToken: string;
  expiresAt: string;
  tokenType: string;
}

// ---------------------------------------------------------------------------
// Connect RPC helper
//
// Makes a unary Connect RPC call against the Vite-dev-proxied backend.
// Connect unary POST path: /<fully-qualified-service>/<Method>
// Body: JSON (or binary when useBinaryFormat is set in transport).
// ---------------------------------------------------------------------------

async function connectRpc<I, O>(
  servicePath: string,
  method: string,
  input: I,
): Promise<O> {
  const token = localStorage.getItem('access_token');
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Connect-Protocol-Version': '1',
  };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const url = `/${servicePath}/${method}`;
  const resp = await fetch(url, {
    method: 'POST',
    headers,
    body: JSON.stringify(input),
  });

  if (!resp.ok) {
    // Try to parse a Connect error from the response body.
    const contentType = resp.headers.get('Content-Type') ?? '';
    if (contentType.includes('application/json')) {
      const err = await resp.json();
      throw new Error(err.message ?? err.code ?? `RPC error ${resp.status}`);
    }
    throw new Error(`RPC error ${resp.status}: ${resp.statusText}`);
  }

  return resp.json();
}

// ---------------------------------------------------------------------------
// Auth Store
// ---------------------------------------------------------------------------

interface AuthUser {
  userId: string;
  roles: string[];
  orgId: string;
}

export const useAuthStore = defineStore('auth', () => {
  const user = ref<AuthUser | null>(null);
  const returnUrl = ref<string | null>(null);

  const isAuthenticated = computed(() => user.value !== null);

  function persistTokens(access: string, refresh: string) {
    localStorage.setItem('access_token', access);
    localStorage.setItem('refresh_token', refresh);
  }

  function clearTokens() {
    localStorage.removeItem('access_token');
    localStorage.removeItem('refresh_token');
  }

  async function login(username: string, password: string): Promise<void> {
    const resp = await connectRpc<LoginRequest, LoginResponse>(
      'auth.v1.AuthService',
      'Login',
      { username, password },
    );
    persistTokens(resp.accessToken, resp.refreshToken);
    user.value = { userId: username, roles: [], orgId: '' };
  }

  async function logout(): Promise<void> {
    try {
      const refresh = localStorage.getItem('refresh_token');
      if (refresh) {
        await connectRpc('auth.v1.AuthService', 'Logout', {
          refreshToken: refresh,
        });
      }
    } finally {
      clearTokens();
      user.value = null;
    }
  }

  async function refreshAccessToken(): Promise<string | null> {
    const refresh = localStorage.getItem('refresh_token');
    if (!refresh) return null;
    try {
      const resp = await connectRpc<RefreshTokenRequest, RefreshTokenResponse>(
        'auth.v1.AuthService',
        'RefreshToken',
        { refreshToken: refresh },
      );
      persistTokens(resp.accessToken, resp.refreshToken);
      return resp.accessToken;
    } catch {
      clearTokens();
      user.value = null;
      return null;
    }
  }

  function setReturnUrl(url: string) {
    returnUrl.value = url;
  }

  function clearReturnUrl() {
    returnUrl.value = null;
  }

  return {
    user,
    returnUrl,
    isAuthenticated,
    login,
    logout,
    refreshAccessToken,
    setReturnUrl,
    clearReturnUrl,
  };
});
