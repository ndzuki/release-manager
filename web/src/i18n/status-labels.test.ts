import { describe, expect, it } from 'vitest';
import { statusLabel, workloadKindLabel } from './status-labels';

/*
 * Every domain the server can send, checked exhaustively. The first version of this file
 * only covered operation/effect/operationType, so deleting a definition, valuesRevision,
 * convergence, audit, export, trustRoot or stage mapping went unnoticed.
 */
const EXPECTED: Record<string, Record<string, string>> = {
  operation: {
    pending: '等待中',
    preflight: '预检',
    queued: '排队中',
    running: '执行中',
    cancelling: '取消中',
    succeeded: '成功',
    failed: '失败',
    cancelled: '已取消',
    timeout: '超时',
  },
  operationType: {
    INSTALL: '安装',
    UPGRADE: '升级',
    ROLLBACK: '回滚',
    EMERGENCY: '紧急变更',
  },
  effect: { UNKNOWN: '未知', APPLIED: '已生效', NOT_APPLIED: '未生效', NOT_STARTED: '未开始' },
  definition: { draft: '草稿', active: '生效中', disabled: '已停用' },
  valuesRevision: {
    draft: '草稿',
    pending_approval: '待审批',
    approved: '已审批',
    rejected: '已驳回',
    superseded: '已被取代',
    discarded: '已丢弃',
  },
  convergence: { pending_promotion: '待提升', converged: '已收敛' },
  convergenceStrategy: {
    REQUIRE_PROMOTION: '生成收敛任务，需异人审批',
    REVERT_ON_NEXT_RECONCILE: '下次对账时回退',
    CONVERGENCE_STRATEGY_UNSPECIFIED: '未指定',
  },
  audit: { success: '成功', succeeded: '成功', failed: '失败', accepted: '已受理' },
  export: { pending: '等待中', running: '生成中', succeeded: '已完成', failed: '已失败' },
  trustRoot: {
    pending: '待生效',
    active: '生效中',
    grace: '宽限期',
    retired: '已退役',
    revoked: '已吊销',
  },
  stage: { passed: '通过', failed: '失败', skipped: '跳过', timeout: '超时', cancelled: '已取消' },
  ackStage: { persisted: '已写入磁盘', unknown: '写入阶段' },
  bundle: {
    received: '已接收',
    validated: '已验证',
    rejected: '已拒绝',
    archived: '已归档',
    unspecified: '未指定',
  },
  preflightStage: {
    artifact: '制品检查',
    render: '渲染检查',
    cluster: '集群检查',
    runtime_pull: '镜像拉取',
  },
  auditAction: {
    create: '创建',
    update: '更新',
    delete: '删除',
    read: '读取',
    write: '写入',
    query: '查询',
    submit: '提交',
    submitted: '已提交',
    close: '关闭',
    seed: '初始化数据',
    approve: '审批通过',
    approved: '已批准',
    reject: '驳回',
    rejected: '已驳回',
    discarded: '已丢弃',
    superseded: '已被取代',
    install: '安装',
    upgrade: '升级',
    rollback: '回滚',
    emergency_change: '紧急变更',
    'cleanup.gc': '清理任务',
    'export.created': '导出创建',
    verify_trust: '信任验证',
    login: '登录',
    logout: '退出登录',
    enrolled: '已注册',
    'operator.revoked': '撤销 Operator',
    'operator.revoked.again': '重复撤销 Operator',
    'operator.enrollment_token.created': '创建注册令牌',
    'operator.enrollment_token.replaced': '替换注册令牌',
    'operator.enrollment_token.revoked': '作废注册令牌',
    'release.emergency.execute': '执行紧急变更',
    'release.emergency.resolve': '解决紧急锁',
    'release.operation.create': '创建发布操作',
    'release.values.approve': '审批 Values 版本',
    'release.values.create': '创建 Values 版本',
    set_approved_annotation: '设置批准注解',
    set_container_image: '设置容器镜像',
    set_image: '设置镜像',
    set_replicas: '设置副本数',
  },
  auditResource: {
    customer: '客户',
    cluster: '集群',
    release: '发布',
    operation: '操作',
    release_operation: '发布操作',
    release_definition: '发布定义',
    release_bundle: '发布 Bundle',
    values_revision: 'Values 版本',
    trust_root: '信任根',
    audit_export: '审计导出',
    audit_outbox: '审计发件箱',
    organization: '组织',
    operator: 'Operator 身份',
    enrollment_token: '注册令牌',
    api_service: 'API 服务',
    gc_cycle: '清理周期',
    migration: '迁移',
  },
};

describe('every domain', () => {
  it.each(Object.entries(EXPECTED))('translates all %s values', (domain, values) => {
    for (const [value, label] of Object.entries(values)) {
      expect(statusLabel(domain as never, value), `${domain}.${value}`).toBe(label);
    }
  });
});

/*
 * The console printed server enums raw in several places while other screens translated
 * them, so the same value read `running` on one page and `执行中` on another.
 */
describe('statusLabel', () => {
  it('translates every operation status the server can send', () => {
    const expected: Record<string, string> = {
      pending: '等待中',
      preflight: '预检',
      queued: '排队中',
      running: '执行中',
      cancelling: '取消中',
      succeeded: '成功',
      failed: '失败',
      cancelled: '已取消',
      timeout: '超时',
    };
    for (const [value, label] of Object.entries(expected)) {
      expect(statusLabel('operation', value), value).toBe(label);
    }
  });

  it('translates the emergency effect statuses', () => {
    expect(statusLabel('effect', 'UNKNOWN')).toBe('未知');
    expect(statusLabel('effect', 'APPLIED')).toBe('已生效');
    expect(statusLabel('effect', 'NOT_APPLIED')).toBe('未生效');
    expect(statusLabel('effect', 'NOT_STARTED')).toBe('未开始');
  });

  it('translates the operation kinds', () => {
    expect(statusLabel('operationType', 'INSTALL')).toBe('安装');
    expect(statusLabel('operationType', 'UPGRADE')).toBe('升级');
    expect(statusLabel('operationType', 'ROLLBACK')).toBe('回滚');
  });

  it('falls back to the raw value for a status it does not know', () => {
    // A new server status must stay visible instead of rendering as an empty cell.
    expect(statusLabel('operation', 'paused')).toBe('paused');
  });

  it('renders nothing for a missing value', () => {
    expect(statusLabel('operation', undefined)).toBe('');
    expect(statusLabel('audit', null)).toBe('');
  });
});

describe('workloadKindLabel', () => {
  it('renders a shouted Kubernetes kind in its canonical casing', () => {
    expect(workloadKindLabel('DEPLOYMENT')).toBe('Deployment');
    expect(workloadKindLabel('STATEFULSET')).toBe('StatefulSet');
    expect(workloadKindLabel('Deployment')).toBe('Deployment');
  });

  it('keeps a custom resource kind as the server sent it', () => {
    expect(workloadKindLabel('Rollout')).toBe('Rollout');
    expect(workloadKindLabel('')).toBe('');
  });
});
