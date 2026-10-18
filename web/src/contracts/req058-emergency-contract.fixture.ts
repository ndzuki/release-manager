/**
 * AC-058-50 canonical-contract compile fixture (W9, 2026-10-18).
 *
 * REQ-058 is written against the canonical REQ-079 emergency surface: one typed
 * `ExecuteEmergencyChange` procedure whose request carries the whole intent as named
 * fields. The AC asks for a *fixture* that fails the moment a required generated
 * symbol disappears (or the legacy `EmergencyChange(payload, actor)` shape returns),
 * so "the contract still lines up" is an artifact a gate can run rather than a
 * property of whatever the pages happen to import this week.
 *
 * How it fails, and how loudly:
 *
 *   - a required export is gone  -> TS2344 names the missing symbol, e.g.
 *     `Type '"ExecuteEmergencyChangeRequestSchema"' does not satisfy the constraint 'never'`
 *   - a required field is gone   -> TS2344 names the field
 *     (`... does not satisfy the constraint 'keyof ExecuteEmergencyChangeRequest'`)
 *   - a legacy symbol returns    -> TS2344 names it in the ABSENT assertion below
 *
 * Every assertion is a `declare`-only declaration: the file emits no JavaScript, so it
 * is a pure compile-time artifact (it never runs, and nothing imports it at runtime).
 * It is compiled by the ordinary project (`tsconfig.json` includes `src/**`), i.e. by
 * `npm run build` / `make web-check`, and also by the focused `npm run check:contract`
 * script, which type-checks only this file plus the generated code it references.
 *
 * This supersedes `web/prototype/emergency-contract-gate.ts` (the throwaway 2026-08-22
 * prototype). That file sat outside the tsconfig `include`, so its "MISSING" section —
 * stale since the canonical contract landed — was never compiled and could not fail
 * anything. Do NOT move this fixture out of `src/`.
 */
import type { GenService } from '@bufbuild/protobuf/codegenv2';
import * as authPb from '@/gen/auth/v1/auth_pb';
import * as orchestratorPb from '@/gen/orchestrator/v1/orchestrator_pb';
import type {
  CheckEmergencyConflictRequest,
  EmergencyErrorDetail,
  EmergencyResult,
  EmergencyTarget,
  EmergencyTypedValues,
  ExecuteEmergencyChangeRequest,
  ExecuteEmergencyChangeResponse,
  ListCandidateArtifactsRequest,
  OperationVersion,
  ReleaseEmergencyLockRequest,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import type { GetAuthorizationSnapshotResponse } from '@/gen/auth/v1/auth_pb';

/** `T` must be `never`; a non-empty union is reported AT THIS TYPE ARGUMENT. */
type AssertNever<T extends never> = T;
/** Forces `K` to be a real field of `T`; the error names the field. */
type RequireField<T, K extends keyof T> = T[K];

// ─────────────────────────────────────────────────────────────────────────────
// 1. Required generated exports (the canonical surface the console consumes).
// ─────────────────────────────────────────────────────────────────────────────
type RequiredOrchestratorExports =
  // Execute / conflict / target discovery.
  | 'ExecuteEmergencyChangeRequestSchema'
  | 'ExecuteEmergencyChangeResponseSchema'
  | 'CheckEmergencyConflictRequestSchema'
  | 'CheckEmergencyConflictResponseSchema'
  | 'ListEmergencyTargetsRequestSchema'
  | 'ListEmergencyTargetsResponseSchema'
  | 'ListCandidateArtifactsRequestSchema'
  | 'ListCandidateArtifactsResponseSchema'
  // Projections.
  | 'EmergencyResultSchema'
  | 'EmergencyTypedValuesSchema'
  | 'EmergencyErrorDetailSchema'
  | 'EmergencyTargetSchema'
  | 'EmergencyActionSchema'
  | 'EmergencyConvergenceSchema'
  | 'EmergencyReasonCodeSchema'
  | 'EmergencyEffectStatusSchema'
  | 'ConvergenceStrategySchema'
  // REQ-087 audited lock release rides the same surface.
  | 'ReleaseEmergencyLockRequestSchema'
  | 'ReleaseEmergencyLockResponseSchema'
  // REQ-079 D4: a message, not a scalar, precisely so this fixture can name it.
  | 'OperationVersionSchema'
  // The procedure itself, not just its payload messages.
  | 'OrchestratorService';

type RequiredAuthExports = 'GetAuthorizationSnapshotResponseSchema';

/** Named failure: the union below is empty only while every export is present. */
export declare const requiredOrchestratorExportsPresent: AssertNever<
  Exclude<RequiredOrchestratorExports, keyof typeof orchestratorPb>
>;
export declare const requiredAuthExportsPresent: AssertNever<
  Exclude<RequiredAuthExports, keyof typeof authPb>
>;

// The RPC must stay reachable through the generated client, with the canonical types.
export declare const executeEmergencyChangeRpc: RequireField<
  typeof orchestratorPb.OrchestratorService.method,
  'executeEmergencyChange'
>;

// ─────────────────────────────────────────────────────────────────────────────
// 2. Request shape: the canonical fields exist, and the legacy two-field shape
//    (`EmergencyChange(payload, actor)`) does NOT come back.
// ─────────────────────────────────────────────────────────────────────────────
export declare const requestReleaseDefinitionId: RequireField<ExecuteEmergencyChangeRequest, 'releaseDefinitionId'>;
export declare const requestWorkloadRef: RequireField<ExecuteEmergencyChangeRequest, 'workloadRef'>;
export declare const requestContainer: RequireField<ExecuteEmergencyChangeRequest, 'container'>;
export declare const requestOperationVersion: RequireField<ExecuteEmergencyChangeRequest, 'operationVersion'>;
export declare const requestArtifactRef: RequireField<ExecuteEmergencyChangeRequest, 'artifactRef'>;
export declare const requestConvergenceStrategy: RequireField<ExecuteEmergencyChangeRequest, 'convergenceStrategy'>;
export declare const requestTargetLocks: RequireField<ExecuteEmergencyChangeRequest, 'targetLocks'>;
export declare const requestIdempotencyKey: RequireField<ExecuteEmergencyChangeRequest, 'idempotencyKey'>;
export declare const requestHash: RequireField<ExecuteEmergencyChangeRequest, 'requestHash'>;
export declare const requestSetReplicas: RequireField<ExecuteEmergencyChangeRequest, 'setReplicas'>;
export declare const requestAnnotations: RequireField<ExecuteEmergencyChangeRequest, 'annotations'>;
export declare const requestAnnotationScope: RequireField<ExecuteEmergencyChangeRequest, 'annotationScope'>;

/**
 * The legacy call surface: a single opaque `payload` plus an `actor` string. Its
 * reappearance on the canonical request — or as a generated `EmergencyChange`
 * message/procedure — is exactly what this fixture must refuse.
 */
type LegacyRequestFields = 'payload' | 'actor';
export declare const legacyRequestFieldsAbsent: AssertNever<
  Extract<LegacyRequestFields, keyof ExecuteEmergencyChangeRequest>
>;
type LegacyGeneratedSymbols = 'EmergencyChange' | 'EmergencyChangeRequest' | 'EmergencyChangeResponse';
export declare const legacyEmergencyChangeSymbolsAbsent: AssertNever<
  Extract<LegacyGeneratedSymbols, keyof typeof orchestratorPb>
>;

// ─────────────────────────────────────────────────────────────────────────────
// 3. Response + projection shapes the console renders (REQ-058 AC-058-20~33).
// ─────────────────────────────────────────────────────────────────────────────
export declare const responseResult: RequireField<ExecuteEmergencyChangeResponse, 'result'>;
export declare const responseOperationId: RequireField<ExecuteEmergencyChangeResponse, 'operationId'>;

export declare const resultEffectStatus: RequireField<EmergencyResult, 'effectStatus'>;
export declare const resultRequested: RequireField<EmergencyResult, 'requested'>;
export declare const resultBefore: RequireField<EmergencyResult, 'before'>;
export declare const resultAfter: RequireField<EmergencyResult, 'after'>;
export declare const resultConvergenceTasks: RequireField<EmergencyResult, 'convergenceTasks'>;
export declare const resultRevertStatus: RequireField<EmergencyResult, 'revertStatus'>;

export declare const typedValuesOneof: RequireField<EmergencyTypedValues, 'values'>;
export declare const errorDetailReasonCode: RequireField<EmergencyErrorDetail, 'reasonCode'>;
export declare const errorDetailRetryable: RequireField<EmergencyErrorDetail, 'retryable'>;
export declare const targetWorkloadRef: RequireField<EmergencyTarget, 'workloadRef'>;
export declare const candidateArtifactWorkloadRef: RequireField<ListCandidateArtifactsRequest, 'workloadRef'>;
export declare const conflictRequest: RequireField<CheckEmergencyConflictRequest, 'releaseDefinitionId'>;
export declare const lockReleaseRequest: RequireField<ReleaseEmergencyLockRequest, 'intentId'>;
export declare const lockReleaseMode: RequireField<ReleaseEmergencyLockRequest, 'mode'>;
export declare const operationVersionField: RequireField<OperationVersion, 'version'>;

// ─────────────────────────────────────────────────────────────────────────────
// 4. Authorization projection: the two emergency capabilities and the freshness
//    fields the console gates on (REQ-058 AC-058-03 / ADR-029).
// ─────────────────────────────────────────────────────────────────────────────
export declare const authorizationCanExecuteEmergency: RequireField<
  GetAuthorizationSnapshotResponse,
  'canExecuteEmergency'
>;
export declare const authorizationCanResolveEmergency: RequireField<
  GetAuthorizationSnapshotResponse,
  'canResolveEmergency'
>;
export declare const authorizationFresh: RequireField<GetAuthorizationSnapshotResponse, 'fresh'>;
export declare const authorizationCheckpoint: RequireField<GetAuthorizationSnapshotResponse, 'checkpoint'>;

// Documents that the generated service type is the one this fixture pins (keeps the
// `GenService` import load-bearing instead of an unused-import lint error).
export type PinnedOrchestratorService = GenService<typeof orchestratorPb.OrchestratorService.method>;
