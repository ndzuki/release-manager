// Emergency error decoding: consume only typed EmergencyErrorDetail (via
// ConnectError.findDetails per core/go/connect-rpc.md) or the stable
// X-Reason-Code metadata header — never free-text messages. This mirrors the
// mapOperationError pattern in web/src/connect/operation-api.ts.
import { Code, ConnectError } from '@connectrpc/connect';
import { EmergencyErrorDetailSchema, EmergencyReasonCode } from '@/gen/orchestrator/v1/orchestrator_pb';

export interface EmergencyErrorDisplay {
  code: string;
  message: string;
  retryable: boolean;
  /** true when the code came from the typed EmergencyErrorDetail payload. */
  typed: boolean;
}

const REASON_MESSAGES: Record<string, string> = {
  // Typed EmergencyReasonCode values (server detail payloads).
  kill_switch_disabled: '紧急变更已被平台开关禁用',
  no_candidate_artifact: '候选制品不可用，请刷新后重试',
  values_conflict: '配置存在冲突，请刷新后重试',
  operation_in_progress: '该发布已有进行中的紧急变更',
  internal: '服务内部错误，请稍后重试',
  artifact_not_found: '候选制品不存在或已失效',
  workload_not_found: '目标工作负载不存在',
  container_not_found: '目标容器不存在',
  version_invalid: 'operationVersion 已失效，请刷新目标后重试',
  locked_path: '目标字段已被其他紧急变更锁定',
  // Legacy X-Reason-Code values emitted by the orchestrator.
  authorization_snapshot_stale: '授权快照已过期，请刷新后重试',
  permission_denied: '无权执行该操作',
  emergency_feature_disabled: '紧急变更功能已关闭',
  definition_not_found: '发布定义不存在',
  release_busy: '发布定义有进行中的标准操作',
  target_lock_conflict: '目标已被其他紧急变更锁定',
  conflicting_emergency: '目标已被其他紧急变更锁定',
  unresolved_effect: '存在未解析的紧急变更效果',
  emergency_effect_unresolved: '存在未解析的紧急变更效果',
  promotion_path_blocked: '收敛路径被占用',
  promotion_not_supported: '该变更不支持收敛策略，请使用 REVERT',
  hpa_managed: '副本数由 HPA 管理，不可修改',
  invalid_replicas: '副本数超出允许范围',
  annotation_key_not_allowed: '注解 key 不在白名单内',
  duplicate_annotation_key: '注解 key 重复',
  annotation_scope_mismatch: '注解 scope 不一致',
  invalid_annotation_entries: '注解条目不合法',
  artifact_not_trusted: '制品未通过信任验证',
  target_changed: '目标已变化，请刷新后重试',
  operator_offline: 'Operator 离线，请稍后重试',
  idempotency_conflict: '幂等键已绑定其他请求内容，请重新确认',
  invalid_cursor: '分页游标已失效',
  convergence_conflict: '收敛任务存在冲突，请刷新列表',
  convergence_revision_exists: '已存在进行中的收敛 revision',
  prepare_token_expired: '准备会话已过期，请重新准备',
  prepare_token_consumed: '准备会话已被消费',
  discard_not_allowed: '当前状态不允许丢弃',
  release_convergence_pending: '存在待收敛任务',
  cancel_not_allowed: '当前状态不允许取消',
  network_error: '网络错误，请检查连接后重试',
  // Validation/lifecycle codes from validateExecuteEmergencyRequest et al.
  release_definition_id_required: '缺少发布定义',
  invalid_workload_ref: 'workload_ref 无效',
  idempotency_key_required: '缺少幂等键',
  convergence_strategy_required: '缺少收敛策略',
  artifact_ref_required: '缺少制品引用',
  target_locks_required: 'REQUIRE_PROMOTION 需要目标锁',
  workload_ref_required: '缺少 workload_ref',
  container_required: '缺少目标容器',
  manifest_inventory_unavailable: '目标清单暂不可用，请稍后重试',
  customer_disabled: '客户已停用',
  release_definition_disabled: '发布定义已停用',
  authentication_required: '登录已失效，请重新登录',
  parent_conflict: '收敛父版本已变化，请刷新后重试',
  task_invalid: '所选任务不合法，请刷新列表',
  release_definition_not_found: '发布定义不存在',
  release_definition_owner_unresolved: '发布定义归属未解析',
  invalid_argument: '请求参数不合法，请检查后重试',
  not_found: '资源不存在或当前账号不可见',
};

const RETRYABLE_CODES = new Set<string>([
  'network_error',
  'authorization_snapshot_stale',
  'operator_offline',
  'operation_in_progress',
  'no_candidate_artifact',
  'manifest_inventory_unavailable',
  'invalid_cursor',
  'prepare_token_expired',
]);

function typedReasonName(value: EmergencyReasonCode): string {
  const name = EmergencyReasonCode[value];
  if (!name) return 'internal';
  return name.toLowerCase();
}

export function reasonMessageFor(code: string, fallback: string): string {
  return REASON_MESSAGES[code] ?? fallback;
}

/**
 * Maps any Connect transport/business error to a stable EmergencyErrorDisplay.
 * Priority: typed EmergencyErrorDetail → X-Reason-Code header → Connect code.
 */
export function mapEmergencyError(error: unknown): EmergencyErrorDisplay {
  const connectError = ConnectError.from(error);

  const details = connectError.findDetails(EmergencyErrorDetailSchema);
  const detail = details[0];
  if (detail) {
    const code = typedReasonName(detail.reasonCode);
    return {
      code,
      message: reasonMessageFor(code, detail.message || '紧急变更请求失败'),
      retryable: detail.retryable || RETRYABLE_CODES.has(code),
      typed: true,
    };
  }

  const reason = connectError.metadata.get('X-Reason-Code') ?? '';
  if (reason && REASON_MESSAGES[reason]) {
    return {
      code: reason,
      message: REASON_MESSAGES[reason],
      retryable: RETRYABLE_CODES.has(reason),
      typed: false,
    };
  }

  switch (connectError.code) {
    case Code.PermissionDenied:
      return { code: 'permission_denied', message: REASON_MESSAGES.permission_denied, retryable: false, typed: false };
    case Code.NotFound:
      return { code: 'not_found', message: REASON_MESSAGES.not_found, retryable: false, typed: false };
    case Code.InvalidArgument:
      return { code: 'invalid_argument', message: REASON_MESSAGES.invalid_argument, retryable: false, typed: false };
    case Code.AlreadyExists:
      return {
        code: 'idempotency_conflict',
        message: REASON_MESSAGES.idempotency_conflict,
        retryable: false,
        typed: false,
      };
    case Code.Aborted:
      return {
        code: reason || 'parent_conflict',
        message: reasonMessageFor(reason, REASON_MESSAGES.parent_conflict),
        retryable: RETRYABLE_CODES.has(reason),
        typed: false,
      };
    case Code.FailedPrecondition:
      return {
        code: reason || 'release_busy',
        message: reasonMessageFor(reason, '请求与当前状态冲突'),
        retryable: RETRYABLE_CODES.has(reason),
        typed: false,
      };
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return { code: 'network_error', message: REASON_MESSAGES.network_error, retryable: true, typed: false };
    default:
      return {
        code: reason || 'unknown',
        message: reasonMessageFor(reason, connectError.rawMessage || '紧急变更请求失败'),
        retryable: RETRYABLE_CODES.has(reason),
        typed: false,
      };
  }
}
