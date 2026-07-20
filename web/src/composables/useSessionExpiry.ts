import { onScopeDispose, watch } from 'vue';
import type { Ref } from 'vue';

type SessionExpiryTimer = number | undefined;

interface SessionExpiryOptions {
  expiresAt: Ref<number | null>;
  onExpired: () => void;
}

export function useSessionExpiry(options: SessionExpiryOptions): void {
  let timer: SessionExpiryTimer;

  watch(
    options.expiresAt,
    (expiresAt) => {
      clearTimeout(timer);
      if (!expiresAt) return;

      const delay = Math.max(0, expiresAt * 1_000 - Date.now());
      timer = window.setTimeout(options.onExpired, delay);
    },
    { immediate: true },
  );

  onScopeDispose(() => clearTimeout(timer));
}
