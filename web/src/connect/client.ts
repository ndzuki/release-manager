/**
 * Connect transport — configured with Vite dev proxy.
 *
 * All RPC calls go through this transport. Auth token is injected
 * from localStorage.
 *
 * When buf TypeScript codegen is wired up, swap manual fetch calls
 * in stores/services to typed PromiseClient instances.
 */
import { createConnectTransport } from '@connectrpc/connect-web';
import type { Transport } from '@connectrpc/connect';

// Re-export for consumers
export { createClient } from '@connectrpc/connect';
export type { Transport } from '@connectrpc/connect';

export const transport: Transport = createConnectTransport({
  baseUrl: import.meta.env.VITE_API_BASE ?? '',
  useBinaryFormat: true,
  fetch: (input, init) => {
    const token = localStorage.getItem('access_token');
    if (token) {
      const headers = new Headers(init?.headers);
      headers.set('Authorization', `Bearer ${token}`);
      return fetch(input, { ...init, headers });
    }
    return fetch(input, init);
  },
});
