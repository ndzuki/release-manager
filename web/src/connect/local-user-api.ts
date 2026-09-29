import { Code, ConnectError } from '@connectrpc/connect';
import { create } from '@bufbuild/protobuf';
import { CreateLocalUserRequestSchema, ListLocalUsersRequestSchema } from '@/gen/auth/v1/auth_pb';
import { t } from '@/i18n/messages';
import { describeError, copyForReason } from './error-copy';
import { authClient } from './client';

/*
 * Local accounts (REQ-025 / A4's second half: CreateLocalUser + ListLocalUsers).
 *
 * Contract notes from api/proto/auth/v1/auth.proto that the UI must respect:
 *  - the whole `auth` object is platform_admin-only (procedure_policy.go marks
 *    these procedures adminOnly: no non-wildcard role holds a policy row), so the
 *    page is a platform-admin surface and everything else must not see it;
 *  - CreateLocalUser accepts AT MOST ONE role, an empty value means viewer, and
 *    platform_admin is rejected (D-16) — so the console never offers it;
 *  - ListLocalUsers is cursor-paginated (page_size <= 100) and answers
 *    INVALID_ARGUMENT for a cursor it cannot decode: resume from the first page
 *    instead of retrying the same token;
 *  - a local user's status is active | pending | disabled: only active accounts can
 *    authenticate, a disabled account stops validating immediately;
 *  - CreateLocalUser is idempotent on the username (D-13): a repeated create returns
 *    the existing account (HTTP 200) rather than ALREADY_EXISTS, which is why the UI
 *    must not claim it created something — and why this module has no `duplicate`
 *    classification: the server cannot produce that code on these procedures.
 */
export type LocalUserRole = 'viewer' | 'deployer' | 'release_admin';

/** The roles the server accepts here (platform_admin is rejected by D-16). */
export const LOCAL_USER_ROLES: LocalUserRole[] = ['viewer', 'deployer', 'release_admin'];

export interface LocalUserView {
  id: string;
  username: string;
  roles: string[];
  orgId: string;
  status: string;
}

export interface LocalUserPage {
  users: LocalUserView[];
  nextCursor: string;
}

export async function listLocalUsers(cursor = '', pageSize = 50): Promise<LocalUserPage> {
  const response = await authClient.listLocalUsers(create(ListLocalUsersRequestSchema, { cursor, pageSize }));
  return {
    users: response.users.map((user) => ({
      id: user.id,
      username: user.username,
      roles: user.roles,
      orgId: user.orgId,
      status: user.status,
    })),
    nextCursor: response.nextCursor,
  };
}

export async function createLocalUser(input: {
  username: string;
  password: string;
  role: LocalUserRole;
}): Promise<LocalUserView> {
  const response = await authClient.createLocalUser(
    create(CreateLocalUserRequestSchema, {
      username: input.username,
      password: input.password,
      // The contract takes a repeated field but accepts at most one value.
      roles: [input.role],
    }),
  );
  const user = response.user;
  return {
    id: user?.id ?? '',
    username: user?.username ?? input.username,
    roles: user?.roles ?? [input.role],
    orgId: user?.orgId ?? '',
    status: user?.status ?? 'active',
  };
}

export type LocalUserFailureCode =
  | 'permission_denied'
  | 'session_expired'
  | 'invalid_input'
  | 'invalid_cursor'
  | 'org_disabled'
  | 'unavailable';

export interface LocalUserFailure {
  code: LocalUserFailureCode;
  message: string;
  /** Correlation data for the technical-details line (never payload data). */
  details: string;
  retryable: boolean;
}

const MESSAGES: Record<LocalUserFailureCode, string> = {
  permission_denied: t('localUser.error.permissionDenied'),
  session_expired: t('localUser.error.sessionExpired'),
  invalid_input: t('localUser.error.invalidInput'),
  invalid_cursor: t('localUser.error.invalidCursor'),
  org_disabled: t('localUser.error.orgDisabled'),
  unavailable: t('localUser.error.unavailable'),
};

/**
 * `context` matters for one case: an InvalidArgument while paging means the cursor is
 * gone (the console must reload page one), while the same code on a first page means the
 * request itself was wrong. The server sets X-Reason-Code: invalid_cursor for the former;
 * the context keeps the branch alive against a server that does not set it yet.
 */
export function mapLocalUserError(error: unknown, context: 'list' | 'loadMore' = 'list'): LocalUserFailure {
  const description = describeError(error);
  const connectError = ConnectError.from(error);
  // Same rule as the rest of the console: a stable server reason code wins, then the
  // Connect code classifies the failure. Free-text messages are never matched.
  const shared = copyForReason(description.reasonCode);

  let code: LocalUserFailureCode;
  if (connectError.code === Code.PermissionDenied) {
    code = 'permission_denied';
  } else if (connectError.code === Code.Unauthenticated) {
    // The repo convention for an expired session (see auth-api/stuck-lock-api): say so
    // instead of reporting an outage the user would retry forever.
    code = 'session_expired';
  } else if (connectError.code === Code.InvalidArgument) {
    code =
      description.reasonCode.includes('cursor') || context === 'loadMore' ? 'invalid_cursor' : 'invalid_input';
  } else if (connectError.code === Code.FailedPrecondition) {
    code = 'org_disabled';
  } else if (connectError.code === Code.Internal) {
    // An Internal answer is an outage here, NOT a password verdict: the page pre-checks
    // bcrypt's 72-byte limit before sending, so the remaining Internal causes are the
    // server's own failures (`list users: %w`, `create local user: %w`, ...). Reporting
    // it as a rejected password would hide an outage behind a user error and disable the
    // retry button — the mistake an independent review caught in the previous revision.
    // This branch is behaviourally the same as the fallback on purpose; what it adds is
    // the named code in the technical line above.
    code = 'unavailable';
  } else {
    code = 'unavailable';
  }

  return {
    code,
    message: shared ?? MESSAGES[code],
    // The Connect code is part of the technical line: it is how an operator tells an
    // outage (Internal) apart from a refusal once the copy has been centralised.
    details: [Code[connectError.code], description.reasonCode, description.requestId]
      .filter(Boolean)
      .join(' · '),
    retryable: code === 'unavailable' || description.retryable,
  };
}
