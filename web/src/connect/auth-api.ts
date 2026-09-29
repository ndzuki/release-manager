import { Code, ConnectError } from '@connectrpc/connect';
import { authClient } from './client';

/**
 * Password change (REQ-025 / A4 of the UX plan's missing surfaces).
 *
 * Two server behaviours the console must not get wrong:
 *  - a wrong old password answers UNAUTHENTICATED with `invalid old password`,
 *    which is NOT the same as an expired session;
 *  - on success the server revokes every session of that user (AC-025-03) and
 *    fails closed if revocation fails, so the console is logged out afterwards
 *    whether the caller expects it or not.
 */
export interface ChangePasswordFailure {
  code: 'invalid_old_password' | 'session_expired' | 'unavailable';
  message: string;
}

/**
 * bcrypt refuses inputs longer than 72 BYTES (golang.org/x/crypto/bcrypt
 * ErrPasswordTooLong), and the server surfaces that as INTERNAL — an opaque
 * "修改失败" for the user. This is a protocol limit, not a strength policy, so the
 * console states it explicitly before the request goes out.
 */
export const PASSWORD_MAX_BYTES = 72;

export function passwordTooLong(password: string): boolean {
  return new TextEncoder().encode(password).length > PASSWORD_MAX_BYTES;
}

export async function changePassword(oldPassword: string, newPassword: string): Promise<void> {
  await authClient.changePassword({ oldPassword, newPassword });
}

export function mapChangePasswordError(error: unknown): ChangePasswordFailure {
  const connectError = ConnectError.from(error);
  const raw = (connectError.rawMessage ?? connectError.message ?? '').trim().toLowerCase();

  if (connectError.code === Code.Unauthenticated) {
    // REQ-025 documents this exact message as the contract string for a wrong old
    // password; the server sets no reason code on this path, so match it EXACTLY
    // rather than as a substring (a rewording must fail loudly, not silently).
    if (raw === 'invalid old password') {
      return { code: 'invalid_old_password', message: '旧密码不正确' };
    }
    return { code: 'session_expired', message: '会话已失效，请重新登录后再修改密码' };
  }
  return { code: 'unavailable', message: '修改失败，请稍后重试' };
}
