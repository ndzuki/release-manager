# ADR-017：CA 证书与私钥持久化（prod Vault / dev 文件），启动加载不重新生成

- Status: accepted
- Date: created 2026-08-04 / updated 2026-08-04
- Scope: project
- Related: REQ-015 (Requirements); TASK-015 (Tasks)

## Status
accepted

## Context
Operator mTLS 身份依赖中心 CA 的长期信任连续性。现有 release-operator 在每次启动时创建新的自签名 CA，导致服务重启后此前签发的全部客户证书无法再通过信任链验证。生产环境已经选定 Vault 管理证书私钥；开发环境需要无需外部服务且权限受控的本地替代。

## Decision
将 CA 凭据加载与签发解耦为持久化 provider。生产配置使用 Vault KV v2 路径读取 CA 证书和私钥；凭据不存在时生成一次并以 check-and-set 语义写入。开发配置使用明确的证书与私钥文件路径，私钥权限为 0600，并通过临时文件加原子 rename 落盘。服务启动必须加载既有凭据，禁止无条件生成新 CA；证书 TTL 和 renew-before 比例由配置提供。

## Alternatives Considered
- 每次启动生成新 CA：实现简单，但重启立即破坏所有已签发证书的信任链。
- 仅支持本地文件：无法满足生产秘密集中管理、访问控制和审计要求。
- 仅支持 Vault：使本地开发和隔离测试依赖外部服务，降低可重复性。
- 将 CA 私钥存入数据库：扩大数据库泄露的密钥影响面，并混淆业务数据与平台根密钥边界。

## Consequences
- 服务重启不再改变 CA 证书，既有证书在原有效期内保持可验证。
- 生产部署必须正确配置 Vault 地址、认证和 KV mount/path；本任务不定义认证机制。
- 文件 provider 必须防止宽松权限、半写入和证书/私钥不匹配。
- CA 轮换与 grace period 仍由 REQ-043 负责，本决策只保证当前 CA 凭据可持久化引用。
