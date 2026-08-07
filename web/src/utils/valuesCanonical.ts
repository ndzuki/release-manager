import deepDiff from 'deep-diff';
import { load } from 'js-yaml';
import type { CanonicalResult, DiffChange, DiffResult } from '@/types/valuesRevision';

function sortValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortValue);
  if (value !== null && typeof value === 'object') {
    const record = value as Record<string, unknown>;
    return Object.fromEntries(Object.keys(record).sort().map((key) => [key, sortValue(record[key])]));
  }
  return value;
}

function parseIssue(error: unknown): { line?: number; column?: number; message: string } {
  if (error && typeof error === 'object') {
    const candidate = error as { mark?: { line?: number; column?: number }; reason?: string; message?: string };
    return {
      line: candidate.mark?.line === undefined ? undefined : candidate.mark.line + 1,
      column: candidate.mark?.column === undefined ? undefined : candidate.mark.column + 1,
      message: candidate.reason || candidate.message || 'invalid YAML',
    };
  }
  return { message: String(error) };
}

export function canonicalize(document: string, maxBytes = 1 << 20): CanonicalResult {
  const size = new TextEncoder().encode(document).byteLength;
  if (size > maxBytes) {
    return {
      value: null,
      document: '',
      issue: { code: 'size_exceeded', message: `文档过大 (当前 ${size} bytes，上限 ${maxBytes} bytes)` },
    };
  }
  try {
    const value = sortValue(document.trim() === '' ? {} : load(document));
    const normalized = value === undefined || value === null ? {} : value;
    return { value: normalized, document: JSON.stringify(normalized), issue: null };
  } catch (error) {
    const issue = parseIssue(error);
    return {
      value: null,
      document: '',
      issue: {
        code: 'invalid_yaml',
        message: `YAML 语法错误: ${issue.line ?? '?'}:${issue.column ?? '?'} — ${issue.message}`,
        line: issue.line,
        column: issue.column,
      },
    };
  }
}

function pathLabel(path: readonly unknown[]): string {
  if (path.length === 0) return '.';
  return path.reduce<string>((label, part) => (
    typeof part === 'number' ? `${label}[${part}]` : `${label}.${String(part)}`
  ), '');
}

function valueAt(root: unknown, path: readonly unknown[]): unknown {
  let value = root;
  for (const part of path) {
    if (value === null || typeof value !== 'object') return undefined;
    value = (value as Record<string | number, unknown>)[part as string | number];
  }
  return value;
}

export function canonicalDiff(current: unknown, parent: unknown): DiffResult {
  const normalizedCurrent = sortValue(current);
  const normalizedParent = sortValue(parent);
  const rawChanges = deepDiff(normalizedParent, normalizedCurrent) ?? [];
  const changes: DiffChange[] = [];
  const arrayPaths = new Set<string>();

  for (const change of rawChanges) {
    const path = change.path ?? [];
    if (change.kind === 'A') {
      const label = pathLabel(path);
      if (!arrayPaths.has(label)) {
        arrayPaths.add(label);
        changes.push({
          path: label,
          kind: 'array_change',
          oldValue: valueAt(normalizedParent, path),
          newValue: valueAt(normalizedCurrent, path),
        });
      }
      continue;
    }
    const arrayIndex = path.findIndex((part) => typeof part === 'number');
    if (arrayIndex >= 0) {
      const arrayPath = path.slice(0, arrayIndex);
      const label = pathLabel(arrayPath);
      if (!arrayPaths.has(label)) {
        arrayPaths.add(label);
        changes.push({
          path: label,
          kind: 'array_change',
          oldValue: valueAt(normalizedParent, arrayPath),
          newValue: valueAt(normalizedCurrent, arrayPath),
        });
      }
      continue;
    }
    if (change.kind === 'N') {
      changes.push({ path: pathLabel(path), kind: 'added', newValue: change.rhs });
    } else if (change.kind === 'D') {
      changes.push({ path: pathLabel(path), kind: 'removed', oldValue: change.lhs });
    } else {
      changes.push({ path: pathLabel(path), kind: 'modified', oldValue: change.lhs, newValue: change.rhs });
    }
  }

  return { changes, hasChanges: changes.length > 0 };
}
