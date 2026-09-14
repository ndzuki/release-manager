# ADR-018：证书序列号（sha256(certDER) 前 10 字节 hex）为身份权威，renew 即时失效旧证书

- Status: accepted
- Date: created 2026-08-04 / updated 2026-08-04
- Scope: project
- Related: REQ-015 (Requirements); TASK-015 (Tasks)

## Status
accepted

## Context
X.509 SerialNumber 由签发者选择，不能单独证明客户端展示的是控制面当前认可的那一张证书。证书续期后旧证书在 NotAfter 前仍可通过 CA 信任链；若仅检查 operator_id、SAN 和数据库状态，泄露的旧证书仍可重新连接。控制面需要一个从实际证书字节稳定派生、可精确比较且不依赖客户端自报的当前证书标识。

## Decision
将 `sha256(certDER)` 的前 10 字节编码为小写 hex，作为 `Operator.CertSerial` 的权威表示。签发时从最终 DER 计算并持久化；mTLS 请求从已验证对端证书的 `Raw` DER 以同一算法计算。Enroll 写入初始值，RenewCertificate 原子替换当前值；CommandStream 和 renew 身份守卫必须要求呈现值等于数据库当前值。续期提交成功后旧证书立即返回稳定错误码 `cert_replaced`。

## Alternatives Considered
- 使用 X.509 SerialNumber：不能保证现有代码与数据库使用同一表示，且容易把签发字段误当作证书内容指纹。
- 使用完整 SHA-256：安全裕量更大但存储与日志更长；80-bit 截断对当前身份规模已足够，并由数据库唯一约束检测冲突。
- 允许旧证书直到自然过期：扩大泄露窗口，违反 renew 后即时失效要求。
- 使用 CRL/OCSP：增加分发与在线依赖；本系统已有每次重连查询控制面状态的权威路径。

## Consequences
- 续期后只有最新证书可重新建立控制流，现有已连接 session 不被主动中断。
- 所有签发、TLS 身份提取与数据库比较代码必须复用同一 DER-hash helper，禁止使用 `x509.Certificate.SerialNumber.String()`。
- 数据库需要保存当前 `cert_serial` 并建立唯一约束；极低概率截断冲突会导致签发事务失败而非错误绑定身份。
- 撤销仍由 operator 状态权威完成，不引入 CRL 或 OCSP。
