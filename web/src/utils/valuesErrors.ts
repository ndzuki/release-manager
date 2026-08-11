import { Code, ConnectError } from '@connectrpc/connect';
import type { ValidationIssue } from '@/types/valuesRevision';

export interface ValuesRequestError {
  code: string;
  message: string;
  issue?: ValidationIssue;
}

export function mapValuesError(error: unknown): ValuesRequestError {
  const connectError = ConnectError.from(error);
  const raw = connectError.rawMessage || '请求失败';
  const messageReason = raw.match(/^(invalid_yaml|secret_literal_forbidden|parent_conflict|not_approved|permission_denied|size_exceeded|revision_not_found):?\s*(.*)$/)?.[1];
  const metadataReason = connectError.metadata.get('X-Reason-Code') ?? undefined;
  const code = metadataReason || messageReason || (
    connectError.code === Code.Unavailable ? 'network_error'
      : connectError.code === Code.PermissionDenied ? 'permission_denied'
        : connectError.code === Code.NotFound ? 'revision_not_found'
          : 'unknown'
  );
  const location = raw.match(/line\s+(\d+)(?::(\d+))?/i);
  const issue = code === 'invalid_yaml' || code === 'secret_literal_forbidden' || code === 'size_exceeded'
    ? { code, message: raw, line: location?.[1] ? Number(location[1]) : undefined, column: location?.[2] ? Number(location[2]) : undefined } as ValidationIssue
    : undefined;
  if (code === 'parent_conflict') return { code, message: 'Revision 已被更新，请基于最新版本重新编辑' };
  if (code === 'not_approved') return { code, message: '无法审批：Revision 状态不允许此操作' };
  if (code === 'permission_denied') return { code, message: '无权执行此操作' };
  if (code === 'network_error' || code === 'operator_offline' || code === 'operator_timeout') {
    return { code, message: '网络或 Operator 暂不可用，请检查连接后重试' };
  }
  if (code === 'revision_not_found') return { code, message: 'Revision 不存在或已被删除' };
  return { code, message: raw, issue };
}
