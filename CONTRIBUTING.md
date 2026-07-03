# 贡献指南

## 开发流程

1. **Fork & Clone:** fork 仓库，clone 到本地
2. **分支:** 从 `main` 创建特性分支 `feat/<feature>` 或 `fix/<bug>`
3. **开发:** 遵循下方代码规范
4. **测试:** `make test` 全部通过
5. **提交:** 使用约定式提交格式
6. **PR:** 提交 Pull Request 到 `main`

## 提交规范

```
<type>(<scope>): <description>

type:
  feat     新功能
  fix      Bug 修复
  docs     文档变更
  test     测试相关
  refactor 代码重构（无功能变更）
  chore    构建/工具变更

scope:
  operator, notification, helm, tls, config, docs, test, makefile

示例:
  feat(operator): add install-or-upgrade with v4 SDK
  fix(notification): handle empty webhook payload
  test(helm): add unit tests for pullChart
```

## 代码审查清单

- [ ] 所有公共函数/方法有 godoc 注释
- [ ] 错误使用 `fmt.Errorf("context: %w", err)` 包装
- [ ] 需要 context 的函数接收 `context.Context` 参数
- [ ] 日志使用结构化 key-value 对
- [ ] 新增功能有对应的单元测试
- [ ] 对外 API 变更更新 swagger.md
- [ ] PR 描述说明做了什么和为什么

## 运行测试

```bash
# 单元测试
make test

# 含覆盖率
make test-cover

# E2E 测试
make kind-up
make test-e2e
make kind-down
```

## 目录结构

参见 [README.md](README.md#项目结构).
