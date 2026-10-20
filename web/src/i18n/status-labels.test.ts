import { describe, expect, it } from 'vitest';
import { statusLabel } from './status-labels';

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
    approve: '审批通过',
    reject: '驳回',
    install: '安装',
    upgrade: '升级',
    rollback: '回滚',
    emergency_change: '紧急变更',
    'export.created': '导出创建',
    verify_trust: '信任验证',
    login: '登录',
    logout: '退出登录',
  },
  auditResource: {
    customer: '客户',
    cluster: '集群',
    release: '发布',
    operation: '操作',
    release_bundle: '发布 Bundle',
    trust_root: '信任根',
    audit_export: '审计导出',
    organization: '组织',
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
