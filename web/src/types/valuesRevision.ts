import type { ValuesStatus } from '@/gen/common/v1/domain_pb';

export type EditorLanguage = 'yaml' | 'json';
export type RevisionStatus = 'draft' | 'approved' | 'rejected' | 'superseded';

export interface SecretRef {
  path: string;
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
  version: number;
  document: string;
  valuesDigest: string;
  status: RevisionStatus;
  parentRevisionId: string | null;
  secretRefs: SecretRef[];
  createdBy: string;
  createdAt: string;
  approvedBy?: string;
  approvedAt?: string;
  rejectedBy?: string;
  rejectedAt?: string;
  reason?: string;
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
      return 'superseded';
    default:
      return 'draft';
  }
}
