import type { ValuesStatus } from '@/gen/common/v1/domain_pb';

export type EditorLanguage = 'yaml' | 'json';
export type RevisionStatus = 'draft' | 'pending_approval' | 'approved' | 'rejected' | 'superseded' | 'discarded';

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
      return 'draft';
  }
}
