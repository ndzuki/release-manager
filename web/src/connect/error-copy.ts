import { Code, ConnectError } from '@connectrpc/connect';
import { hasMessage, t, type MessageKey } from '@/i18n/messages';

/*
 * One place that turns any Connect failure into stable copy plus correlation data
 * (UX plan §11 W5: "错误文案稳定 + requestId").
 *
 * Carriers, in the order the repo already trusts (core/go/connect-rpc.md, and the
 * pattern in features/emergency/errors.ts):
 *   1. X-Reason-Code — the stable machine code 20+ procedures already set;
 *   2. X-Request-ID  — set on every response and on every Connect error by
 *      contractsinterceptor.NewRequestIDInterceptor (internal/contracts/errors.go
 *      RequestIDHeader = "X-Request-ID"), so an operator can hand it to the server
 *      logs;
 *   3. the Connect code — the last resort.
 * Free-text messages are never matched: that is how a wording change silently
 * reclassifies an error.
 */

export const REQUEST_ID_HEADER = 'X-Request-ID';
export const REASON_CODE_HEADER = 'X-Reason-Code';

export interface ErrorDescription {
  /** HTTP-ish Connect code, for the technical line. */
  code: Code;
  /** Stable server reason code when the server sent one. */
  reasonCode: string;
  /** Server-side correlation id when the server sent one. */
  requestId: string;
  /** True when the failure is worth retrying unchanged. */
  retryable: boolean;
  /**
   * Centralised copy for a known reason code, or null when this layer has nothing
   * stable to say and the caller should use its own (usually code-based) wording.
   */
  message: string | null;
}

const RETRYABLE_CODES = new Set<Code>([Code.Unavailable, Code.DeadlineExceeded, Code.Aborted, Code.ResourceExhausted]);

/** Reason-code keys are the catalog's `reason.*` entries. */
export function copyForReason(reasonCode: string): string | null {
  const key = `reason.${reasonCode}`;
  return hasMessage(key) ? t(key as MessageKey) : null;
}

export function describeError(error: unknown): ErrorDescription {
  const connectError = ConnectError.from(error);
  const reasonCode = connectError.metadata.get(REASON_CODE_HEADER) ?? '';
  const requestId = connectError.metadata.get(REQUEST_ID_HEADER) ?? '';

  return {
    code: connectError.code,
    reasonCode,
    requestId,
    retryable: RETRYABLE_CODES.has(connectError.code),
    message: reasonCode ? copyForReason(reasonCode) : null,
  };
}

/**
 * The single line shown under "technical details": everything an operator needs to
 * correlate a failure with the server logs, and nothing that leaks payload data.
 */
export function correlationLine(description: ErrorDescription, procedure?: string): string {
  const parts = [`code=${Code[description.code] ?? String(description.code)}`];
  if (procedure) parts.push(`procedure=${procedure}`);
  if (description.reasonCode) parts.push(`reason=${description.reasonCode}`);
  if (description.requestId) parts.push(`requestId=${description.requestId}`);
  return parts.join(' · ');
}
