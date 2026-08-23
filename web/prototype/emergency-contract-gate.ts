/**
 * TASK-058 Step 1 canonical contract gate fixture (throwaway, v3 · 2026-08-22).
 *
 * Verifies that the current generated schema/client can express REQ-058's
 * hard-blocking canonical contracts. PASS = this file compiles with zero
 * errors AND the old `EmergencyChange` procedure/caller scan is zero.
 * FAIL = any required symbol/field below is missing, or the old procedure
 * still exists, or a handwritten type/shim would be required.
 *
 * Run:  ./node_modules/.bin/tsc --noEmit --target ES2022 --module ESNext \
 *        --moduleResolution Bundler --skipLibCheck prototype/emergency-contract-gate.ts
 *
 * IMPORTANT: the `missing` section intentionally references symbols that the
 * approved plan (v3 Step 1) lists as HARD BLOCKING. Their absence is the FAIL
 * evidence. Do not "fix" the fixture by deleting them — re-run the gate after
 * upstream contracts land (D9 backend task); symbols move from `missing` to
 * `present` only then.
 *
 * v3 updates vs v1: added ListCandidateArtifactsRequest cascade-parameter
 * assertions (workload_ref/container/operationVersion) and EmergencyReasonCode
 * to the hard-blocking missing list, per approved plan v3 Step 1.
 */

// ─────────────────────────────────────────────────────────────────────────────
// 1. PRESENT symbols (expected to exist on current upstream/main@fe3f7ee)
// ─────────────────────────────────────────────────────────────────────────────
import {
  CheckEmergencyConflictRequestSchema,
  ListEmergencyTargetsRequestSchema,
  ListCandidateArtifactsRequestSchema,
  ListConvergenceTasksRequestSchema,
  CreatePrepareSessionRequestSchema,
  GetPrepareSessionRequestSchema,
  DiscardValuesRevisionRequestSchema,
  CreateValuesRevisionRequestSchema,
  SubmitValuesRevisionRequestSchema,
  ApproveValuesRevisionRequestSchema,
  RejectValuesRevisionRequestSchema,
  GetOperationRequestSchema,
  WatchOperationRequestSchema,
  CancelOperationRequestSchema,
  EmergencyResultSchema,
  ReleaseSummarySchema,
  ListReleasesRequestSchema,
  type EmergencyResult,
  type ReleaseSummary,
  type CreateValuesRevisionRequest,
  type ListCandidateArtifactsRequest,
} from '../src/gen/orchestrator/v1/orchestrator_pb';
import {
  GetAuthorizationSnapshotResponseSchema,
  type GetAuthorizationSnapshotResponse,
} from '../src/gen/auth/v1/auth_pb';
import {
  ValuesRevisionSchema,
  ValuesStatus,
  type ValuesRevision,
} from '../src/gen/common/v1/domain_pb';
import { EmergencyEffectStatusSchema } from '../src/gen/orchestrator/v1/orchestrator_pb';

// Present-symbol type assertions (compile-time; zero runtime cost).
const _checkConflictReq = CheckEmergencyConflictRequestSchema.typeName;
const _targetsReq = ListEmergencyTargetsRequestSchema.typeName;
const _artifactsReq = ListCandidateArtifactsRequestSchema.typeName;
const _convergenceTasksReq = ListConvergenceTasksRequestSchema.typeName;
const _createPrepare = CreatePrepareSessionRequestSchema.typeName; // naming divergence: main delivers CreatePrepareSession (REQ-058 expected PrepareConvergenceRevision)
const _getPrepare = GetPrepareSessionRequestSchema.typeName;
const _discard = DiscardValuesRevisionRequestSchema.typeName;
const _createValues = CreateValuesRevisionRequestSchema.typeName;
const _submitValues = SubmitValuesRevisionRequestSchema.typeName;
const _approveValues = ApproveValuesRevisionRequestSchema.typeName;
const _rejectValues = RejectValuesRevisionRequestSchema.typeName;
const _getOp = GetOperationRequestSchema.typeName;
const _watchOp = WatchOperationRequestSchema.typeName;
const _cancelOp = CancelOperationRequestSchema.typeName;
const _emergencyResult = EmergencyResultSchema.typeName;
const _releaseSummary = ReleaseSummarySchema.typeName;
const _listReleases = ListReleasesRequestSchema.typeName;
const _authSnapshot = GetAuthorizationSnapshotResponseSchema.typeName;
const _valuesRevision = ValuesRevisionSchema.typeName;
const _effectStatus = EmergencyEffectStatusSchema.typeName;

// Field-level assertions for PRESENT contracts.
function assertPresent(v: unknown): void {
  if (v === undefined) throw new Error('missing present contract field');
}

const authSnap: GetAuthorizationSnapshotResponse = {} as GetAuthorizationSnapshotResponse;
assertPresent(authSnap.canExecuteEmergency); // present
assertPresent(authSnap.canCreateValuesRevision); // present
assertPresent(authSnap.canApproveValuesRevision); // present
assertPresent(authSnap.fresh); // present
assertPresent(authSnap.checkpoint); // present
assertPresent(authSnap.sourceVersion); // present
assertPresent(authSnap.policyVersion); // present

const createValues: CreateValuesRevisionRequest = {} as CreateValuesRevisionRequest;
assertPresent(createValues.prepareToken); // present (CreateValuesRevisionRequest.prepare_token)

const releaseSummary: ReleaseSummary = {} as ReleaseSummary;
assertPresent(releaseSummary.releaseDefinitionId); // present

// ListCandidateArtifactsRequest cascade parameters (plan v3 Step 1):
// REQUIRED workload_ref/container/operationVersion — currently only
// organization_id + release_definition_id exist (fe3f7ee, field 1/2).
const candidateArtifactsReq: ListCandidateArtifactsRequest = {} as ListCandidateArtifactsRequest;
assertPresent(candidateArtifactsReq.organizationId); // present
assertPresent(candidateArtifactsReq.releaseDefinitionId); // present
const _missingArtifactWorkloadRef: unknown = candidateArtifactsReq.workloadRef; // MISSING: workload_ref cascade param
const _missingArtifactContainer: unknown = candidateArtifactsReq.container; // MISSING: container cascade param
const _missingArtifactOperationVersion: unknown = candidateArtifactsReq.operationVersion; // MISSING: operationVersion cascade param

const emergencyResult: EmergencyResult = {} as EmergencyResult;
assertPresent(emergencyResult.effectStatus); // present
assertPresent(emergencyResult.before); // present
assertPresent(emergencyResult.after); // present
assertPresent(emergencyResult.convergenceTasks); // present
assertPresent(emergencyResult.revertStatus); // present

const valuesRev: ValuesRevision = {} as ValuesRevision;
assertPresent(valuesRev.status); // present
if (ValuesStatus.DISCARDED !== 6) throw new Error('DISCARDED enum changed');

// ─────────────────────────────────────────────────────────────────────────────
// 2. MISSING symbols — HARD BLOCKING (expected to NOT exist on fe3f7ee)
//    Their absence below is the Step 1 Prototype Gate FAIL evidence.
// ─────────────────────────────────────────────────────────────────────────────
import {
  ExecuteEmergencyChangeRequestSchema,
  ExecuteEmergencyChangeResponseSchema,
  EmergencyErrorDetailSchema,
  EmergencyReasonCode,
  OperationVersionSchema,
} from '../src/gen/orchestrator/v1/orchestrator_pb';

const _executeEmergencyReq = ExecuteEmergencyChangeRequestSchema.typeName; // MISSING
const _executeEmergencyResp = ExecuteEmergencyChangeResponseSchema.typeName; // MISSING
const _emergencyErrorDetail = EmergencyErrorDetailSchema.typeName; // MISSING
const _emergencyReasonCode: unknown = EmergencyReasonCode; // MISSING
const _operationVersion = OperationVersionSchema.typeName; // MISSING

// 3. MISSING fields — HARD BLOCKING (compiled as type errors below).
const _missingRequested: unknown = emergencyResult.requested; // MISSING: EmergencyResult.requested
const _missingOperationVersion: unknown = releaseSummary.operationVersion; // MISSING: operationVersion on target/request scope
const _missingEmergencyConflict: unknown = releaseSummary.emergencyConflict; // MISSING: ReleaseSummary.emergencyConflict
const _missingPendingConvergence: unknown = releaseSummary.pendingConvergenceCount; // MISSING: ReleaseSummary.pendingConvergenceCount
const _missingRevertSummary: unknown = releaseSummary.revertStatusSummary; // MISSING: ReleaseSummary.revertStatusSummary
const _missingConvergenceTaskIds: unknown = valuesRev.convergenceTaskIds; // MISSING: ValuesRevision.convergenceTaskIds
const _missingLockedPaths: unknown = valuesRev.lockedPaths; // MISSING: ValuesRevision.lockedPaths
const _missingKillSwitch: unknown = authSnap.emergencyChangeEnabled; // MISSING: emergencyChangeEnabled kill switch

// Keep the module side-effect-free so tree-shaking/tsc semantics stay trivial.
void [
  _checkConflictReq, _targetsReq, _artifactsReq, _convergenceTasksReq,
  _createPrepare, _getPrepare, _discard, _createValues, _submitValues,
  _approveValues, _rejectValues, _getOp, _watchOp, _cancelOp,
  _emergencyResult, _releaseSummary, _listReleases, _authSnapshot,
  _valuesRevision, _effectStatus,
  _executeEmergencyReq, _executeEmergencyResp, _emergencyErrorDetail,
  _emergencyReasonCode, _operationVersion, _missingRequested,
  _missingOperationVersion, _missingEmergencyConflict, _missingPendingConvergence,
  _missingRevertSummary, _missingConvergenceTaskIds, _missingLockedPaths,
  _missingKillSwitch, _missingArtifactWorkloadRef, _missingArtifactContainer,
  _missingArtifactOperationVersion,
];
