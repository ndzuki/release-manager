import { globSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { bareCopy, referencedKeys } from './copy-lint';
import { hasMessage, messageKeys } from './messages';

/*
 * Copy-hygiene gates for plan §11 W5 (single locale, centralised copy).
 *
 * Two mechanical invariants, both locale-agnostic: they hold whatever language the
 * console converges on, and they make "how far has the copy been centralised"
 * measurable instead of a claim.
 */
const webRoot = resolve(__dirname, '../..');
const messageFile = resolve(webRoot, 'src/i18n/messages.ts');

function sourceFiles(): string[] {
  return globSync(`${webRoot}/src/**/*.{vue,ts}`).filter(
    (file: string) =>
      !file.endsWith('.test.ts') && !file.endsWith('.spec.ts') && !file.endsWith('messages.ts'),
  );
}

describe('message catalog usage', () => {
  // The catalog follows the same rule as the design tokens: an entry without a
  // consumer is dead copy, and dead copy is what makes a later convergence
  // mechanical-looking but wrong. `reason.*` is the one exception: those entries are
  // looked up by prefix from the server's reason code (copyForReason), so they never
  // appear as a literal t('...') call.
  it('has no entry without a consumer', () => {
    const used = new Set<string>();
    for (const file of sourceFiles()) {
      const text = readFileSync(file, 'utf8');
      for (const key of referencedKeys(text)) used.add(key);
      // A key can also be consumed through a lookup table (`LABEL_KEYS = { x: 'a.b' }`),
      // where it never appears inside a t(...) call. Comments are stripped first, so a
      // commented-out mention does not count as a consumer.
      const withoutComments = text
        .replace(/<!--[\s\S]*?-->/g, '')
        .replace(/\/\*[\s\S]*?\*\//g, '')
        .replace(/(^|[^:])\/\/[^\n]*/g, '$1');
      for (const key of messageKeys()) {
        if (key.startsWith('reason.')) continue;
        if (withoutComments.includes(`'${key}'`) || withoutComments.includes('`' + key + '`')) used.add(key);
      }
    }
    const orphans = messageKeys().filter((key) => !key.startsWith('reason.') && !used.has(key));

    expect(orphans, `these catalog entries have no consumer: ${orphans.join(', ')}`).toEqual([]);
  });

  it('knows every key it is asked for', () => {
    for (const file of sourceFiles()) {
      const text = readFileSync(file, 'utf8');
      for (const key of referencedKeys(text)) {
        expect(hasMessage(key), `${file} asks for the unknown key ${key}`).toBe(true);
      }
    }
  });
});

/*
 * Files whose user-visible copy has been moved into the catalog. Any literal text a
 * migrated file still shows must be listed here WITH a reason, and the list must
 * shrink rather than grow: an entry that no longer matches the file fails the test,
 * so the allowlist cannot rot.
 */
/*
 * `allowed` holds literals the file still shows. Two kinds appear here:
 *   - non-copy values (product names, units, format identifiers) with a per-file reason;
 *   - Chinese copy that is ALREADY in the target locale but still inline. Centralising
 *     that is a separate pass over the Chinese wave, and this manifest keeps the gate
 *     useful in the meantime: it still fails when English README-style copy is added.
 */
const MIGRATED: Array<{ file: string; allowed: string[]; reason: string }> = [
  {
    file: 'src/components/common/AppShell.vue',
    allowed: ['Release Manager', '制品生命周期', '发布 Bundle', '发布定义', '紧急锁', '信任根', '授权绑定', '组织成员', '修改密码'],
    reason:
      'The brand is a product name, and the governance entries are the Chinese wave whose copy belongs to the locale convergence (W5), not to this migration',
  },
  {
    file: 'src/components/common/EmptyState.vue',
    allowed: [],
    reason: 'defaults come from the catalog',
  },
  {
    file: 'src/components/common/ForbiddenState.vue',
    allowed: [],
    reason: 'defaults come from the catalog',
  },
  {
    file: 'src/components/common/LoadingState.vue',
    allowed: [],
    reason: 'default label comes from the catalog',
  },
  {
    file: 'src/components/common/OrganizationSwitcher.vue',
    allowed: [],
    reason: 'label and error message come from the catalog',
  },
  {
    file: 'src/components/common/ErrorState.vue',
    allowed: [],
    reason: 'the default title comes from the catalog',
  },
  {
    file: 'src/pages/ForbiddenPage.vue',
    allowed: [],
    reason: 'the action label comes from the catalog',
  },
  {
    file: 'src/components/operations/OperationConfirmPanel.vue',
    allowed: ['v ， 个镜像'],
    reason:
      'the fragments around the interpolations (version prefix plus the image-count unit); the sentence itself is Chinese',
  },
  {
    file: 'src/components/operations/OperationForm.vue',
    allowed: ['vr-…'],
    reason: 'placeholder showing the shape of a ValuesRevision id, not copy',
  },
  {
    file: 'src/components/operations/PatchOverrideEditor.vue',
    allowed: ['image.tag', 'LITERAL', 'SECRET_REF'],
    reason: 'a dot-path example and the wire enum values of the patch kind',
  },
  {
    file: 'src/components/operations/PreflightResultPanel.vue',
    allowed: [],
    reason: 'preflight panel converged to Chinese (wave 8)',
  },
  {
    file: 'src/components/operations/TimelineEntryItem.vue',
    allowed: ['： / 就绪'],
    reason:
      'the fragments around the workload/ready interpolations (colon, slash and the 就绪 unit); the sentence itself is Chinese',
  },
  {
    file: 'src/components/emergency/EmergencyConvergenceCard.vue',
    allowed: [],
    reason: 'convergence card converged to Chinese (wave 8)',
  },
  {
    file: 'src/components/emergency/EmergencyConfirmDialog.vue',
    allowed: [],
    reason: 'emergency confirmation converged to Chinese (wave 8)',
  },
  {
    file: 'src/components/emergency/ConvergenceTaskList.vue',
    allowed: ['选择'],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/components/emergency/EmergencyAnnotationEditor.vue',
    allowed: ['注解变更当前后端契约暂不支持提交，此处仅展示白名单与批量校验。', '移除'],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/components/emergency/EmergencyResultPanel.vue',
    allowed: [
      '受理状态',
      '紧急变更结果',
      'image ${values.container} → ${values.imageReference}',
      'replicas → ${values.replicas}',
      'annotations ${values.annotations.map((entry) =>',
    ],
    reason:
      'Chinese labels plus evidence strings assembled at runtime from the diff payload (no static scan can follow those)',
  },
  {
    file: 'src/pages/EmergencyChangePage.vue',
    allowed: [
      '选择变更目标',
      '选择容器与制品',
      '镜像变更暂不可用：平台尚未采集到该工作负载的容器信息，因此无法选择容器与制品。',
      '填写变更信息',
      '确认变更',
      '正在加载授权与目标…',
      '403',
      '你没有执行紧急变更的权限（release.emergency.execute）。',
      '404',
      '紧急变更不可用：功能已关闭或发布定义不存在。',
    ],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/ConvergenceTasksPage.vue',
    allowed: [
      '选择不兼容：',
      '已选择 个任务',
      '正在加载收敛任务…',
      '403',
      '你没有创建 ValuesRevision 收敛的权限（canCreateValuesRevision）。',
    ],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/components/values/RejectRevisionDialog.vue',
    allowed: ['拒绝原因（可选）', '取消', '拒绝 ValuesRevision', '说明需要修改的内容', '拒绝原因过长 (上限 1000 字符)'],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/components/values/ValuesConflictDialog.vue',
    allowed: [
      '稍后处理',
      'Revision 已被更新',
      '请重新基于最新 approved revision 计算 diff。当前编辑内容会保留，不会覆盖本地 draft。',
      '重新加载最新 Revision',
    ],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/stores/valuesEditor.ts',
    allowed: [
      '服务端返回的 canonical document 无法解析',
      '已创建收敛 Draft（任务已绑定）',
      '已保存为 Draft',
      '请先保存 Draft；提交的是已保存的 Revision',
      '缺少已批准父 Revision 的基线，无法校验锁定路径；请刷新后重试',
      '已批准，收敛任务已标记 converged',
      'Draft 已丢弃，任务已解绑',
    ],
    reason:
      'Chinese copy already in the target locale; the toast strings reach the page through editor.toast, which no scan can follow',
  },
  {
    file: 'src/components/values/SecretRefEditor.vue',
    allowed: ['SecretRef', '添加 SecretRef', '仅保存 namespace 内 Secret 的 name、key 与目标 path，不读取或传输 value。', '尚未配置 SecretRef。', '请选择 Secret 名称', '请选择 Key', '移除', '选择 name/key 后自动生成'],
    reason: 'SecretRef is the domain noun used as the row heading (CONTEXT/GLOSSARY)',
  },
  {
    file: 'src/components/values/ValuesCodeEditor.vue',
    allowed: [],
    reason: 'values editor aria labels come from the catalog (wave 6)',
  },
  {
    file: 'src/components/values/ValuesDiffPanel.vue',
    allowed: ['无 canonical 变化。格式、注释与 key 顺序差异不会产生无意义 diff。'],
    reason: 'values diff panel converged to Chinese (wave 6)',
  },
  {
    file: 'src/components/values/ValuesEditorSkeleton.vue',
    allowed: [],
    reason: 'its label comes from the catalog (wave 6)',
  },
  {
    file: 'src/components/values/ValuesRevisionActions.vue',
    allowed: ['不可审批自己创建的 Revision。', '保存 Draft', '请先保存 Draft：提交的是已保存的 Revision'],
    reason: 'revision workflow converged to Chinese (wave 6)',
  },
  {
    file: 'src/pages/ValuesEditorPage.vue',
    allowed: ['YAML', 'JSON', '编辑 canonical values 并通过 SecretRef 引用集群 Secret。', '已恢复未保存的编辑', '创建首个配置 Revision。', '当前角色为只读。服务端仍会独立执行授权。', '重试', '确认丢弃当前 Draft？绑定的 个收敛任务将被解绑。', '取消', '确认丢弃', 'ValuesRevision 加载失败', '确认丢弃 Draft'],
    reason: 'editor language identifiers; the surrounding labels are Chinese',
  },
  {
    file: 'src/pages/DefinitionsPage.vue',
    allowed: ['发布定义', '每个定义把 Bundle 来源与客户/集群/namespace/release 名称绑定；', '决定收敛后的紧急变更如何提升为标准 ValuesRevision。', '客户 ID（可空）', '集群 ID（可空）', '包含已停用', '查询', '有 个定义的 Promotion Mapping 无法解析（契约违反）： 。这些行已禁用编辑，避免把损坏数据写回。', '名称', '状态', '版本', '操作', '无法解析', '编辑', '编辑 （版本 ）', '提交时会带上读到的版本号；若期间被他人修改，服务端会拒绝（乐观锁），页面会刷新后提示重试。', '删除', '新增映射', '取消', '操作未成功', '正在读取发布定义…', '没有发布定义', '当前过滤条件下没有定义；清空客户/集群过滤可查看组织内全部定义。', '第 ${index + 1} 行映射缺少必填项（workload kind/name、field、values path）'],
    reason: 'release definitions converged to Chinese (wave 6)',
  },
  {
    file: 'src/pages/NotFoundPage.vue',
    allowed: [],
    reason: 'the 404 copy comes from the catalog (wave 7)',
  },
  {
    file: 'src/pages/InitPage.vue',
    allowed: [],
    reason: 'first-time setup converged to Chinese (wave 6)',
  },
  {
    file: 'src/pages/OperatorEnrollPage.vue',
    allowed: ['operator-staging'],
    reason: 'a placeholder example of the DNS-compatible operator name, not copy to translate',
  },
  {
    file: 'src/pages/OperatorDetailPage.vue',
    allowed: [],
    reason: 'operator detail converged to Chinese (wave 5)',
  },
  {
    file: 'src/components/audit/AuditFilters.vue',
    allowed: [],
    reason: 'audit wave converged to Chinese (wave 5)',
  },
  {
    file: 'src/components/audit/AuditEventTable.vue',
    allowed: ['ms', "${actorKindLabels[actor.kind]}:${actor.id || t('audit.table.unknownActor')}"],
    reason:
      'ms is the unit suffix (servers report milliseconds) and the actor label is assembled at runtime from the kind map plus the id',
  },
  {
    file: 'src/components/audit/AuditEventDetail.vue',
    allowed: ['ms'],
    reason: 'ms is the unit suffix',
  },
  {
    file: 'src/components/audit/AuditExportPanel.vue',
    allowed: [],
    reason: 'audit export panel converged to Chinese (wave 5)',
  },
  {
    file: 'src/pages/AuditPage.vue',
    allowed: [],
    reason: 'audit page converged to Chinese (wave 5)',
  },
  {
    file: 'src/stores/audit.ts',
    allowed: [],
    reason: 'its failure copy comes from the catalog (wave 5)',
  },
  {
    file: 'src/utils/cluster-routing.ts',
    allowed: [],
    reason: 'validation messages come from the catalog (wave 9)',
  },
  {
    file: 'src/bootstrap.ts',
    allowed: [],
    reason: 'startup failure copy comes from the catalog (wave 9)',
  },
  {
    file: 'src/connect/cluster-api.ts',
    allowed: ['chartRules[${routeIndex - input.imageRules.length}].${match[2]}'],
    reason: 'a wire field path built at runtime, not copy',
  },
  {
    file: 'src/connect/customer-api.ts',
    allowed: [],
    reason: 'its failure copy comes from the catalog (wave 9)',
  },
  {
    file: 'src/router/index.ts',
    allowed: ['Forbidden', 'Init', 'Login'],
    reason: 'vue-router route names, not copy',
  },
  {
    file: 'src/pages/ArtifactLifecyclePage.vue',
    allowed: ['制品生命周期', '保留期垃圾回收与归档恢复；两者都只对 platform_admin 开放（cleanup/write），维护模式下会被拒绝。', '执行保留期 GC', '不可撤销', '：清理会删除超出保留期的 bundle、候选制品与 preflight 记录；计数是', '行数', '而非释放的字节。 调用', '同步', '执行（服务端预算可达约 1 小时），因此客户端对本过程使用更长的死线。', '同一幂等键在保留窗口内（默认 24 小时，可配置）会被拒绝（', 'cleanup_already_requested', '）而不重跑； 「重试」复用同一个键，点「执行 GC」才是新的清理请求。注意：单节点部署可能没有幂等键表，此时只有进程内互斥生效。', '我已确认清理会删除超出保留期的制品，且不可撤销', '计数', '删除 bundle', '删除候选制品', '删除 preflight', '跳过 bundle', '非致命错误（ ）', '成功响应里仍可能包含逐阶段错误，需要人工确认：', '本次没有非致命错误。', '恢复已归档 Bundle', '只有「从', 'validated', '归档」的 bundle 能恢复；恢复后回到', 'validated', '，不是重新接收。 该操作是收敛的：重复调用再次成功且不改数据，因此要按', '存储的原始状态', '理解', 'previous_status', '已恢复', '，归档前状态：', '清理未执行', 'bundle id', '恢复未成功', '执行 GC'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/BundlesPage.vue',
    allowed: ['发布 Bundle', 'Bundle 是不可变输入：内容摘要、Chart 引用与镜像绑定，以及签名/SBOM/来源证明的引用。', '状态', '全部', '请先填写 Release Definition ID：服务端把它同时作为', '查询范围', '授权对象', '，缺少它会被拒绝 （不是「没有数据」），因此页面在填写前不会发起请求。', '名称', '状态', '摘要', 'Chart', '镜像绑定', '接收时间', '操作', '明细', '正在追加下一页…', 'Bundle 明细', '名称', '摘要', '状态', '证据引用', '签名', '来源证明', '镜像绑定（ ）', '引用', '摘要', 'Values 路径', '关闭明细', 'definition id', 'chart name', '加载未成功', '正在读取 Bundle…', '没有 Bundle', '当前过滤条件下没有 Bundle；清空过滤可查看全部（注意：无权限时服务端会返回拒绝而不是空列表）。', '正在读取明细…', '明细加载失败'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/StuckLocksPage.vue',
    allowed: ['卡住的紧急锁', '终态 EMERGENCY 操作在观察窗口内仍未确认集群效果时会保留锁，定义因此无法接受新任务。', 'Release Definition 过滤（留空 = 当前组织内全部）', '查询', '定义 / 操作', '动作', '锁定路径', '终态时间 / 卡住自', '观察窗口', '操作', 'op', '卡住自', '释放锁', '释放锁', '没有撤销', '：释放错误会让一个集群状态仍未知的定义重新接受任务。第二次释放不可重放（会报未找到或状态冲突）。', '释放模式', 'NOT_APPLIED_PROVEN —— 能证明命令从未生效（例如从未 ACK_PERSISTED 且操作会话离线）；效果记为 NOT_APPLIED。', 'AUDITED_OVERRIDE —— 无法证明未生效但必须接管目标；效果', '保留 UNKNOWN', '，释放全程审计，迟到的结果仍可能解析效果。', '释放原因（必填，≤1000 字）', '解锁证据（可选，≤500 字）', '我已通过集群观察核实该效果确实可以释放（释放后定义会重新接受任务，且没有撤销）', '取消', 'Release Definition ID', '操作未成功', '正在读取卡住的锁…', '没有卡住的锁', '当前范围内没有「效果未知且已过观察窗口」的紧急操作——这正是期望状态。', 'NOT_APPLIED_PROVEN', 'AUDITED_OVERRIDE', '为什么可以释放这个锁', '例如 kubectl get deploy -o yaml 的观察结论', '释放原因最多 1000 个字符', '解锁证据最多 500 个字符'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/TrustPolicyPage.vue',
    allowed: ['签名信任根', '环境', '策略版本', '· 可用信任根', '状态', '生效自 / 宽限至', '操作', '宽限至', '确认吊销信任根 ？', '吊销会立即停止该密钥的签名校验，并', '提升吊销 epoch', '（缓存持有者据此失效）；记录会保留。 已用该密钥验证过的制品不会因此失去既有地位。', '取消', '信任根操作未成功', '正在读取信任策略…', '该环境还没有信任根', '没有任何签名密钥被信任（策略版本 1、epoch 0）——这是「尚未配置」而不是错误。'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/OperationCreatePage.vue',
    allowed: ['创建发布操作', '选择制品和已审批配置，确认完整目标后启动 Preflight。', '返回 Releases', '查看进行中操作', '重试', '正在加载可用制品与配置…', '操作选项加载失败', '没有可创建操作的选项', '请先准备 validated Bundle；已审批的 ValuesRevision ID 需从配置中心获取后手动填写。', '操作创建失败'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/OperationListPage.vue',
    allowed: ['操作历史', '返回发布清单', '创建操作', '状态', '页码已过期，已从最新一页重新加载（筛选条件保留）。', '重试', '类型', '状态', '目标 Revision', '创建时间', 'Operation', '面包屑', '操作历史加载失败', '正在加载操作历史…', '暂无操作记录', '当前筛选条件下没有操作。', '该 ReleaseDefinition 还没有执行过操作。'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/OperationDetailPage.vue',
    allowed: ['Releases', '操作历史', '操作', '取消中…', '取消操作', '刷新', 'ReleaseDefinition', 'StateVersion', 'TargetRevision', '创建时间', '更新时间', '终止时间', '正在加载 Operation…', '实时更新已关闭', '点击刷新加载 Operation 最新状态', 'Operation 加载失败', '操作已完成，无法取消', 'Release'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/HomePage.vue',
    allowed: ['Release Manager', '进入客户 → 集群 → 发布的层级', 'Promotion Mapping 与定义设置', '发布 Bundle', '保留期 GC 与归档恢复'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/components/releases/ReleaseInventoryTable.vue',
    allowed: ['Release', '状态', 'Chart', 'Revision', '最近同步', '紧急变更', '操作', '操作历史', '未绑定 Definition', '紧急变更', '紧急变更', '收敛', '创建操作', '未绑定 Definition', '回滚', '未关联 ReleaseDefinition，紧急变更入口不可用', '存在进行中的标准操作，紧急变更入口已阻断', '发起紧急变更'],
    reason: 'Chinese copy already in the target locale (plus server wire values and domain nouns); centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/App.vue',
    allowed: [],
    reason: 'its only label comes from the catalog',
  },
  {
    file: 'src/pages/ChangePasswordPage.vue',
    allowed: ['修改密码', '修改成功后所有会话都会被撤销，需要重新登录。', '密码已修改，正在跳转到登录页…', '当前密码', '新密码', '确认新密码', '密码只做非空与一致性校验；上限 字节（bcrypt 限制），不设复杂度要求。', '返回', '修改密码失败', '新密码过长（上限 ${PASSWORD_MAX_BYTES} 字节）'],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/BindingsPage.vue',
    allowed: ['授权绑定', '客户', '状态', '版本', '建立时间', '操作', '撤销', '新增绑定', '客户 ID', '能力授予', '契约没有「列出既有授权」的 RPC，因此这里只能施加一次授予/撤销并回报服务端返回的版本号。', '主体（用户 ID）', '能力', '动作', '授予', '撤销', '操作未成功', '正在加载绑定…', '暂无绑定', '该组织还没有与任何客户建立授权绑定。', '客户 ID', '用户 ID', '请填写客户 ID', '请填写主体（用户 ID）'],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/pages/OrganizationPage.vue',
    allowed: ['组织成员', '用户', '角色', '版本', '加入时间', '操作', '修改 的角色', '移除', '只读', '添加成员', '用户 ID', '角色', '成员操作未成功', '正在加载成员…', '暂无成员', '该组织还没有成员记录。', '用户 ID', '请填写用户 ID'],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/components/releases/ReleaseStatusBadge.vue',
    allowed: ['Release 已从集群中消失', 'Release 与最近一次同步结果一致'],
    reason: 'Chinese copy already in the target locale; centralising it is the separate Chinese-wave pass',
  },
  {
    file: 'src/connect/operator-api.ts',
    allowed: [],
    reason: 'its failure copy comes from the catalog',
  },
  {
    file: 'src/connect/client.ts',
    allowed: [],
    reason: 'its timeout copy comes from the catalog',
  },
  {
    file: 'src/stores/auth.ts',
    allowed: [],
    reason: 'its refusal copy comes from the catalog',
  },
  {
    file: 'src/stores/cluster.ts',
    allowed: [],
    reason: 'its validation copy comes from the catalog',
  },
  {
    file: 'src/stores/operator.ts',
    allowed: [],
    reason: 'its validation copy comes from the catalog',
  },
  {
    file: 'src/components/clusters/RoutePreview.vue',
    allowed: [],
    reason: 'its labels come from the catalog',
  },
  {
    file: 'src/pages/LocalUsersPage.vue',
    allowed: [],
    reason: 'every string comes from the catalog (TASK-212)',
  },
  {
    file: 'src/connect/local-user-api.ts',
    allowed: [],
    reason: 'error copy comes from the catalog, correlation data from the response headers',
  },
  {
    file: 'src/stores/localUsers.ts',
    allowed: [],
    reason: 'its only copy comes from the catalog',
  },
  {
    file: 'src/utils/operator-format.ts',
    allowed: [],
    reason: 'the reason labels and the "never" fallback come from the catalog (wave 4)',
  },
  {
    file: 'src/utils/operator-validation.ts',
    allowed: [],
    reason: 'the field messages come from the catalog (wave 4)',
  },
  {
    file: 'src/components/operators/EnrollmentTokenModal.vue',
    allowed: [],
    reason: 'operator wave converged to Chinese (wave 4)',
  },
  {
    file: 'src/components/operators/OperatorFilters.vue',
    allowed: [],
    reason: 'operator wave converged to Chinese (wave 4)',
  },
  {
    file: 'src/components/operators/OperatorStatusBadge.vue',
    allowed: [],
    reason: 'its labels come from the catalog (wave 4)',
  },
  {
    file: 'src/components/operators/OperatorTable.vue',
    allowed: [],
    reason: 'operator wave converged to Chinese (wave 4)',
  },
  {
    file: 'src/components/operators/PendingTokenPanel.vue',
    allowed: [],
    reason: 'operator wave converged to Chinese (wave 4)',
  },
  {
    file: 'src/components/operators/RevokeOperatorDialog.vue',
    allowed: [],
    reason: 'operator wave converged to Chinese (wave 4)',
  },
  {
    file: 'src/pages/OperatorListPage.vue',
    allowed: [],
    reason: 'operator wave converged to Chinese (wave 4)',
  },
  {
    file: 'src/components/customers/CustomerForm.vue',
    allowed: [],
    reason: 'customer sub-component converged to Chinese (wave 3)',
  },
  {
    file: 'src/components/customers/CustomerHistory.vue',
    allowed: [],
    reason: 'customer sub-component converged to Chinese (wave 3)',
  },
  {
    file: 'src/components/customers/DisableCustomerDialog.vue',
    allowed: [],
    reason: 'customer sub-component converged to Chinese (wave 3)',
  },
  {
    file: 'src/components/clusters/ClusterTargetSelect.vue',
    allowed: [],
    reason: 'cluster sub-component converged to Chinese (wave 3)',
  },
  {
    file: 'src/components/clusters/RouteRuleEditor.vue',
    allowed: [],
    reason: 'cluster routing-rule editor converged to Chinese (wave 3)',
  },
  {
    file: 'src/components/releases/ReleaseInventorySkeleton.vue',
    allowed: [],
    reason: 'its single label comes from the catalog (wave 3)',
  },
  {
    file: 'src/pages/CustomerListPage.vue',
    allowed: [],
    reason: 'customer wave converged to Chinese (2026-09-28)',
  },
  {
    file: 'src/pages/CustomerDetailPage.vue',
    allowed: [],
    reason: 'customer wave converged to Chinese (2026-09-28)',
  },
  {
    file: 'src/pages/ClusterListPage.vue',
    allowed: [],
    reason: 'cluster wave converged to Chinese (2026-09-28)',
  },
  {
    file: 'src/pages/ClusterDetailPage.vue',
    allowed: [],
    reason: 'cluster wave converged to Chinese (2026-09-28)',
  },
  {
    file: 'src/pages/ClusterEditPage.vue',
    allowed: [],
    reason: 'cluster wave converged to Chinese (2026-09-28)',
  },
  {
    file: 'src/pages/LoginPage.vue',
    allowed: ['Release Manager'],
    reason: 'the brand is a product name; every other string comes from the catalog',
  },
];


/*
 * Files that still show copy and are NOT in MIGRATED.
 *
 * The gate used to check only the files it knew about, so an unconverted file could sit
 * there unnoticed (an independent review found 37 of them). Recording them here makes the
 * remaining work explicit: every file with copy must be either migrated or listed, and an
 * entry that no longer has copy fails so the list can only shrink.
 */
const NOT_YET_MIGRATED: Array<{ file: string; reason: string }> = [
  {
    file: 'src/utils/valuesCanonical.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/utils/valuesErrors.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/utils/valuesValidation.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/artifactLifecycle.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/convergenceSelection.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/definitions.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/governance.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/operationForm.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/operationTimeline.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/organization.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/releaseInventory.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/stuckLocks.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/stores/trustPolicy.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/pages/ReleaseInventoryPage.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/features/emergency/errors.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/features/emergency/validation.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/auth-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/bundle-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/cleanup-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/definition-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/error-copy.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/operation-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/organization-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/rollback-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/stuck-lock-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/trust-api.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/connect/values-revision.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/composables/useFocusTrap.ts',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/releases/RollbackReleaseDialog.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/operations/CancelOperationDialog.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/operations/DisconnectBanner.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/operations/OperationTimeline.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/emergency/ConvergenceLockedPathsPanel.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/emergency/EmergencyArtifactSelector.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/emergency/EmergencyChangeForm.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/emergency/EmergencyTargetSelector.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
  {
    file: 'src/components/common/AuthorizationStaleNotice.vue',
    reason: 'not migrated yet: Chinese or wire-value copy that the convergence waves have not reached; listed so the gap cannot grow silently',
  },
];

describe('migration coverage', () => {
  it('accounts for every file that still shows copy', () => {
    const known = new Set([
      ...MIGRATED.map((entry) => entry.file),
      ...NOT_YET_MIGRATED.map((entry) => entry.file),
    ]);
    const unaccounted = sourceFiles()
      .filter((file) => bareCopy(readFileSync(file, 'utf8')).length > 0)
      .map((file) => file.slice(webRoot.length + 1))
      .filter((file) => !known.has(file))
      .sort();

    expect(unaccounted, 'add these to MIGRATED (converged) or NOT_YET_MIGRATED (with a reason)').toEqual([]);
  });

  it('has no stale not-yet-migrated entry', () => {
    const withCopy = new Set(
      sourceFiles()
        .filter((file) => bareCopy(readFileSync(file, 'utf8')).length > 0)
        .map((file) => file.slice(webRoot.length + 1)),
    );
    const stale = NOT_YET_MIGRATED.map((entry) => entry.file).filter((file) => !withCopy.has(file));

    expect(stale, 'these files no longer show copy: move them out of the registry').toEqual([]);
  });
});

describe('migrated files carry no unrecorded copy', () => {
  it.each(MIGRATED)('$file only shows what its allowlist records', ({ file, allowed }) => {
    const literals = bareCopy(readFileSync(resolve(webRoot, file), 'utf8'));
    const extra = literals.filter((literal) => !allowed.includes(literal));

    expect(extra, `${file} shows copy outside the catalog: ${extra.join(' | ')}`).toEqual([]);
  });

  it.each(MIGRATED)('$file has no stale allowlist entry', ({ file, allowed }) => {
    const literals = bareCopy(readFileSync(resolve(webRoot, file), 'utf8'));
    const stale = allowed.filter((literal) => !literals.includes(literal));

    expect(stale, `${file} allowlists copy that is no longer there: ${stale.join(' | ')}`).toEqual([]);
  });

  /*
   * The user chose Chinese as the console's single locale (2026-09-28), so the shared
   * catalog must stay Chinese-only. The rule catches English SENTENCES rather than any
   * ASCII word, because the domain vocabulary (Release, Bundle, Definition, CSO…) is
   * deliberately English in this project's copy. Three properties keep it honest:
   *  - EVERY entry is parsed, whatever its indentation, and the count must match the
   *    catalog's own key list (a re-indented entry cannot escape the check);
   *  - exemptions are bidirectional: a violation must be exempted AND an exemption must
   *    still be a violation, so the list cannot rot and an empty list is not vacuous;
   *  - the rule is stated once, so the failing message names the offending value.
   */
  /*
   * The project's own vocabulary keeps its English nouns inside Chinese copy (CONTEXT /
   * GLOSSARY: Release, Definition, Bundle, ValuesRevision, Operation, Emergency, Cluster,
   * Customer, Operator, Trust Root…). Blanking those terms before the sentence check lets
   * the copy read naturally without weakening the rule: `无效的 Reason code` is still
   * flagged, because "Reason" is not part of the domain vocabulary.
   */
  const DOMAIN_TERMS = [
    'Release', 'Definition', 'Bundle', 'ValuesRevision', 'Values', 'Revision', 'Mapping',
    'Promotion', 'Namespace', 'Operation', 'Emergency', 'Cluster', 'Customer', 'Operator',
    'Trust', 'Root',
    // Product terms kept in English inside Chinese copy (they name a feature, not prose):
    'Pull-through', 'cache', 'Operator', 'Helm', 'Kubernetes',
    // Proto/domain vocabulary that appears inside Chinese sentences on purpose:
    'Emergency', 'Unresolved', 'Effect', 'Result', 'Intent', 'APPLIED', 'NOT_APPLIED',
  ];
  function withoutDomainTerms(value: string): string {
    return DOMAIN_TERMS.reduce((acc, term) => acc.replaceAll(term, ' '), value);
  }
  const ENGLISH_SENTENCE = /[A-Za-z]{2,}\s+[A-Za-z]{2,}/;
  const SINGLE_LOCALE_EXEMPT = new Map<string, string>([
    // Placeholder examples for routing prefixes: a URL pattern, not copy to translate.
    ['cluster.rules.sourceExample', 'a URL example (docker.io/library/) shown as a placeholder'],
    ['cluster.rules.targetExample', 'a URL example (harbor.example.com/proxy/) shown as a placeholder'],
  ]);

  /** Violations ignoring the exemption map — the exemption check needs them raw. */
  function rawViolations(): string[] {
    return catalogValues()
      .filter(
        ({ value }) =>
          !/[\u4e00-\u9fff]/.test(value) ||
          (/[\u4e00-\u9fff]/.test(value) && ENGLISH_SENTENCE.test(withoutDomainTerms(value))),
      )
      .map((entry) => entry.key);
  }

  function catalogValues(): Array<{ key: string; value: string }> {
    const catalog = readFileSync(resolve(webRoot, 'src/i18n/messages.ts'), 'utf8');
    return [...catalog.matchAll(/^\s*'([^']+)':\s*'([^']*)',$/gm)].map((match) => ({
      key: match[1]!,
      value: match[2]!,
    }));
  }

  it('scans every catalog entry', () => {
    const values = catalogValues();

    expect(values.map((entry) => entry.key).sort()).toEqual([...messageKeys()].sort());
  });

  // Two orthogonal rules, both needed: "Audit" alone is not a sentence, and
  // "Access denied 审计" is not a missing script.
  it('keeps every catalog value in the chosen script', () => {
    const violations = catalogValues().filter(
      ({ key, value }) => !/[\u4e00-\u9fff]/.test(value) && !SINGLE_LOCALE_EXEMPT.has(key),
    );

    expect(
      violations.map(({ key, value }) => `${key} = ${value}`),
      'the console converges on Chinese; these values contain no Chinese at all',
    ).toEqual([]);
  });

  it('keeps every catalog value free of English sentences', () => {
    const violations = catalogValues().filter(
      ({ key, value }) =>
        !SINGLE_LOCALE_EXEMPT.has(key) &&
        /[\u4e00-\u9fff]/.test(value) &&
        ENGLISH_SENTENCE.test(withoutDomainTerms(value)),
    );

    expect(
      violations.map(({ key, value }) => `${key} = ${value}`),
      'the console converges on Chinese; these values mix an English sentence into Chinese copy',
    ).toEqual([]);
  });

  it('keeps exempt keys necessary and sufficient', () => {
    const violations = new Set(rawViolations());
    const exempt = [...SINGLE_LOCALE_EXEMPT.keys()];

    // Every exemption must still be a violation (no rotted exemption)…
    expect(exempt.filter((key) => !violations.has(key))).toEqual([]);
    // …and every violation must be listed (no unrecorded exemption).
    expect([...violations].filter((key) => !SINGLE_LOCALE_EXEMPT.has(key))).toEqual([]);
  });

  /*
   * Vue 3 removed interpolation inside attributes. Writing
   * `message="{{ t('x') }}"` compiles to the literal string and renders the braces to
   * the user — which is exactly what this migration shipped in five files before the
   * independent review caught it in the browser.
   */
  it('never interpolates inside an attribute', () => {
    const offenders: string[] = [];
    for (const file of sourceFiles().filter((candidate) => candidate.endsWith('.vue'))) {
      const text = readFileSync(file, 'utf8');
      if (/\s[\w-]+\s*=\s*["']\{\{/.test(text)) offenders.push(file);
    }

    expect(offenders, `Vue 3 cannot interpolate in attributes: ${offenders.join(', ')}`).toEqual([]);
  });

  /*
   * The console's own bundle can fail to load, and index.html carries the only UI that
   * still works in that case. The gate used to glob src/** only, so those strings were
   * permanently invisible.
   */
  it('keeps the inline boot guard in the chosen locale', () => {
    const html = readFileSync(resolve(webRoot, 'index.html'), 'utf8');
    const quoted = [...html.matchAll(/'([^'\n]{4,})'/g)].map((match) => match[1]!);
    const english = quoted.filter((value) => /[A-Za-z]{2,}\s+[A-Za-z]{2,}/.test(value));

    expect(english, `index.html shows English while the rest of the console is Chinese: ${english.join(' | ')}`).toEqual([]);
  });

  it('keeps the message file free of literal copy itself', () => {
    const text = readFileSync(messageFile, 'utf8');

    // The catalog is the one place copy is allowed; it must not also contain a
    // second, inline copy layer.
    expect(text).not.toContain('$t(');
  });
});
