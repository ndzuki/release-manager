import type { ValuesStatus } from '@/gen/common/v1/domain_pb';

export type EditorLanguage = 'yaml' | 'json';
/**
 * `unknown` is a DISPLAY state for a value this console could not classify; it is
 * not a server state and must never be sent back as a filter. The wire filter is a
 * proto `ValuesStatus` and `UNSPECIFIED` means "no filter" to the server (see
 * listValuesRevisions), so a filter built from this union would silently return
 * every row. Nothing builds one today: every caller passes `ValuesStatus.APPROVED`
 * or no filter at all.
 */
export type RevisionStatus = 'draft' | 'pending_approval' | 'approved' | 'rejected' | 'superseded' | 'discarded' | 'unknown';

export interface SecretRef {
  path?: string;
  name: string;
  key: string;
  namespace?: string;
}

export interface SecretRefFormItem extends SecretRef {
  id: string;
  fieldLabel?: string;
}

export interface SecretOption {
  name: string;
  keys: string[];
}

export interface ValuesRevision {
  id: string;
  releaseDefinitionId: string;
  revision: number;
  stateVersion: string;
  document: string;
  valuesDigest: string;
  status: RevisionStatus;
  parentRevisionId: string | null;
  secretRefs: SecretRef[];
  createdByUserId: string;
  createdAt: string;
  submittedAt?: string;
  decidedAt?: string;
  /** Convergence bindings (REQ-079 D10): task operation ids + locked paths. */
  convergenceTaskIds: string[];
  lockedPaths: string[];
}

export interface DiffChange {
  path: string;
  kind: 'added' | 'removed' | 'modified' | 'array_change';
  oldValue?: unknown;
  newValue?: unknown;
}

export interface DiffResult {
  changes: DiffChange[];
  hasChanges: boolean;
}

export type ValidationCode = 'invalid_yaml' | 'secret_literal_forbidden' | 'size_exceeded';

export interface ValidationIssue {
  code: ValidationCode;
  message: string;
  line?: number;
  column?: number;
}

export interface CanonicalResult {
  value: unknown;
  document: string;
  issue: ValidationIssue | null;
}

export function statusFromProto(status: ValuesStatus): RevisionStatus {
  switch (status) {
    case 1:
      return 'draft';
    case 2:
      return 'approved';
    case 3:
      return 'rejected';
    case 4:
      return 'pending_approval';
    case 5:
      return 'superseded';
    case 6:
      return 'discarded';
    default:
      // `ValuesStatus` is an open proto3 enum, so a value this console does not know
      // (a newer server) and `UNSPECIFIED` (0) both land here. `UNSPECIFIED` is also
      // what the server's own mapper returns for a stored status it cannot read
      // (internal/orchestrator/values_approval.go valuesStatusToProto). Reading either
      // as DRAFT claimed a specific approval state nobody had established, and the
      // editor gates Submit/Discard on `status === 'draft'`, so the false draft was
      // an actionable one. Unknown is its own state now.
      return 'unknown';
  }
}
