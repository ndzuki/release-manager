/*
 * Single-locale message catalog (UX plan §11 W5 / decision G5).
 *
 * G5 decided: do NOT adopt an i18n framework this wave, converge on ONE locale,
 * centralise the copy, and leave the seam for a framework behind a tiny API.
 * The user chose the locale on 2026-09-28: **Chinese**. Two gates enforce it here —
 * every value must contain Chinese, and none may mix in an English sentence.
 *
 * Copy policy (TASK-394, the "mature, quiet, clean" pass the user asked for):
 *  - the console speaks Chinese. Server enums, role identifiers, internal decision
 *    numbers and implementation details never appear in visible copy;
 *  - a closed set of PRODUCT nouns stays English inside a Chinese sentence, because
 *    they name a product concept rather than prose: Release, Values, Values 版本,
 *    Bundle, Chart, Operator, Secret, Trust Root, plus technical format names
 *    (YAML, JSON, PEM, SBOM, TTL, DNS);
 *  - everything else is Chinese: Revision → 版本, Operation → 操作,
 *    Definition → 发布定义, Workload → 工作负载, Namespace → 命名空间,
 *    Draft → 草稿, Patch → 补丁, platform_admin → 平台管理员, viewer → 只读,
 *    Canonical → 规范化内容, Promotion Mapping → 提升映射;
 *  - one concept gets exactly one wording; the same term must not be English on one
 *    screen and Chinese on another;
 *  - copy never carries a decision number (D-xx) or a permission action
 *    (`release/write`, `cleanup/write`); those belong in the docs.
 *
 * This module is the seam:
 *  - messages live in one table (`zhCN`) instead of being scattered across 70 files;
 *  - `MessageKey` is derived from the table, so `t('typo.key')` is a COMPILE error;
 *  - interpolation is `{name}`, and a missing key is loud (never a silent empty
 *    string): it warns once in dev and renders the key itself elsewhere.
 *
 * The English that still remains lives in the pages the convergence waves have not
 * reached; moving it here is a mechanical pass over one table rather than a hunt
 * through the UI.
 */
export type MessageParams = Record<string, string | number>;

const zhCN = {
  // Shared states
  // Shared chrome (Chinese since the 2026-09-28 locale decision)
  'shell.nav.label': '主导航',
  /* The ≤1024px drawer control. `aria-expanded` carries open/closed, so the name states
     the destination ("navigation menu") rather than repeating the state. */
  'shell.nav.menu': '导航菜单',
  'shell.organization.choose': '组织',
  'shell.organization.switchError': '切换组织失败，请重试',
  'shell.signOut': '退出登录',
  /*
   * Direction C sidebar (TASK-394). The three group headings keep the 14 entries in the
   * 212px rail scannable; they are new copy, so they go through the catalog rather than
   * into the component (the older navigation labels are inline and already registered).
   */
  'shell.nav.group.release': '发布与运维',
  'shell.nav.group.assets': '资产与审计',
  'shell.nav.group.governance': '治理与账号',
  /*
   * The theme control cycles system → light → dark. The accessible name states the
   * CURRENT preference, because a bare "toggle theme" does not tell a screen-reader user
   * what the button will do next.
   */
  'shell.theme.toggle': '切换主题（当前：{theme}）',
  'shell.theme.system': '跟随系统',
  'shell.theme.light': '浅色',
  'shell.theme.dark': '深色',
  'nav.customers': '客户',
  'nav.clusters': '集群',
  'nav.audit': '审计',
  // Front door
  'app.restoringSession': '正在恢复会话…',
  'account.security': '账号安全',
  'governance.eyebrow': '治理',
  'governance.sourcePolicy': '来源 / 策略',
  'release.status.outOfSync': '不同步',
  'release.status.missing': '缺失',
  'release.status.active': '生效中',
  'release.status.unknown': '未知',
  'release.status.unknownTooltip': '服务端返回了控制台无法识别的状态；请刷新或升级控制台',
  'operator.api.missingResponse': 'Operator 响应缺少必要字段。',
  'operator.api.requestFailed': 'Operator 请求失败。',
  'client.timeout': '请求超时；服务可能不可达',
  'client.rpcTimeout': '请求超时',
  'auth.permissionDenied': '你没有执行该操作的权限。',
  'cluster.form.fixFields': '保存前请修正高亮字段。',
  'operator.form.fixFields': '生成令牌前请修正高亮字段。',
  'login.title': '登录',
  'login.description': '使用由服务端管理的会话继续。',
  'login.username': '用户名',
  'login.password': '密码',
  'login.submit': '登录',
  'login.submitting': '登录中…',
  'login.sessionExpired': '会话已过期。请重新登录以返回上一个页面。',
  'login.failedTitle': '登录失败',
  'login.failedFallback': '无法登录。',
  // Customer + cluster wave (converged 2026-09-28; domain nouns stay English)
  'customer.list.title': '客户',
  'customer.list.subtitle': '管理当前组织下的客户租户。',
  'customer.list.create': '新建客户',
  'customer.list.createFirst': '新建第一个客户',
  'customer.list.empty': '暂无客户',
  'customer.list.emptyMessage': '当前组织下还没有客户租户。',
  'customer.list.version': '版本',
  'customer.list.created': '创建时间',
  'customer.list.view': '查看',
  'customer.list.edit': '编辑',
  'customer.detail.label': '客户',
  'customer.detail.back': '返回客户列表',
  'customer.detail.disable': '停用客户',
  'customer.detail.disabled': '该客户已停用：不可编辑或停用。',
  'customer.detail.disabledBanner': '已停用：仅保留只读历史。',
  'customer.detail.unknownStatus': '客户状态无法识别：为避免误改，表单已设为只读。',
  'customer.detail.subtitle': '发布管理的租户边界。',
  'customer.detail.details': '客户详情',
  'customer.detail.editTitle': '编辑客户',
  'customer.detail.readonly': '你的只读角色没有新建客户入口。',
  'customer.detail.stale': '该客户在你打开后已被修改。请刷新后重新保存。',
  'customer.detail.create': '新建客户',
  'customer.detail.save': '保存修改',
  'customer.detail.disableSuccess': '已停用客户 {name}：该客户及其集群不再接收新的发布。',
  'cluster.list.title': '集群',
  'cluster.list.subtitle': '管理该客户的发布目标与制品路由。',
  'cluster.list.create': '新建集群',
  'cluster.list.createFirst': '新建第一个集群',
  'cluster.list.empty': '该客户下还没有集群。',
  'state.loading.customer': '正在加载客户…',
  'state.loading.customers': '正在加载客户列表…',
  'state.loading.cluster': '正在加载集群…',
  'state.loading.clusters': '正在加载集群列表…',
  'state.loading.releases': '正在加载 Release 列表…',
  'cluster.notFound': '未找到该集群',
  'cluster.list.routingRules': '路由规则',
  'cluster.list.preview': '发布目标预览',
  'cluster.detail.label': '集群',
  'cluster.detail.back': '返回集群列表',
  'cluster.detail.releases': '发布清单',
  'release.inventory.breadcrumb': '发布清单',
  'release.inventory.eyebrow': '发布清单',
  'cluster.detail.operators': 'Operator 列表',
  'cluster.detail.version': '版本',
  'cluster.detail.status': '状态',
  'cluster.detail.routingRules': '路由规则',
  'cluster.detail.imageRoutes': '镜像路由',
  'cluster.detail.chartRoutes': 'Chart 路由',
  'cluster.detail.disable': '停用集群',
  'cluster.detail.activeTarget': '生效中的发布目标',
  'cluster.detail.inactiveTarget': '已停用：不可作为发布目标',
  'cluster.detail.disableConfirm': '停用该集群会把它移出发布目标并撤销其 Operator 的访问。继续？',
  'cluster.detail.disableTitle': '停用集群',
  'cluster.detail.disableAction': '确认停用',
  'cluster.detail.disableSuccess': '已停用集群 {name}：它已移出发布目标。',
  'cluster.edit.credentials': '镜像仓库凭据不会也不应被保存于此。',
  'cluster.edit.draftPreserved': '你的草稿已保留。刷新以与服务端版本比对。',
  'cluster.edit.retrySave': '重试保存',
  'cluster.edit.refreshVersion': '刷新服务端版本',
  'cluster.edit.name': '集群名称',
  'cluster.edit.newLabel': '新建集群',
  'cluster.edit.editLabel': '编辑集群',
  'cluster.edit.createTitle': '新建集群',
  'cluster.edit.save': '保存集群',
  'cluster.edit.saving': '保存中…',
  'cluster.edit.conflict': '数据已被其他用户修改。',
  'cluster.edit.enabled': '作为发布目标启用',
  'cluster.edit.imageRoutes': '镜像路由',
  'cluster.edit.chartRoutes': 'Chart 路由',
  'action.cancel': '取消',
  'action.refresh': '刷新',
  'action.retry': '重试',
  'action.dismiss': '不再提示',
  'action.close': '关闭',
  // Status labels (the store's values stay English; only the display label is Chinese)
  'status.active': '生效中',
  'status.disabled': '已停用',
  'status.unknown': '未知',
  // Wave 3: customer/cluster/release-inventory sub-components
  'customer.form.name': '名称',
  'customer.form.slug': '标识',
  'customer.form.readonly': '只读访问：客户字段不可修改。',
  'customer.form.saving': '保存中…',
  'customer.form.save': '保存客户',
  'customer.history.title': '历史',
  'customer.history.empty': '暂无历史',
  'customer.history.loading': '正在加载客户历史…',
  'customer.history.noEvents': '暂无客户生命周期事件。',
  'customer.history.scrollRegion': '客户历史（可滚动）',
  'common.table.scrollRegion': '数据表格（可横向滚动）',
  'customer.disable.cascadeTokens': '注册令牌将被撤销。',
  'customer.disable.cascadeCerts': 'Operator 证书将被撤销。',
  'customer.disable.cascadeSessions': '活跃的 Operator 会话将被关闭。',
  'customer.disable.acknowledge': '我明白上述级联影响。',
  'customer.disable.title': '停用客户？',
  'customer.disable.body': '该操作会级联影响客户生命周期，且无法在本页撤销。',
  'customer.disable.confirm': '确认停用',
  'customer.disable.inProgress': '停用中…',
  'cluster.target.select': '目标集群',
  'cluster.target.placeholder': '选择集群',
  'cluster.rules.add': '新增规则',
  'cluster.rules.empty': '未配置规则。',
  'cluster.rules.rule': '规则',
  'cluster.rules.remove': '移除',
  'cluster.rules.mode': '模式',
  'cluster.rules.modeHint': '透传缓存需要先通过能力测试。',
  'cluster.rules.provider': '提供商',
  'cluster.rules.sourcePrefix': '源前缀',
  'cluster.rules.targetPrefix': '目标前缀',
  'cluster.rules.providerPlaceholder': '可选的提供商名称',
  'cluster.rules.imageTitle': '容器镜像路由规则',
  'cluster.rules.chartTitle': 'Helm Chart 路由规则',
  'cluster.rules.pullThrough': '透传缓存',
  'cluster.rules.modeDirect': '直连',
  'cluster.rules.modeReplicated': '副本同步',
  'cluster.rules.capabilityRequired': '需先通过能力测试才能启用',
  'cluster.rules.sourceExample': 'docker.io/library/',
  'cluster.rules.targetExample': 'harbor.example.com/proxy/',
  'release.filter.allStatus': '全部状态',
  'release.table.valuesDigest': '配置摘要',
  'release.table.scrollRegion': '发布清单表格（可横向滚动）',
  'common.breadcrumb': '面包屑',
  /*
   * Direction C copy affordance (TASK-394, CopyableHash). The failure copy names the
   * manual fallback because the truncated value stays selectable on screen; it is
   * rendered visibly, not only announced, so a refused clipboard write cannot look like
   * success.
   */
  'common.copy': '复制',
  'common.copied': '已复制',
  'common.copyFailed': '复制失败，请手动选中复制',
  'common.copyFullValue': '复制完整值',
  /*
   * Accessible name for a form control that lives in a repeated table row: the field
   * name alone repeats once per row, so a screen-reader user tabbing through the row
   * editors cannot tell which row they are on. `{field}` is itself a catalog value.
   */
  'common.labelledRowField': '{field}（第 {index} 行）',
  /*
   * TASK-299: accessible name for the notification primitive's dismiss control. It
   * names the action (close this notification), not the notification's own text, so it
   * stays generic instead of interpolating the message body into it.
   */
  'common.dismissNotice': '关闭通知',
  /*
   * Shared pagination primitive (TASK-283). The wording of a pager is the same on
   * every list, so it lives here once instead of once per page; `pagination.label`
   * is the primitive's fallback landmark name, and a page with a domain name for
   * its region (Operation 分页) passes its own `label`.
   */
  'pagination.label': '分页',
  'pagination.previous': '上一页',
  'pagination.next': '下一页',
  'pagination.page': '第 {page} 页',
  // Called with a source that also reports its size (the audit list pages a known
  // total). The page index alone cannot say how much data is behind it.
  'pagination.pageOfTotal': '第 {page} 页，共 {total} 条',
  // Wave 4: operator management
  'operator.token.oneTimeSecret': '一次性密钥',
  'operator.token.closeWarning': '关闭该对话框会从浏览器中永久移除明文。',
  'operator.token.copy': '复制令牌',
  /*
   * UX-009: the clipboard write can fail (an insecure origin has no
   * `navigator.clipboard`, a secure one can still reject while the document is
   * unfocused or the permission is denied). The failure copy names the manual fallback
   * because the plaintext stays selectable on screen.
   */
  'operator.token.copied': '已复制到剪贴板。',
  'operator.token.copyFailed': '复制失败：浏览器未允许写入剪贴板，请手动选中并复制。',
  'operator.token.deploymentCommand': '部署命令',
  'operator.token.template': '模板 ·',
  /*
   * TASK-389: the install command is only executable once the console substitutes the
   * gateway address the control plane advertises (`gateway.advertise_url`). The label
   * names that fact in the dialog, and the warning is the fail-closed branch: an endpoint
   * that is empty OR known-unreachable leaves the placeholder in the command, so the copy
   * must say why instead of letting the customer discover it when the operator fails to
   * connect. Review block: until a separate change teaches the server to advertise a
   * customer-reachable address, `operator_endpoint` is still the dev fixture `.local` name
   * or a control-plane Service DNS name, and a plaintext `http` URL would send the
   * enrollment token unencrypted — all of those fail closed, not just the empty value.
   */
  'operator.token.gatewayAddress': '控制面网关地址',
  'operator.token.gatewayMissing': '未配置',
  'operator.token.gatewayMissingWarning': '未配置控制面网关地址：安装命令中的 <CONTROL_PLANE_OPERATOR_GATEWAY> 未被替换，复制后请先填入该集群可访问的网关地址再执行。',
  'operator.token.gatewayUnreachableWarning': '控制面网关地址不可达（{reason}）：安装命令中的 <CONTROL_PLANE_OPERATOR_GATEWAY> 未被替换，复制后请先换成客户集群能连到的 TLS 地址再执行。',
  'operator.token.gatewayReasonDevLocal': '开发夹具域名 .local 只在夹具网络内解析',
  'operator.token.gatewayReasonClusterDns': '集群内服务 DNS 只在控制面集群内可解析',
  'operator.token.gatewayReasonPlaintext': 'http 地址会让注册令牌明文传输，客户网关必须使用 TLS',
  'operator.token.gatewayReasonInvalid': '不是有效的 URL',
  'operator.token.gatewayRemediation': '把 <CONTROL_PLANE_OPERATOR_GATEWAY> 换成客户集群能连到的 TLS 地址',
  'operator.token.copyCommand': '复制命令',
  'operator.token.saved': '我已把令牌保存到受认可的密钥存储中。',
  'operator.token.closeAndForget': '关闭并忘记令牌',
  'operator.token.discardAck': '我明白丢弃会撤销该待用令牌。',
  'operator.token.retryGeneration': '重试生成',
  'operator.token.generating': '正在生成一次性令牌…',
  'operator.token.discarding': '丢弃中…',
  'operator.token.discard': '丢弃令牌',
  'operator.token.noToken': '未返回令牌。',
  'operator.token.title': '立即保存注册令牌',
  'operator.token.failedTitle': '令牌生成未完成',
  'operator.filters.title': '筛选 Operator 历史',
  'operator.filters.lifecycle': '生命周期',
  'operator.filters.session': '会话',
  'operator.filters.all': '全部',
  'operator.filters.noSession': '无会话',
  'operator.lifecycle.active': '在用',
  'operator.lifecycle.superseded': '已被取代',
  'operator.lifecycle.revoked': '已撤销',
  'operator.session.online': '在线',
  'operator.session.suspect': '可疑',
  'operator.session.offline': '离线',
  'operator.table.caption': 'Operator 列表',
  'operator.table.name': '名称',
  'operator.table.lastHeartbeat': '最近心跳',
  'operator.table.registered': '注册时间',
  'operator.table.actions': '操作',
  'operator.table.revoke': '撤销',
  'operator.pending.title': '待用的注册令牌',
  'operator.pending.waiting': '有令牌等待使用',
  'operator.pending.expires': '过期时间',
  'operator.pending.createdBy': '创建者',
  'operator.pending.replace': '替换令牌',
  'operator.pending.revoke': '撤销待用令牌',
  'operator.revoke.reason': '原因',
  'operator.revoke.warning': '该操作会立即撤销活跃会话且不可撤销。',
  'operator.revoke.inProgress': '撤销中…',
  'operator.revoke.confirm': '确认撤销',
  'operator.list.title': '集群 Operator',
  'operator.list.subtitle': 'Operator 注册历史',
  'operator.list.description': '查看该集群的身份生命周期与服务端会话状态。',
  'operator.list.generate': '生成令牌',
  'operator.list.generateFirst': '生成第一个令牌',
  'operator.list.stale': '既有数据仍可见；请用「刷新」重试。',
  'operator.list.empty': '暂无已注册的 Operator',
  'operator.list.loading': '正在加载 Operator…',
  'operator.list.emptyHint': '生成注册令牌以为该集群注册第一个 Operator。',
  'operator.list.loadMore': '加载更多',
  'operator.reason.noSession': '还没有会话连接过。',
  'operator.reason.heartbeatTimeout': '心跳超时。',
  'operator.reason.heartbeatDelayed': '心跳延迟。',
  'operator.reason.certificateRevoked': '该 Operator 证书已被撤销。',
  'operator.reason.superseded': '更新的 Operator 身份已取代此身份。',
  'operator.reason.sessionReplaced': '更新的会话取代了该连接。',
  'operator.reason.unknown': '服务端上报了未知的状态原因。',
  'operator.reason.unknownWith': '服务端上报了未知的状态原因（{reason}）。',
  'operator.time.never': '从未',
  'operator.validation.name': 'Operator 名称必须是 1–63 个字符的小写 DNS 兼容标签。',
  'operator.validation.ttl': '有效期必须为 0，或介于 5 与 1440 分钟之间。',
  'operator.validation.reason': '撤销原因必须为 5–500 个字符。',
  'operator.status.unknown': '未知',
  // TASK-330 (AC-330-03): the notice reports the automatic chain, not data freshness.
  // A successful out-of-band refresh (the page's own refresh button) clears the page's
  // `role="alert"` stale paragraph but deliberately leaves the chain stopped, so a
  // "the data may be stale" claim here would outlive the fact that justifies it.
  'operator.polling.stopped': '自动刷新已停止。',
  'operator.polling.resume': '恢复自动刷新',
  // Wave 5: operator detail/enrollment + audit
  'operator.enroll.eyebrow': 'Operator 注册',
  'operator.enroll.title': '生成一次性令牌',
  'operator.enroll.note': '明文只在接下来的弹窗中显示，关闭后无法找回。',
  'operator.enroll.back': '返回 Operator 列表',
  'operator.enroll.parameters': '注册参数',
  'operator.enroll.name': 'Operator 名称',
  'operator.enroll.nameHint': '小写 DNS 兼容标签，1–63 个字符。',
  'operator.enroll.ttl': '有效期（分钟）',
  'operator.enroll.ttlHint': '0 表示使用 60 分钟默认值；否则取 5–1440。',
  'operator.enroll.submit': '生成注册令牌',
  'operator.enroll.pendingBlocked': '请先撤销或显式替换待用令牌，再生成新的令牌。',
  'operator.enroll.checking': '正在检查待用令牌状态…',
  'operator.enroll.replaceConfirm': '替换待用令牌会立即撤销旧令牌。继续？',
  'operator.enroll.revokeConfirm': '撤销待用的注册令牌？该操作不可撤销。',
  'operator.enroll.replaceTitle': '替换待用令牌',
  'operator.enroll.replaceAction': '确认替换',
  'operator.enroll.revokeTitle': '撤销待用令牌',
  'operator.enroll.revokeAction': '确认撤销',
  'operator.detail.title': 'Operator 详情',
  'operator.detail.revokeOperator': '撤销 Operator',
  'operator.detail.stale': '既有数据仍可见；请用「刷新」重试。',
  'operator.detail.status': '状态',
  'operator.detail.identity': '身份与运行时',
  'operator.detail.customer': '客户',
  'operator.detail.cluster': '集群',
  'operator.detail.instance': '实例',
  'operator.detail.version': '版本',
  'operator.detail.supersededBy': '被取代者',
  'operator.detail.revokedAt': '撤销时间',
  'operator.detail.capabilities': '能力',
  'operator.detail.revocation': '撤销记录',
  'operator.detail.loading': '正在加载 Operator…',
  'operator.detail.notFound': '未找到该 Operator',
  'operator.detail.loadFailed': '无法加载 Operator',
  'operator.detail.notReported': '未上报',
  'operator.revoke.success': '已撤销 Operator {name}：其证书与会话不再被接受。',
  'audit.filters.title': '审计筛选',
  'audit.filters.organization': '组织范围',
  'audit.filters.allOrganizations': '全部组织（含系统事件）',
  'audit.filters.systemScope': '系统事件',
  'audit.filters.systemScopeHint': '平台级事件不属于任何租户，选择此项可单独查看。',
  'audit.filters.actor': '操作者',
  'audit.filters.actorPrivate': '私有筛选：不会写入 URL。',
  'audit.filters.resourceType': '资源类型',
  'audit.filters.allResources': '全部资源',
  'audit.filters.resourceId': '资源 ID',
  'audit.filters.action': '动作',
  'audit.filters.allActions': '全部动作',
  'audit.filters.status': '状态',
  'audit.filters.allStatuses': '全部状态',
  'audit.filters.from': '起始时间',
  'audit.filters.to': '结束时间',
  'audit.filters.windowHint': '当前角色允许的查询跨度最多 {days} 天。',
  'audit.filters.search': '查询',
  'audit.filters.reset': '重置',
  'audit.table.showing': '已显示事件数',
  'audit.table.time': '时间',
  'audit.table.actor': '操作者',
  'audit.table.resource': '资源',
  'audit.table.action': '动作',
  'audit.table.status': '状态',
  'audit.table.duration': '耗时',
  'audit.table.results': '审计结果',
  'audit.table.pagination': '审计分页',
  // The pager's own wording (上一页 / 下一页) now lives in `pagination.*`: the table
  // renders the shared Pagination primitive, which owns the controls and their copy
  // (TASK-300).
  'audit.table.unknownActor': '未知',
  'audit.actor.user': '用户',
  'audit.actor.service': '服务',
  'audit.actor.apiKey': 'API 密钥',
  'audit.actor.anonymous': '匿名',
  'audit.detail.title': '审计事件',
  'audit.detail.timestamp': '时间戳',
  'audit.detail.changeSummary': '变更摘要',
  'audit.detail.close': '关闭审计详情',
  'audit.export.title': '导出',
  'audit.export.note': '导出是异步任务；当前不提供进度查询，导出内容不会存到浏览器。',
  'audit.page.title': '组织审计轨迹',
  'audit.page.subtitle': '审计事件',
  'audit.page.description': '检索服务端已脱敏的事件：',
  'audit.page.empty': '暂无审计事件',
  'audit.page.loading': '正在加载审计事件…',
  'audit.page.emptyHint': '当前组织与筛选条件下没有匹配事件。',
  'audit.page.creatingExport': '正在创建导出…',
  'audit.page.export': '导出当前查询',
  'audit.page.exportScopeHint': '导出只支持单个组织：请先在上方选择一个具体组织。',
  'audit.page.narrowRange': '缩小时间范围',
  'audit.page.failedTitle': '审计请求失败',
  'audit.error.range': '请选择有效的起止时间。',
  'audit.error.narrowDays': '请把时间范围缩小到 {days} 天以内。',
  'audit.error.rangeTooLarge': '查询范围过大，请缩小到 {days} 天以内。',
  'audit.error.rejected': '查询被拒绝。',
  'audit.error.requestFailed': '审计请求失败。',
  'audit.error.rangeOrder': '起始时间不得晚于结束时间。',
  'audit.error.noAccess': '你没有访问该组织的权限。',
  'audit.error.expired': '该页已过期，已重新加载第一页。',
  'audit.error.exportUnavailable': '审计导出暂不可用，请稍后重试。',
  'audit.error.allOrganizationsExport': '导出只支持单个组织：请先选择具体组织再导出。',
  'audit.error.timeout': '请求超时，请重试。',
  'audit.error.service': '审计服务失败，请稍后重试。',
  // Wave 6: values, release definitions, first-time setup
  'values.secretRefs.title': '配置',
  'values.secretRefs.kind': 'Secret 引用',
  'values.secretRefs.name': 'Secret 名称',
  'values.secretRefs.key': '键',
  'values.secretRefs.path': 'Values 路径',
  'values.editor.aria': 'Values 文档编辑器',
  'values.editor.validationAria': 'Values 校验',
  'values.canonical.invalidYaml': 'YAML 语法错误',
  'bundle.api.missingDetail': '成功响应里缺少 Bundle 明细',
  'values.diff.title': '规范化内容差异',
  'values.diff.scope': '父级 → 当前',
  'values.diff.changes': '处变更',
  'values.diff.before': '变更前',
  'values.diff.after': '变更后',
  'values.diff.arrayChanged': '数组已变更',
  'values.diff.added': '新增',
  'values.diff.removed': '删除',
  'values.diff.modified': '修改',
  'values.skeleton.label': '正在加载 Values 编辑器',
  'values.revision.workflow': '版本流程',
  'values.revision.reject': '驳回',
  'values.draft.restored': '已恢复未保存的编辑',
  'values.draft.savedAt': '（保存于 {time}）',
  'values.draft.discard': '丢弃本地草稿',
  'values.draft.saveFailed': '本地草稿无法保存（浏览器存储已满或被禁用）；请及时保存草稿。',
  'values.revision.rejectTime': '驳回时间 ·',
  'values.revision.new': '新建版本',
  'values.revision.submit': '提交',
  'values.revision.submitting': '提交中…',
  'values.revision.discard': '丢弃',
  'values.revision.approve': '审批通过',
  'values.revision.status.draft': '草稿',
  'values.revision.status.pendingApproval': '待审批',
  'values.revision.status.approved': '已审批',
  'values.revision.status.rejected': '已驳回',
  'values.revision.status.superseded': '已被取代',
  'values.revision.status.discarded': '已丢弃',
  'values.revision.status.unknown': '未知',
  'values.page.title': 'Values 文档',
  'values.page.content': 'Values 内容',
  'values.page.language': '语言',
  'values.page.breadcrumb': '面包屑',
  'definitions.eyebrow': '发布定义',
  'definitions.promotionMapping': '提升映射',
  'definitions.span': '客户 / 集群',
  'definitions.namespaceRelease': '命名空间 / Release',
  
  'definitions.namespace': '命名空间',
  'definitions.releaseName': 'Release 名称',
  'definitions.chartName': 'Chart 名称',
  'definitions.workloadKind': '工作负载类型',
  'definitions.workloadName': '工作负载名称',
  'definitions.container': '容器',
  'definitions.field': '字段',
  'definitions.valuesPath': 'Values 路径',
  'definitions.customerId': '客户 ID',
  'definitions.clusterId': '集群 ID',
  'definitions.table.scrollRegion': '发布定义表格（可横向滚动）',
  'definitions.editorTitle': '编辑 {name}（版本 {version}）',
  // TASK-301: creating and disabling a definition had no console surface.
  'definitions.create.open': '新建定义',
  'definitions.create.title': '新建发布定义',
  'definitions.create.hint': '名称取自 Release 名称；同一客户、集群、命名空间与 Release 的组合不能重复。',
  'definitions.create.customerId': '客户 ID',
  'definitions.create.clusterId': '集群 ID',
  'definitions.create.namespace': '命名空间（可空）',
  'definitions.create.releaseName': 'Release 名称',
  'definitions.create.chartName': 'Chart 名称（可空）',
  'definitions.create.enabled': '创建后立即启用（不勾选则先建成草稿）',
  'definitions.create.required': '请填写客户 ID、集群 ID 与 Release 名称',
  'definitions.create.submit': '创建',
  'definitions.create.submitting': '创建中…',
  'definitions.create.success': '已创建定义 {name}（版本 {version}）',
  'definitions.update.success': '已更新定义 {name}（版本 {version}）',
  'definitions.disable.open': '禁用',
  'definitions.disable.title': '禁用发布定义',
  'definitions.disable.description': '确认后将停用定义 {name}：不再有新的操作可以指向它，既有历史与清单会保留。',
  'definitions.disable.confirm': '确认禁用',
  'definitions.disable.submitting': '禁用中…',
  'definitions.disable.success': '已停用定义 {name}',
  // TASK-301: organization-level create/disable (REQ-026); the member surface is A1.
  'organization.table.scrollRegion': '组织成员表格（可横向滚动）',
  'organization.create.open': '新建组织',
  'organization.create.title': '新建组织',
  'organization.create.name': '组织名称',
  'organization.create.hint': '创建者会被加入新组织并设为平台管理员；新组织可能需要重新登录后才会出现在组织切换器中。',
  'organization.create.submit': '创建',
  'organization.create.submitting': '创建中…',
  'organization.create.success': '已创建组织 {name}',
  'organization.disable.open': '禁用组织',
  'organization.disable.title': '禁用组织',
  'organization.disable.description': '确认后将停用组织 {name}：该组织的成员将无法再对其范围内的资源执行操作。停用是终态的，没有恢复流程。',
  'organization.disable.confirm': '确认停用',
  'organization.disable.submitting': '停用中…',
  'organization.disable.success': '已停用组织 {name}，该操作不可恢复；其成员（包括你）将无法再对该组织范围内的资源执行操作。',
  'organization.disable.unavailable': '当前没有可停用的组织（会话未返回活动组织）。',
  'organization.disable.alreadyDisabled': '该组织已停用，无法再次停用。',
  'init.eyebrow': '首次初始化',
  'init.title': '初始化 Release Manager',
  'init.description': '创建第一个平台管理员与组织。',
  'init.adminUsername': '管理员用户名',
  'init.organizationName': '组织名称',
  'init.password': '密码',
  'init.failed': '初始化失败',
  'init.inProgress': '正在初始化…',
  'init.submit': '初始化系统',
  'init.defaultOrganization': '默认组织',
  'init.failedFallback': '无法初始化系统。',
  // Wave 7: emergency change + convergence tasks
  'emergency.result.title': '紧急变更结果',
  'emergency.result.intent': '意图',
  'emergency.result.effectStatus': '效果状态',
  'emergency.result.before': '执行前快照',
  'emergency.result.after': '执行后快照',
  'emergency.result.unresolved': '操作已结束但集群效果尚未确认：目标锁会继续保留，页面会持续观察迟到的执行结果，直到确认为「已生效」或「未生效」。',
  'emergency.result.resolved': '效果已解析，观察已停止。',
  'emergency.tasks.caption': '待收敛任务',
  'emergency.tasks.target': '目标',
  'emergency.tasks.type': '类型',
  'emergency.tasks.reason': '原因',
  'emergency.tasks.promotionPaths': '提升路径',
  'emergency.tasks.status': '状态',
  'emergency.tasks.bound': '已绑定',
  'emergency.tasks.continue': '继续',
  'emergency.page.title': '紧急变更',
  'notFound.titleText': '页面不存在',
  'notFound.body': '你访问的页面不存在。',
  'notFound.home': '返回首页',
  'emergency.page.blockedTitle': '存在进行中的标准操作',
  'emergency.page.blockedBody': '发布定义存在进行中的操作，紧急变更入口已阻断：',
  'emergency.page.viewOperation': '查看操作',
  'convergence.page.title': '收敛任务',
  'convergence.page.pending': '待收敛任务',
  'convergence.page.prepare': '准备收敛',
  'convergence.page.empty': '没有待收敛任务。',
  'annotation.value': '值',
  'annotation.add': '添加注解',
  'annotation.keyWhitelist': '注解键（白名单）',
  'emergency.tasks.pendingPromotion': '待提升',
  'annotation.scope': '作用域',
  // Emergency action selector + replicas/annotations payload (REQ-081, REQ-058)
  'emergency.action.label': '变更动作',
  'emergency.action.image': '镜像变更',
  'emergency.action.replicas': '副本数变更',
  'emergency.action.annotations': '注解变更',
  'emergency.action.unavailable.image': '镜像变更不可用：平台尚未采集到该工作负载的容器信息。',
  'emergency.action.unavailable.replicas': '副本数变更不可用：平台尚未采集到该工作负载的副本数。',
  'emergency.action.unavailable.annotations.notObserved': '注解变更不可用：尚未观测到该工作负载上的已批准注解。',
  'emergency.action.unavailable.annotations.unsupported': '注解变更不可用：该工作负载的 Operator 不支持设置已批准注解。',
  'emergency.replicas.label': '目标副本数',
  'emergency.replicas.help': '范围 {min}–{max}；0 表示不修改副本数。',
  'emergency.replicas.current': '当前副本数 {current}，紧急变更上限 {max}。',
  // The observed image of the selected container (imageActions[].currentImageRef).
  // `currentUnknown` is the fail-closed branch: an empty ref means the operator
  // has not reported that container, so the UI says so instead of inventing one.
  'emergency.image.current': '当前镜像：{ref}',
  'emergency.image.currentUnknown': '当前镜像：未观测',
  'emergency.replicas.unavailable': '当前目标不可变更副本数。',
  'emergency.replicas.unavailable.hpa': '副本数由自动扩缩容管理，不可修改。',
  'annotation.hint': '只可填写该发布定义白名单内的注解键，且必须为同一作用域。',
  'annotation.key.observed': '{key}（已观测）',
  'annotation.key.notObserved': '{key}（已批准，未观测）',
  'annotation.whitelist.violation': '注解白名单读取异常：服务端返回的内容无法解码，这里只列出已观测到的注解键。',
  'annotation.whitelist.unavailable': '注解白名单暂不可用：未能读取该发布定义，这里只列出已观测到的注解键。',
  'annotation.valuePlaceholder': '1–2048 字节',
  'annotation.remove': '移除',
  'annotation.removeRow': '移除注解 {key}',
  // Server-owned status display names (see i18n/status-labels.ts)
  'status.operation.pending': '等待中',
  'status.operation.preflight': '预检',
  'status.operation.queued': '排队中',
  'status.operation.running': '执行中',
  'status.operation.cancelling': '取消中',
  'status.operation.succeeded': '成功',
  'status.operation.failed': '失败',
  'status.operation.cancelled': '已取消',
  'status.operation.timeout': '超时',
  'status.operationType.install': '安装',
  'status.operationType.upgrade': '升级',
  'status.operationType.rollback': '回滚',
  'status.effect.unknown': '未知',
  'status.effect.applied': '已生效',
  'status.effect.notApplied': '未生效',
  'status.effect.notStarted': '未开始',
  'status.definition.draft': '草稿',
  'status.definition.active': '生效中',
  'status.definition.disabled': '已停用',
  'status.convergence.pendingPromotion': '待提升',
  'status.convergence.converged': '已收敛',
  'status.audit.success': '成功',
  'status.audit.succeeded': '成功',
  'status.audit.failed': '失败',
  'status.audit.accepted': '已受理',
  // Audit event action + resource type (web/src/stores/audit.ts actionOptions /
  // resourceTypeOptions). Values stay the wire identifiers because they are filters.
  'status.auditAction.create': '创建',
  'status.auditAction.update': '更新',
  'status.auditAction.delete': '删除',
  'status.auditAction.approve': '审批通过',
  'status.auditAction.reject': '驳回',
  'status.auditAction.install': '安装',
  'status.auditAction.upgrade': '升级',
  'status.auditAction.rollback': '回滚',
  'status.auditAction.emergency_change': '紧急变更',
  'status.auditAction.export.created': '导出创建',
  'status.auditAction.verify_trust': '信任验证',
  'status.auditAction.login': '登录',
  'status.auditAction.logout': '退出登录',
  'status.auditResource.customer': '客户',
  'status.auditResource.cluster': '集群',
  'status.auditResource.release': '发布',
  'status.auditResource.operation': '操作',
  'status.auditResource.release_bundle': '发布 Bundle',
  'status.auditResource.trust_root': '信任根',
  'status.auditResource.audit_export': '审计导出',
  'status.auditResource.organization': '组织',
  'status.auditAction.approved': '已批准',
  'status.auditAction.rejected': '已驳回',
  'status.auditAction.submitted': '已提交',
  'status.auditAction.discarded': '已丢弃',
  'status.auditAction.superseded': '已被取代',
  'status.auditAction.enrolled': '已注册',
  'status.auditAction.read': '读取',
  'status.auditAction.write': '写入',
  'status.auditAction.query': '查询',
  'status.auditAction.submit': '提交',
  'status.auditAction.close': '关闭',
  'status.auditAction.seed': '初始化数据',
  'status.auditAction.cleanup.gc': '清理任务',
  'status.auditAction.operator.revoked': '撤销 Operator',
  'status.auditAction.operator.revoked.again': '重复撤销 Operator',
  'status.auditAction.operator.enrollment_token.created': '创建注册令牌',
  'status.auditAction.operator.enrollment_token.replaced': '替换注册令牌',
  'status.auditAction.operator.enrollment_token.revoked': '作废注册令牌',
  'status.auditAction.release.emergency.execute': '执行紧急变更',
  'status.auditAction.release.emergency.resolve': '解决紧急锁',
  'status.auditAction.release.operation.create': '创建发布操作',
  'status.auditAction.release.values.approve': '审批 Values 版本',
  'status.auditAction.release.values.create': '创建 Values 版本',
  'status.auditAction.set_approved_annotation': '设置批准注解',
  'status.auditAction.set_container_image': '设置容器镜像',
  'status.auditAction.set_image': '设置镜像',
  'status.auditAction.set_replicas': '设置副本数',
  'status.auditResource.api_service': 'API 服务',
  'status.auditResource.audit_outbox': '审计发件箱',
  'status.auditResource.enrollment_token': '注册令牌',
  'status.auditResource.gc_cycle': '清理周期',
  'status.auditResource.migration': '迁移',
  'status.auditResource.operator': 'Operator 身份',
  'status.auditResource.release_definition': '发布定义',
  'status.auditResource.release_operation': '发布操作',
  'status.auditResource.values_revision': 'Values 版本',
  'status.export.pending': '等待中',
  'status.export.running': '生成中',
  'status.export.succeeded': '已完成',
  'status.export.failed': '已失败',
  'status.trustRoot.pending': '待生效',
  'status.trustRoot.active': '生效中',
  'status.trustRoot.grace': '宽限期',
  'status.trustRoot.retired': '已退役',
  'status.trustRoot.revoked': '已吊销',
  'status.operationType.emergency': '紧急变更',
  // api/proto/common/v1/domain.proto: BundleStatus (lowercased on the wire view).
  'status.bundle.received': '已接收',
  'status.bundle.validated': '已验证',
  'status.bundle.rejected': '已拒绝',
  'status.bundle.archived': '已归档',
  'status.bundle.unspecified': '未指定',
  'status.convergenceStrategy.requirePromotion': '生成收敛任务，需异人审批',
  'status.convergenceStrategy.revertOnNextReconcile': '下次对账时回退',
  'status.convergenceStrategy.unspecified': '未指定',
  'status.emergencyOperation.setContainerImage': '镜像变更',
  'status.emergencyOperation.setReplicas': '副本变更',
  'status.emergencyOperation.setAnnotation': '注解变更',
  'status.emergencyOperation.revert': '回退',
  'status.stage.passed': '通过',
  'status.stage.failed': '失败',
  'status.stage.skipped': '跳过',
  'status.stage.timeout': '超时',
  'status.stage.cancelled': '已取消',
  // internal/orchestrator/preflight/stage.go: StageName
  'status.preflightStage.artifact': '制品检查',
  'status.preflightStage.render': '渲染检查',
  'status.preflightStage.cluster': '集群检查',
  'status.preflightStage.runtime_pull': '镜像拉取',
  // Wave 8: operation creation/detail components + remaining emergency components
  'operation.confirm.title': '最终确认',
  'operation.confirm.submit': '确认创建操作',
  'operation.confirm.note': '提交后将立即进入预检。',
  'operation.confirm.release': 'Release 名称',
  'operation.confirm.cluster': '集群',
  'operation.confirm.artifact': '制品',
  'operation.confirm.version': '配置版本',
  'operation.confirm.patch': '补丁覆盖',
  'operation.confirm.noPatch': '无',
  'operation.confirm.currentRevision': '当前版本',
  'operation.form.type': '操作类型',
  'operation.form.bundle': '制品 Bundle',
  'operation.form.selectArtifact': '请选择制品',
  'operation.form.approvedRevision': '已审批的 Values 版本',
  'operation.form.selectRevision': '请选择已审批的 Values 版本',
  'operation.form.revisionLoading': '正在加载已审批的 Values 版本…',
  'operation.form.revisionEmpty': '该 Release 还没有已审批的 Values 版本：请先在配置中心提交并完成审批。',
  'operation.form.revisionLoadFailed': '已审批配置版本加载失败，请刷新后重试。',
  'operation.form.currentRevision': '当前版本',
  'operation.form.targetRevision': '回滚目标版本',
  'operation.form.checkAndConfirm': '检查并确认',
  'operation.patch.title': '补丁覆盖',
  'operation.patch.help': '只允许点分路径（如 spec.replicas）。Secret 类字段必须使用 Secret 引用，不保存明文。',
  'operation.patch.pathAria': '补丁 {index} 路径',
  'operation.patch.kindAria': '补丁 {index} 类型',
  'operation.patch.valueAria': '补丁 {index} 值',
  'operation.patch.secretPlaceholder': 'Secret 名称',
  'operation.patch.valuePlaceholder': '值',
  'operation.patch.literal': '字面量',
  'operation.patch.secretRef': 'Secret 引用',
  'operation.patch.add': '添加补丁',
  'operation.preflight.title': '预检结果',
  'operation.preflight.unknown': '状态未知（{reason}）',
  'operation.preflight.notReturned': '未返回状态',
  'operation.preflight.loadFailed': '预检结果加载失败',
  'operation.preflight.loadFailedHint': '未能读取预检结果；可重试。',
  'operation.preflight.rawJson': '查看原始响应',
  'operation.timeline.errorCode': '错误码：',
  'operation.timeline.ack': '集群已确认写入',
  'operation.timeline.workload': '未上报的工作负载',
  'operation.timeline.effectConfirmed': '紧急变更生效结果已确认',
  'operation.timeline.ackStage.persisted': '已写入磁盘',
  'operation.timeline.ackWithStage': '{base}（{stage}）',
  'operation.timeline.ackStage.unknown': '写入阶段',
  'operation.timeline.unknownState': '未知状态',
  'stuckLocks.mode.notAppliedHint': '能证明命令从未生效，例如操作从未确认写入且会话已离线。',
  'stuckLocks.mode.overrideHint': '无法证明未生效但必须接管目标；效果会保留为未知，释放全程审计，迟到的结果仍可能解析效果。',
  'operation.timeline.scrollRegion': '状态时间线（可滚动）',
  'convergence.card.title': '收敛任务（需异人审批提升）',
  'convergence.card.awaitingCreate': '结果已生效，等待创建收敛任务。',
  'convergence.card.notConfirmed': '结果未确认生效前不会创建收敛任务。',
  'convergence.card.create': '创建收敛 Values 版本',
  'convergence.card.revertPolicy': '回退策略（下次对账时回退）',
  'convergence.card.revertNote': '该策略不创建收敛任务，也不阻断标准发布。',
  'convergence.card.reconciled': '已对账（操作 {operationId}）',
  'convergence.card.awaitingStandardRelease': '等待标准发布对账',
  'convergence.card.awaitingRevert': '等待回退对账',
  'emergency.confirm.target': '目标',
  'emergency.confirm.container': '容器',
  'emergency.confirm.artifact': '制品',
  'emergency.confirm.action': '变更动作',
  'emergency.confirm.action.replicas': '副本数 → {replicas}',
  'emergency.confirm.action.annotations': '注解 → {entries}',
  'emergency.confirm.policy': '收敛策略',
  'emergency.confirm.reason': '原因',
  'emergency.confirm.warning': '确认后系统将立即投递变更命令（可能影响线上服务），且此确认不替代收敛审批。',
  'emergency.confirm.ack': '我已确认变更内容与风险',
  'emergency.confirm.cancel': '取消',
  'emergency.confirm.submit': '确认紧急变更',
  // Wave 9: the last English surfaces (startup, validation, page eyebrows/headers)
  'bootstrap.unknownError': '未知的启动错误。',
  'bootstrap.unreachable': '无法连接发布管理服务',
  'bootstrap.failed': '控制台无法完成启动。请确认服务可达后重试。',
  'bootstrap.timeout': '启动在 {timeoutMs} 毫秒内未完成',
  'cluster.validation.nameRequired': '集群名称必填',
  'cluster.validation.nameTooLong': '集群名称不得超过 253 个字符',
  'cluster.validation.capability': 'Chart 透传缓存尚未通过能力测试',
  'cluster.validation.unsupportedMode': '不支持的镜像路由模式',
  'cluster.validation.sourcePrefix': '源前缀不合法',
  'cluster.validation.targetPrefix': '目标前缀不合法',
  'cluster.validation.sourceConflict': '源前缀与其它规则冲突',
  'cluster.api.missingResponse': '集群响应缺少必要字段。',
  'customer.api.connectFailed': '无法连接服务器，草稿已保留。',
  'customer.api.missingResponse': '客户响应缺少必要字段。',
  'customer.api.requestFailed': '请求失败',
  'cluster.api.connectFailed': '无法连接服务器，草稿已保留。',
  'cluster.api.saveFailed': '保存失败',
  'artifact.eyebrow': '维护',
  'artifact.bundleIdRequired': '请填写 Bundle ID',
  'bundle.detail.gitCommit': 'Git 提交',
  'bundle.detail.pipeline': '流水线',
  'bundle.detail.sbom': 'SBOM 引用',
  'bundle.detail.bundleId': 'Bundle 标识',
  'bundle.eyebrow': '发布',
  'definitions.releaseDefinitionIdRequired': '发布定义 ID（必填）',
  'definitions.chartNameOptional': 'Chart 名称（可空）',
  'stuck.eyebrow': '紧急',
  // Stuck-lock confirmation (TASK-296). The warning is a catalog entry so the
  // hand-rolled dialog could drop its inline copy: AppDialog renders it as
  // `description` and therefore also wires aria-describedby to the dialog.
  'stuck.dialog.title': '释放锁 {path}',
  'stuck.dialog.warning': '释放后不可撤销，且无法再次释放。请确认集群效果确实可以接管。',
  'trust.eyebrow': '治理',
  'trust.keyId': '密钥 ID',
  'trust.issuerSubject': '签发者 / 主体',
  'trust.revocationEpoch': '· 吊销代数',
  // Writes: create + rotate (TASK-280 D11). Rotation takes only PUBLIC key material;
  // the signing private key never enters the browser or the wire.
  'trust.create.entry': '新建信任根',
  'trust.rotate.entry': '轮换',
  'trust.create.title': '新建信任根',
  'trust.rotate.title': '轮换信任根 {keyId}',
  'trust.confirm.title': '确认吊销信任根 {keyId}？',
  'trust.editor.keyId': '密钥 ID',
  'trust.editor.issuer': '签发者',
  'trust.editor.subjectPattern': '主体前缀',
  'trust.editor.subjectPatternHint': '留空表示不限制主体。',
  'trust.editor.publicKey': '新公钥 PEM',
  'trust.editor.publicKeyHelp': '只粘贴公钥；签名私钥留在签发方，平台不接受也不存储它。',
  'trust.editor.graceHours': '旧根宽限小时数',
  'trust.editor.graceHoursHint': '0 表示立即切换；默认 24 小时，让在途签名仍可用旧根验证。',
  'trust.editor.overlapHint': '密钥 ID 与签发者都不能与仍生效或处于宽限期的信任根重复。',
  'trust.editor.submitCreate': '创建并启用',
  'trust.editor.submitRotate': '轮换',
  'trust.editor.cancel': '取消',
  'trust.editor.saving': '提交中…',
  'trust.editor.publicKeyRequired': '请粘贴新信任根的公钥 PEM。',
  'trust.editor.publicKeyPrivate': '检测到私钥 PEM：请只粘贴公钥，私钥绝不能离开签发方。',
  'trust.editor.publicKeyFormat': '公钥格式不正确：需要一个 PEM 公钥块（Ed25519）。',
  'trust.editor.keyIdRequired': '请填写密钥 ID。',
  'trust.editor.issuerRequired': '请填写签发者。',
  'trust.editor.graceHoursInvalid': '宽限小时数必须是不小于 0 的数字。',
  'trust.create.notice': '信任根 {keyId} 已创建并生效（策略版本已提升）。',
  'trust.rotate.notice': '已轮换：新根 {keyId} 生效，旧根进入宽限期。',
  'route.preview.title': '路由预览',
  'route.preview.centralUri': '中心地址',
  'route.preview.targetUri': '目标地址',
  'route.preview.sourcePlaceholder': '请输入源前缀',
  'route.preview.targetPlaceholder': '请输入目标前缀',
  'home.workbench': '工作台',
  // Cross-release non-terminal queue (REQ-100 H9 / TASK-277). The home overview
  // (TASK-394) reads the same feed, so these labels are reused by its panel.
  'home.todo.viewAll': '前往操作中心',
  'home.todo.loading': '正在加载待办…',
  'home.todo.empty': '当前没有待处理的操作。',
  'home.todo.maintenance': '维护中不可用',
  'home.todo.forbidden': '无权读取待办',
  'home.todo.error': '待办加载失败',
  'operationCenter.title': '操作中心',
  'operationCenter.eyebrow': '跨 Release',
  'operationCenter.hint': '跨 Release 的未结束操作',
  'operationCenter.description': '本组织授权范围内所有 Release 中尚未结束的操作，按等待时间从长到短排序。',
  'operationCenter.column.state': '状态',
  'operationCenter.column.type': '类型',
  'operationCenter.column.customer': '客户',
  'operationCenter.column.release': '发布定义',
  'operationCenter.column.revision': '目标版本',
  'operationCenter.column.createdAt': '创建时间',
  'operationCenter.column.operation': '操作标识',
  'operationCenter.emergency': '紧急',
  'operationCenter.emptyMessage': '当前作用域内没有未结束的操作。',
  'operationCenter.maintenance': '维护中不可用：平台正处于维护模式，操作中心暂时无法读取。',
  'operationCenter.forbidden': '无读取权限：当前账号无权读取该作用域内的操作。',
  'operationCenter.pagination': '操作分页',
  'operation.create.eyebrow': '新建操作',
  'operation.create.subtitle': '发布操作',
  'operation.list.eyebrow': '操作历史',
  'operation.list.subtitle': '发布操作',
  // Local accounts (REQ-025 / A4 second half)
  'role.platformAdmin': '平台管理员',
  'role.releaseAdmin': '发布管理员',
  'role.deployer': '部署者',
  'role.viewer': '只读',
  'localUser.page.title': '本地用户',
  'localUser.page.eyebrow': '平台管理',
  'localUser.page.description': '本地账号只有平台管理员能读写，列表是全局的；下方显示各账号所属组织，每个账号最多一个角色。',
  'localUser.column.username': '用户名',
  'localUser.column.role': '角色',
  'localUser.column.status': '状态',
  'localUser.status.active': '生效中',
  'localUser.status.pending': '待激活',
  'localUser.status.disabled': '已停用',
  'localUser.status.unknown': '未知',
  'localUser.list.empty': '还没有本地账号。',
  'localUser.list.loading': '正在加载本地用户…',
  'localUser.list.loadMore': '加载更多',
  'localUser.create.title': '创建本地账号',
  'localUser.create.username': '用户名',
  'localUser.create.password': '初始密码',
  'localUser.create.passwordHint': '密码只做非空校验；长度上限受哈希算法限制。',
  'localUser.create.role': '角色',
  'localUser.create.submit': '创建账号',
  'localUser.create.submitting': '创建中…',
  'localUser.create.ready': '本地账号 {username} 已就绪（同名账号会复用已有账号）',
  'localUser.error.permissionDenied': '只有平台管理员能管理本地账号。',
  'localUser.error.invalidInput': '用户名或密码不合法（每个账号最多一个角色，且不能授予平台管理员）。',
  'localUser.error.invalidCursor': '分页游标已失效，已回到第一页。',
  'localUser.error.unavailable': '本地账号服务不可用，请稍后重试。',
  'localUser.error.sessionExpired': '会话已失效，请重新登录后再管理本地账号。',
  'localUser.error.orgDisabled': '该组织已停用，无法创建本地账号。',
  'localUser.error.passwordTooLong': '密码过长（上限 {max} 字节）。',
  'localUser.column.org': '组织',
  // Shared state components' defaults
  'state.noData': '暂无数据',
  'state.accessDenied': '无权访问',
  'state.accessDeniedMessage': '你没有访问该资源的权限。',
  'state.loading.label': '加载中',
  // Correlation / failure chrome
  'error.technicalDetails': '技术详情',
  'error.title': '出错了',
  'action.returnHome': '返回首页',
  // Stable server reason codes (the carriers the console reads instead of free text).
  'reason.invalid_cursor': '页码已过期，已从最新一页重新加载',
  'reason.release_busy': '该 Release 有正在进行的操作，请等它结束后再试',
  'reason.duplicate_member': '该用户已是组织成员；请直接修改其角色',
  'reason.last_platform_admin_forbidden': '不能移除或降级最后一个平台管理员',
  'reason.organization_disabled': '该组织已停用，无法再执行该操作',
  'reason.duplicate_binding': '该组织与客户的绑定已存在（正在生效）',
  'reason.binding_revoked': '该绑定已撤销',
  'reason.overlap_conflict': '新密钥与某个仍生效的信任根重叠，请换一把密钥',
  'reason.last_root_removal_forbidden': '这是该环境最后一个可用信任根，不能退休或吊销',
  'reason.reason_required': '必须填写原因',
  'reason.release_mode_unspecified': '必须选择释放模式',
  'reason.intent_not_found': '未找到该锁：它可能已被释放（释放不可重放）',
  'reason.definition_not_found': '未找到该发布定义',
  'reason.definition_not_found_filter': '未找到该发布定义；请检查过滤条件（留空可查看当前组织内全部锁）',
  'reason.authentication_required': '会话已失效，请重新登录',
  'reason.rollback_values_not_allowed': '回滚不能携带 Values 版本或补丁',
  'reason.optimistic_lock_conflict': '记录已被其他人修改，已为你刷新最新数据',
  'reason.AUTHORIZATION_VERSION_CONFLICT': '授权版本已变化，请重新读取后重试',
  /*
   * Accessible names for the scroll regions added by TASK-394. A table that can outgrow
   * its column becomes a labelled, focusable region (the shape DefinitionTable /
   * OrganizationMemberTable / ReleaseInventoryTable already use), so a screen-reader user
   * hears WHERE they are before scrolling and a keyboard user can Tab to it at all.
   */
  'trust.table.scrollRegion': '信任根表格（可横向滚动）',
  'operations.table.scrollRegion': '操作历史表格（可横向滚动）',
  'bundles.table.scrollRegion': 'Bundle 列表表格（可横向滚动）',
  'bundles.images.scrollRegion': 'Bundle 镜像绑定表格（可横向滚动）',
  'bindings.table.scrollRegion': '授权绑定表格（可横向滚动）',
  'localUser.table.scrollRegion': '本地用户表格（可横向滚动）',
  'stuckLocks.table.scrollRegion': '紧急锁表格（可横向滚动）',
  'emergency.tasks.scrollRegion': '收敛任务表格（可横向滚动）',
  'annotation.table.scrollRegion': '注解表格（可横向滚动）',
  'definitions.mappings.scrollRegion': '提升映射表格（可横向滚动）',
  // --- home overview (TASK-394) ---
  'home.overview.subtitle': '发布活动与集群健康一览',
  'home.overview.loading': '正在加载…',
  'home.overview.loadFailed': '加载失败',
  'home.overview.featureOff': '功能未启用',
  'home.stat.pendingConvergence': '待收敛发布',
  'home.stat.pendingConvergenceHint': '共 {count} 个发布',
  'home.stat.activeOperations': '进行中操作',
  'home.stat.activeOperationsHint': '{count} 个在下发阶段',
  'home.stat.syncRate': '发布同步率',
  'home.stat.syncRateValue': '{percent}%',
  'home.stat.syncRateHint': '{total} 个发布中 {synced} 个与集群一致',
  'home.stat.syncRateEmpty': '当前没有发布',
  'home.stat.emergency': '紧急变更',
  'home.stat.emergencyHint': '需要显式收敛',
  'home.create.open': '创建操作',
  'home.create.title': '选择要操作的发布',
  'home.create.hint': '创建操作需要一个发布作为上下文；列表来自当前组织的发布清单。',
  'home.create.selectLabel': '发布',
  'home.create.confirm': '前往创建',
  'home.live.online': '{count} 个 Operator 在线',
  'home.live.partial': '{count} 个在线 · 已探测 {probed}/{total} 个集群',
  'home.live.unavailable': 'Operator 状态未知',
  'home.operations.title': '进行中的操作',
  'home.operations.note': '按等待时长升序 · 接口只提供进行中操作',
  'home.operations.clusterUnknown': '未知集群',
  'home.operations.truncated': '已加载 {count} 条，可能还有更多',
  'home.cluster.title': '集群健康',
  'home.cluster.synced': '已同步 {synced}/{total}',
  'home.cluster.operators': '{count} 个在线',
  'home.cluster.noOperator': '暂无 Operator',
  'home.cluster.heartbeat': 'Operator 心跳间隔 {seconds} 秒',
  'home.cluster.heartbeatUnknown': 'Operator 心跳间隔未知',
  'home.cluster.more': '另有 {count} 个集群未显示',
  'home.cluster.empty': '暂无集群',
  'home.cluster.error': '集群健康加载失败',
  'home.quickLinks.title': '快速入口',
  'home.time.justNow': '刚刚',
  'home.time.minutes': '{count} 分钟前',
  'home.time.hours': '{count} 小时前',
  'home.time.days': '{count} 天前',
  'home.time.unknown': '时间未知',


  // --- release inventory table (TASK-394) ---
  /*
   * Direction-C release inventory columns. The status LABELS are reused from the
   * `release.status.*` entries above (they are the store's own vocabulary); what is new
   * here is the column set and the per-status tooltip copy AC-054-02 requires. Those
   * tooltips lived inline in ReleaseStatusBadge.vue, which the inventory table no longer
   * renders now that the status is a StatusTag.
   */
  'release.table.release': '发布',
  'release.table.cluster': '集群',
  'release.table.revision': '当前版本',
  'release.table.status': '库存状态',
  'release.table.emergency': '紧急变更',
  'release.table.actions': '操作',
  'release.status.tooltip.active': 'Release 与最近一次同步结果一致',
  'release.status.tooltip.missing': 'Release 已从集群中消失',
  'release.status.tooltip.outOfSync': '配置与期望不一致',

  // --- audit page (TASK-394) ---
  /*
   * Direction C audit page. The heading sentence names the scope the current query is
   * pinned to (an organization, every organization, or the platform "system" scope);
   * the wording mirrors the page's subtitle contract "查看 <scope> 的脱敏审计记录".
   */
  'audit.page.headerSubtitle': '查看 {scope} 的脱敏审计记录',
  'audit.table.scrollRegion': '审计事件表格（可横向滚动）',
  /*
   * Labels for the audit outcome values the server actually writes (the sources are
   * listed in src/components/audit/auditEventStatus.ts). The four values the catalog
   * already covered reuse status.audit.*; these are the rest of the open set, and an
   * unknown value still falls back to the raw server string on purpose.
   */
  'audit.result.trusted': '可信',
  'audit.result.rejected': '已拒绝',
  'audit.result.blocked': '已阻断',
  'audit.result.wouldBlock': '预演：将阻断（观察模式）',
  'audit.result.policyWarning': '策略警告',
  'audit.result.signatureMissing': '缺少签名',
  'audit.result.verificationUnavailable': '校验不可用',
  'audit.result.pending': '待处理',
  'audit.result.preflight': '预检中',
  'audit.result.queued': '已排队',
  'audit.result.running': '执行中',
  'audit.result.cancelling': '取消中',
  'audit.result.cancelled': '已取消',
  'audit.result.timeout': '超时',

  // --- create operation (TASK-394) ---
  /*
   * Direction C rewrote the create page around a header + stepper + two-column
   * form/confirmation layout. The page title, subtitle and the back action moved here
   * from inline copy; the two stepper labels and the confirmation-panel chrome
   * (type row, "check the form first" hint) are new because the panel is now visible
   * while the form is being filled instead of only behind the old confirmation step.
   */
  'operation.create.title': '创建发布操作',
  'operation.create.lead': '选择制品和已审批配置，确认后启动预检',
  'operation.create.back': '返回发布清单',
  'operation.create.step.artifact': '制品与配置',
  'operation.create.step.confirm': '确认',
  'operation.confirm.type': '类型',
  'operation.confirm.hint': '先完成表单并点击「检查并确认」，确认创建才可用。',

  // --- operation detail (TASK-394) ---
  /*
   * Direction C rewrote the detail page around a PageHeader (operation type + release
   * name title, state/version subtitle, name-only breadcrumbs), a four-step lifecycle
   * strip and a two-column preflight/timeline layout. The load-failure surface is new
   * copy: the page used to sit on "正在加载 Operation…" forever for an unknown id, so
   * the error state now names the failure, shows its stable code and offers a way back.
   */
  'operation.detail.breadcrumb': '操作详情',
  'operation.detail.title': '{type}操作',
  'operation.detail.titleWithRelease': '{type}操作 · {release}',
  'operation.detail.subtitle': '{state} · 状态版本 {version}',
  'operation.detail.steps.preflight': '预检',
  'operation.detail.steps.approval': '审批',
  'operation.detail.steps.dispatch': '下发',
  'operation.detail.steps.convergence': '收敛',
  'operation.detail.preflightPane': '预检结果',
  'operation.detail.timelinePane': '状态时间线',
  'operation.detail.preflightEmptyTitle': '暂未返回预检结果',
  'operation.detail.preflightEmptyMessage': '预检完成后会列出各阶段结果。',
  'operation.detail.loading': '正在加载操作…',
  'operation.detail.liveUpdatesOff': '实时更新已关闭',
  'operation.detail.liveUpdatesHint': '点击刷新加载操作最新状态',
  'operation.detail.loadFailed': '操作加载失败',
  'operation.detail.errorCode': '错误码',
  'operation.detail.loadTimeout': '加载超时，未能读取该操作。',
  'operation.detail.backToHistory': '返回操作历史',
  'operation.detail.backToCenter': '返回操作中心',


  // --- values diff (TASK-394) ---
  /*
   * Direction C gives the Values page a header action that names what the save creates
   * (a new ValuesRevision, not an in-place edit), and gives the side-by-side diff card a
   * heading plus a zero-change label. The count itself composes with the existing
   * `values.diff.changes` unit, so "N 处变更" and "无变更" are one vocabulary.
   */
  'values.page.saveNew': '保存为新版本',
  'values.page.saving': '保存中…',
  'values.page.saveBlocked': '保存为新版本需要写权限，且内容需通过校验。',
  'values.diff.heading': '与上一版本的差异',
  'values.diff.none': '无变更',

  // --- consistency w2 (TASK-394) ---
  /*
   * The operator detail page printed the raw operator UUID under its title. The id now
   * sits in the identity card as a CopyableHash row, which needs its own label. The value
   * follows the existing "X 标识" vocabulary (`operationCenter.column.operation`,
   * `bundle.detail.bundleId`) and stays in Chinese like the rest of the catalog.
   */
  'operator.detail.id': 'Operator 标识',


  // --- consistency w3 (TASK-394) ---
  /*
   * Wave-3 of the direction-C consistency pass: Bundles, StuckLocks and TrustPolicy.
   * These three pages were the densest hard-coded-Chinese surfaces that still had no
   * shared header and no table styling. Their page copy now lives here (the eyebrow
   * strings reuse the existing `bundle.eyebrow` / `stuck.eyebrow` / `trust.eyebrow`
   * entries, so the category meaning is unchanged); the two English placeholders on
   * the bundle filters and the `Chart` column header stay inline, because they are
   * wire-field examples and a domain noun rather than prose.
   */
  'bundles.page.title': '发布 Bundle',
  'bundles.page.subtitle': 'Bundle 是不可变输入：内容摘要、Chart 引用与镜像绑定，以及签名/SBOM/来源证明的引用。',
  'bundles.page.total': '共 {count} 个',
  'bundles.filters.status': '状态',
  'bundles.filters.all': '全部',
  'bundles.filters.apply': '查询',
  'bundles.filters.applying': '查询中…',
  'bundles.load.failed': '加载未成功',
  'bundles.load.failedAction': '回到第一页',
  'bundles.load.prompt': '请先填写发布定义 ID：服务端用它确定查询范围并校验权限。',
  'bundles.loading': '正在读取 Bundle…',
  'bundles.empty.title': '没有 Bundle',
  'bundles.empty.message': '当前过滤条件下没有 Bundle；清空过滤可查看全部。',
  'bundles.table.name': '名称',
  'bundles.table.status': '状态',
  'bundles.table.digest': '摘要',
  'bundles.table.images': '镜像绑定',
  'bundles.table.receivedAt': '接收时间',
  'bundles.table.actions': '操作',
  'bundles.row.images': '{count} 个',
  'bundles.row.detail': '明细',
  'bundles.more.loading': '加载中…',
  'bundles.more.label': '加载更多',
  'bundles.more.appending': '正在追加下一页…',
  'bundles.detail.title': 'Bundle 明细',
  'bundles.detail.loading': '正在读取明细…',
  'bundles.detail.loadFailed': '明细加载失败',
  'bundles.detail.name': '名称',
  'bundles.detail.digest': '摘要',
  'bundles.detail.status': '状态',
  'bundles.detail.evidence': '证据引用',
  'bundles.detail.signature': '签名',
  'bundles.detail.provenance': '来源证明',
  'bundles.detail.images': '镜像绑定（{count}）',
  'bundles.detail.close': '关闭明细',
  'bundles.images.ref': '引用',
  'bundles.images.digest': '摘要',
  'bundles.images.valuesPath': 'Values 路径',

  'stuckLocks.page.title': '卡住的紧急锁',
  'stuckLocks.page.subtitle': '处于终态的紧急变更操作在观察窗口内仍未确认集群效果时会保留锁，发布定义因此无法接受新任务。',
  'stuckLocks.refreshing': '刷新中…',
  'stuckLocks.filter.label': '发布定义过滤（留空表示当前组织内全部）',
  'stuckLocks.filter.submit': '查询',
  'stuckLocks.load.failed': '操作未成功',
  'stuckLocks.load.failedAction': '刷新列表',
  'stuckLocks.loading': '正在读取卡住的锁…',
  'stuckLocks.empty.title': '没有卡住的锁',
  'stuckLocks.empty.message': '当前范围内没有「效果未知且已过观察窗口」的紧急操作——这正是期望状态。',
  'stuckLocks.table.definition': '定义 / 操作',
  'stuckLocks.table.action': '动作',
  'stuckLocks.table.lockPath': '锁定路径',
  'stuckLocks.table.terminal': '终态时间 / 卡住自',
  'stuckLocks.table.window': '观察窗口',
  'stuckLocks.table.actions': '操作',
  'stuckLocks.table.stuckSince': '卡住自 {time}',
  'stuckLocks.release.entry': '释放锁',
  'stuckLocks.mode.legend': '释放模式',
  'stuckLocks.mode.notApplied': '证明未生效',
  'stuckLocks.mode.override': '审计后强制接管',
  'stuckLocks.reason.label': '释放原因（必填，≤1000 字）',
  'stuckLocks.reason.placeholder': '为什么可以释放这个锁',
  'stuckLocks.evidence.label': '解锁证据（可选，≤500 字）',
  'stuckLocks.evidence.placeholder': 'kubectl 的观察结论，例如 deploy 的副本状态',
  'stuckLocks.ack': '我已通过集群观察核实该效果确实可以释放（释放后定义会重新接受任务，且没有撤销）',
  'stuckLocks.submitting': '提交中…',
  'stuckLocks.submit': '确认释放',
  'stuckLocks.validation.reasonRequired': '必须填写释放原因',
  'stuckLocks.validation.reasonTooLong': '释放原因最多 1000 个字符',
  'stuckLocks.validation.evidenceTooLong': '解锁证据最多 500 个字符',
  'stuckLocks.validation.modeRequired': '必须选择释放模式',
  'stuckLocks.validation.ackRequired': '请先确认已核实集群效果',

  'trust.page.title': '签名信任根',
  'trust.filter.environment': '环境',
  'trust.filter.version': '策略版本',
  'trust.filter.liveRoots': '可用信任根',
  'trust.refreshing': '刷新中…',
  'trust.error.title': '信任根操作未成功',
  'trust.error.action': '刷新策略',
  'trust.loading': '正在读取信任策略…',
  'trust.empty.title': '该环境还没有信任根',
  'trust.empty.message': '没有任何签名密钥被信任（策略版本 1、吊销代数 0）——这是「尚未配置」而不是错误。',
  'trust.table.state': '状态',
  'trust.table.validity': '生效自 / 宽限至',
  'trust.table.actions': '操作',
  'trust.table.graceUntil': '宽限至 {time}',
  'trust.dialog.revokeBody': '吊销会立即停止该密钥的签名校验，并提升吊销代数（缓存持有者据此失效）；记录会保留。已用该密钥验证过的制品不会因此失去既有地位。',
  'trust.dialog.submitting': '提交中…',
  'trust.dialog.confirmRevoke': '确认吊销',
  'trust.action.endGrace': '结束宽限',
  'trust.action.retire': '退休',
  'trust.action.revoke': '吊销',
  'trust.disabled.lastRoot': '该环境只剩一个可用信任根，不能移除',


  // --- consistency w5 (TASK-394) ---
  /*
   * The convergence-tasks and operation-history pages moved their heading and action copy
   * out of the template. The two `definition` labels name the CopyableHash that now carries
   * the release definition id the h1/subtitle used to print: CopyableHash itself only knows
   * a value, so the surrounding label says what the value is.
   */
  'convergence.page.definition': '发布定义',
  'operation.list.back': '返回发布清单',
  'operation.list.create': '创建操作',
  'operation.list.definition': '发布定义',

  // --- consistency w7 (TASK-394) ---
  /*
   * The emergency-change page had no h1 at all. Its category is the same one the stuck-lock
   * surface carries (`stuck.eyebrow` also reads 紧急), but the value is stated here under the
   * emergency page's own key so a later change to the stuck-lock category cannot silently
   * relabel this page. The word itself is not new copy: it is the category name the console
   * already uses.
   */
  'emergency.page.eyebrow': '紧急',
} as const;

export type MessageKey = keyof typeof zhCN;

const warned = new Set<string>();

export function interpolate(template: string, params?: MessageParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : whole,
  );
}

/**
 * Resolves a message. A key that is not in the table is a bug: it warns once (dev
 * only, so production stays quiet) and renders the raw key, which is visible in the
 * UI instead of silently showing an empty string.
 */
export function t(key: MessageKey, params?: MessageParams): string {
  // hasOwnProperty, not a plain lookup: 'constructor' / 'toString' would otherwise
  // resolve through the prototype and throw inside interpolate().
  const template = hasMessage(key) ? (zhCN as Record<string, string>)[key] : undefined;
  if (template === undefined) {
    if (import.meta.env.DEV && !warned.has(key)) {
      warned.add(key);
      console.warn(`[i18n] missing message: ${key}`);
    }
    return key;
  }
  return interpolate(template, params);
}

/** True when the catalog knows this key (used to detect copy that drifted away). */
export function hasMessage(key: string): boolean {
  return Object.prototype.hasOwnProperty.call(zhCN, key);
}

export function messageKeys(): MessageKey[] {
  return Object.keys(zhCN) as MessageKey[];
}
