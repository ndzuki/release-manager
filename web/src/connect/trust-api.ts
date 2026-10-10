import { Code, ConnectError } from '@connectrpc/connect';
import { correlationLine, describeError } from './error-copy';
import { create } from '@bufbuild/protobuf';
import { timestampDate, timestampFromDate, type Timestamp } from '@bufbuild/protobuf/wkt';
import {
  CreateTrustRootRequestSchema,
  EndGraceRequestSchema,
  GetTrustPolicyRequestSchema,
  RetireTrustRootRequestSchema,
  RevokeTrustRootRequestSchema,
  RotateTrustRootRequestSchema,
  TrustRootState,
  type TrustRoot,
} from '@/gen/trust/v1/trust_pb';
import { trustClient } from './client';

/*
 * Signature trust roots (REQ-012 / REQ-043, A5 of the UX plan's missing surfaces).
 *
 * The console had no trust surface at all: an operator could not see which signing
 * keys the environment trusts, nor retire or revoke one. The contract that shapes
 * this page:
 *  - GetTrustPolicy returns the WHOLE snapshot (every root ever created, including
 *    retired and revoked ones) with the policy version and revocation epoch;
 *  - an unknown or empty environment is NOT an error: it answers version 1, epoch 0
 *    and no roots, which reads as "nothing trusted yet";
 *  - writes are neither idempotent nor version-checked: they are ordered by the
 *    current root state, so a repeated call fails on STATE
 *    (`FAILED_PRECONDITION`) instead of replaying — the UI therefore gates every
 *    action on the state it last read, and refuses the ones that would remove the
 *    last live root (the server refuses those too).
 */

export const TRUST_ENVIRONMENTS = ['staging', 'production'] as const;
export type TrustEnvironment = (typeof TRUST_ENVIRONMENTS)[number];

export type TrustRootStateName = 'pending' | 'active' | 'grace' | 'retired' | 'revoked' | 'unspecified';

export interface TrustRootView {
  id: string;
  keyId: string;
  issuer: string;
  subjectPattern: string;
  publicKeyPem: string;
  state: TrustRootStateName;
  validFrom: string | null;
  graceUntil: string | null;
  createdAt: string | null;
  updatedAt: string | null;
  revokedAt: string | null;
}

export interface TrustPolicyView {
  environment: string;
  version: bigint;
  revocationEpoch: bigint;
  roots: TrustRootView[];
}

export type TrustAction = 'end_grace' | 'retire' | 'revoke';

const STATE_NAMES: Record<number, TrustRootStateName> = {
  [TrustRootState.PENDING]: 'pending',
  [TrustRootState.ACTIVE]: 'active',
  [TrustRootState.GRACE]: 'grace',
  [TrustRootState.RETIRED]: 'retired',
  [TrustRootState.REVOKED]: 'revoked',
};

function toStateName(state: TrustRootState): TrustRootStateName {
  return STATE_NAMES[state] ?? 'unspecified';
}

function toIso(value: Timestamp | undefined): string | null {
  return value ? timestampDate(value).toISOString() : null;
}

function toView(root: TrustRoot): TrustRootView {
  return {
    id: root.id,
    keyId: root.keyId,
    issuer: root.issuer,
    subjectPattern: root.subjectPattern,
    publicKeyPem: root.publicKeyPem,
    state: toStateName(root.state),
    validFrom: toIso(root.validFrom),
    graceUntil: toIso(root.graceUntil),
    createdAt: toIso(root.createdAt),
    updatedAt: toIso(root.updatedAt),
    revokedAt: toIso(root.revokedAt),
  };
}

export async function getTrustPolicy(environment: string): Promise<TrustPolicyView> {
  const response = await trustClient.getTrustPolicy(create(GetTrustPolicyRequestSchema, { environment }));
  const policy = response.policy;
  return {
    environment: policy?.environment ?? environment,
    version: policy?.version ?? 1n,
    revocationEpoch: policy?.revocationEpoch ?? 0n,
    roots: (policy?.roots ?? []).map(toView),
  };
}

/*
 * Write surface (TASK-280 D11): create and rotate were the only two TrustService
 * RPCs with no console code path at all.
 *
 * MATERIAL REQUIREMENT — read from the contract, not guessed:
 *  - CreateTrustRootRequest.public_key_pem (api/proto/trust/v1/trust.proto:78) and
 *    RotateTrustRootRequest.public_key_pem (trust.proto:94) both carry PUBLIC key
 *    material only;
 *  - internal/trust/service.go:392-408 (toDomainRootFromRotate) feeds that field to
 *    Root.Validate, which runs ParsePublicKey (internal/trust/types.go:72);
 *  - ParsePublicKey rejects a PEM block whose type contains "PRIVATE"
 *    (internal/trust/types.go:112-114).
 * So rotation DOES need new key material, but only the public half: no private key
 * ever has to enter the browser or the wire. The console therefore does not
 * generate key pairs (that would put a private key in a non-encrypted path) — the
 * operator pastes the public PEM produced by the signer's KMS/CI, and the checks
 * below refuse anything that looks private before it is sent.
 */
export interface TrustRootInput {
  environment: string;
  keyId: string;
  publicKeyPem: string;
  issuer: string;
  subjectPattern: string;
  operator: string;
  /** Omitted ⇒ the server uses its own now (internal/trust/service.go:384-388). */
  validFrom?: Date;
}

export interface RotateTrustRootInput extends TrustRootInput {
  oldRootId: string;
  /**
   * End of the old root's grace window. Omitting it expires the old root
   * immediately — a hard cutover (trust.proto:174-176) — so the form always
   * decides this explicitly.
   */
  graceUntil?: Date;
}

/** Failure shapes the UI must distinguish before it ever reaches the wire. */
export type PublicKeyPemIssue = 'empty' | 'private_key' | 'not_public_key';

/**
 * Shape check only: the server is the authority on whether the PEM parses as an
 * Ed25519 public key (internal/trust/types.go:116-127). The point of this helper is
 * the one mistake that must not leave the browser — pasting a PRIVATE key — plus a
 * cheap guard against sending obviously wrong text.
 */
export function inspectPublicKeyPem(publicKeyPem: string): PublicKeyPemIssue | null {
  const trimmed = publicKeyPem.trim();
  if (trimmed === '') return 'empty';
  if (/PRIVATE KEY/.test(trimmed)) return 'private_key';
  if (!/-----BEGIN [A-Z ]*PUBLIC KEY-----/.test(trimmed) || !/-----END [A-Z ]*PUBLIC KEY-----/.test(trimmed)) {
    return 'not_public_key';
  }
  return null;
}

/** Refuses private material before it can be serialized into a request body. */
function assertPublicKeyMaterial(publicKeyPem: string): void {
  const issue = inspectPublicKeyPem(publicKeyPem);
  if (issue === null) return;
  throw new ConnectError(
    issue === 'private_key'
      ? 'refusing to send private key material'
      : 'public_key_pem must be one PEM public key block',
    Code.InvalidArgument,
  );
}

/** Registers a NEW root and activates it immediately (not idempotent). */
export async function createTrustRoot(input: TrustRootInput): Promise<TrustRootView> {
  assertPublicKeyMaterial(input.publicKeyPem);
  const response = await trustClient.createTrustRoot(create(CreateTrustRootRequestSchema, {
    environment: input.environment,
    keyId: input.keyId,
    publicKeyPem: input.publicKeyPem,
    issuer: input.issuer,
    subjectPattern: input.subjectPattern,
    operator: input.operator,
    validFrom: input.validFrom ? timestampFromDate(input.validFrom) : undefined,
  }));
  if (!response.root) throw new ConnectError('create trust root response is empty', Code.Internal);
  return toView(response.root);
}

/**
 * Introduces the replacement root and moves the old one into its grace window in one
 * step; the old root must be ACTIVE or the server answers FAILED_PRECONDITION.
 */
export async function rotateTrustRoot(
  input: RotateTrustRootInput,
): Promise<{ oldRoot: TrustRootView | null; newRoot: TrustRootView }> {
  assertPublicKeyMaterial(input.publicKeyPem);
  const response = await trustClient.rotateTrustRoot(create(RotateTrustRootRequestSchema, {
    environment: input.environment,
    oldRootId: input.oldRootId,
    keyId: input.keyId,
    publicKeyPem: input.publicKeyPem,
    issuer: input.issuer,
    subjectPattern: input.subjectPattern,
    operator: input.operator,
    validFrom: input.validFrom ? timestampFromDate(input.validFrom) : undefined,
    graceUntil: input.graceUntil ? timestampFromDate(input.graceUntil) : undefined,
  }));
  if (!response.newRoot) throw new ConnectError('rotate trust root response is empty', Code.Internal);
  return {
    oldRoot: response.oldRoot ? toView(response.oldRoot) : null,
    newRoot: toView(response.newRoot),
  };
}

/*
 * Which lifecycle actions the server will accept for a root state (mirrors the RPC
 * comments): ACTIVE can be retired or revoked, GRACE can end its window, be retired
 * or revoked, and PENDING/RETIRED/REVOKED accept nothing. Rotation is a write of its
 * own (rotateTrustRoot above) rather than a state-machine row action, because it
 * carries a new public key.
 */
export function allowedActions(state: TrustRootStateName): TrustAction[] {
  if (state === 'active') return ['retire', 'revoke'];
  if (state === 'grace') return ['end_grace', 'retire', 'revoke'];
  return [];
}

/**
 * The states the server counts as live when it refuses to remove the last root:
 * ONLY active and grace (internal/store/store.go "trust root not in active or grace
 * state"). PENDING must not be counted — it never reaches the store, and counting it
 * would LOOSEN the guard on the real last live root.
 */
export function isLive(state: TrustRootStateName): boolean {
  return state === 'active' || state === 'grace';
}

export async function endGrace(environment: string, rootId: string, operator: string): Promise<void> {
  await trustClient.endGrace(create(EndGraceRequestSchema, { environment, rootId, operator }));
}

export async function retireTrustRoot(environment: string, rootId: string, operator: string): Promise<void> {
  await trustClient.retireTrustRoot(create(RetireTrustRootRequestSchema, { environment, rootId, operator }));
}

export async function revokeTrustRoot(environment: string, rootId: string, operator: string): Promise<void> {
  await trustClient.revokeTrustRoot(create(RevokeTrustRootRequestSchema, { environment, rootId, operator }));
}

export type TrustFailureCode =
  | 'state_conflict'
  | 'last_live_root'
  | 'overlap_conflict'
  | 'not_found'
  | 'permission_denied'
  | 'invalid_input'
  | 'unavailable';

export interface TrustFailure {
  /** Stable correlation data for the technical-details line (never payload data). */
  details: string;
  code: TrustFailureCode;
  message: string;
  /** True when the reason came from the server's stable machine token. */
  typed: boolean;
}

const MESSAGES: Record<TrustFailureCode, string> = {
  state_conflict: '该信任根的当前状态不允许这个操作；已为你刷新最新策略，请按当前状态重试',
  last_live_root: '这是该环境最后一个可用信任根，不能退休或吊销（先轮换出替代根）',
  overlap_conflict: '新密钥与某个仍生效的信任根重叠，请换一把密钥',
  not_found: '未找到该信任根或环境',
  permission_denied: '无权修改信任根：需要 trust_root/write（通常是 platform_admin）',
  invalid_input: '输入不合法：请检查密钥材料与有效期',
  unavailable: '信任根操作失败，请稍后重试',
};

/*
 * The server reports its refusal causes as a stable machine token
 * (`internal/trust/types.go`: overlap_conflict, last_root_removal_forbidden) or as a
 * state precondition. We match the LEADING token, never a free-text substring, which
 * is the convention used by features/emergency/errors.ts and connect/operation-api.ts.
 */
export function mapTrustError(error: unknown): TrustFailure {
  // The shared layer owns the copy for stable reason codes; the local function
  // keeps this feature's code classification (which drives reload/retry).
  return withSharedCopy(mapTrustErrorLocal(error), error);
}

function mapTrustErrorLocal(error: unknown): TrustFailure {
  const connectError = ConnectError.from(error);
  const raw = (connectError.rawMessage ?? connectError.message ?? '').trim();
  const token = raw.split(':', 1)[0]!.trim().toLowerCase();
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  const pick = (code: TrustFailureCode, typed = false): TrustFailure => ({
    code,
    message: MESSAGES[code],
    typed,
    details: '',
  });

  // TrustService sets no X-Reason-Code today (only a few auth/orchestrator
  // procedures do), so the stable machine token above is the only carrier for these
  // two refusals; the metadata check stays as cheap defence for the day it appears.
  if (reason.includes('overlap') || token === 'overlap_conflict') return pick('overlap_conflict', true);
  if (reason.includes('last_root') || token === 'last_root_removal_forbidden') return pick('last_live_root', true);
  if (connectError.code === Code.FailedPrecondition) return pick('state_conflict');
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.InvalidArgument) return pick('invalid_input');
  return pick('unavailable');
}

/** Centralised copy for a known reason code overrides the local fallback wording. */
function withSharedCopy(failure: TrustFailure, error: unknown): TrustFailure {
  const described = describeError(error);
  const shared = described.message;
  return {
    ...failure,
    message: shared ?? failure.message,
    details: correlationLine(described),
  };
}
