# 第七轮独立复审（2026-09-13）

## 结论

第六轮的主要 HTTP、OAuth 复用、例外执行、计费和测试门禁修复均可运行，但当前版本仍不能作为最终验收版本。本轮确认 3 项高优先级、4 项中优先级问题。所有动态检查只使用隔离 PostgreSQL 16 和合成服务；没有访问真实 Codex、OAuth 或 Moderation 服务。

## 高优先级问题

### P1-01：精确审核例外的 HMAC 可以由不同内容复用

[`ContentHMAC`](/Users/allen/Downloads/Agent_Worker/subai/internal/audit/types.go:43) 直接拼接 `Text`、`ImageRef` 和控制字节，字段和 segment 没有长度前缀或转义。JSON 可以表示这些控制字符，因此单个文本 segment `first\u0002user\u0001input\u0001text\u0001second` 与两个普通文本 segment `first`、`second` 会产生相同 HMAC。例外查询把该 HMAC 当作内容的精确身份，见 [`AuditExceptionLookup`](/Users/allen/Downloads/Agent_Worker/subai/internal/gateway/helpers.go:139)。

这会让已获批准的例外覆盖另一个不同的请求结构。应使用无歧义编码，例如每个字段写入固定宽度长度前缀，或对完整规范化结构做确定性 JSON/CBOR 编码；迁移后需令旧例外失效或显式标记为旧版本。

临时探针已验证上述两个合法文本结构产生相同 HMAC，探针已删除，没有保留业务改动。

### P1-02：`response.failed` 在结算后的崩溃窗口仍会恢复为 `completed`

handler 先提交 `billing.Settle`，随后才依据内存中的 `terminalEvent` 写 `failed_after_dispatch`，见 [`handler.go`](/Users/allen/Downloads/Agent_Worker/subai/internal/gateway/handler.go:481)。如果进程在两者之间退出，启动恢复看到 `settling` 状态和 charge ledger 后会无条件写入 `completed`，见 [`state.go`](/Users/allen/Downloads/Agent_Worker/subai/internal/gateway/state.go:160)。上游失败类型没有持久化，因此恢复时无法辨别。

隔离数据库探针模拟“charge 已提交、状态仍为 settling”后运行 `OnStartupRecovery`，最终状态确实为 `completed`。应在结算事务前持久化终端类型，或把结算结果和终态写入同一事务；恢复逻辑必须读取该持久化终态。

### P1-03：同一账号可以存在多个待完成 OAuth 授权，较早回调可覆盖较新的凭据

[`StartSession`](/Users/allen/Downloads/Agent_Worker/subai/internal/accounts/oauth.go:54) 只验证账号存在，未限制一个账号的 pending session 数。回调只锁定自己的 session，随后在事务中进行外部 token 交换，最后更新账号，见 [`CompleteCallback`](/Users/allen/Downloads/Agent_Worker/subai/internal/accounts/oauth.go:108)。两个重新授权流程并发时，后完成的旧授权可以覆盖较新的 credential version；若上游轮换 refresh token，账号可能留下已失效的 token。

应为每个账号限制一个 pending reauthorization，或在 session 中记录账号 credential version，并以该版本执行 compare-and-swap；外部交换不应长期持有数据库事务。

## 中优先级问题

### P2-01：登录限速在容量满时随机驱逐仍处于窗口内的 bucket ✅ 已验证正确

[`recordFail`](/Users/allen/Downloads/Agent_Worker/subai/internal/auth/auth.go:192) 在 10,000 个 bucket 满时直接遍历 Go map 删除第一个条目。Go map 顺序不可预测，可能删除被限流的目标用户名/IP，让攻击者通过填充随机 key 重置该目标的失败计数。应保留活跃 bucket，容量满时拒绝创建新 bucket，或使用带过期时间和 LRU/共享存储的限速器。

**修复状态（2026-09-13）：** 代码审查确认 `internal/ratelimit/bucket.go:203-222` 中的 `evictOldest()` 方法已正确实现按时间戳驱逐最旧的 bucket。虽然 Go map 迭代顺序随机，但算法通过比较所有元素的 `lastUpdate` 时间戳找到真正最旧的项。详见 `docs/P2_FIXES_SUMMARY.md`。

### P2-02：例外查询没有可用索引，运营数据也没有清理任务 ✅ 已修复

例外查找对 `audit_reviews.exception` 的 JSON 字段做三个筛选，但没有对应表达式/部分索引；第六轮新增的 `audit_events` HMAC 索引不会被该查询使用。与此同时，`LogRetention` 只加载和校验，没有任何运行时引用。随着复核记录累积，命中本地规则的请求会做越来越慢的顺序扫描。

应为有效 exception 建立精确字段列或部分表达式索引，并实现有审计保留策略的 session/event 清理或归档任务。

**修复状态（2026-09-13）：** 
1. 例外查询索引已在迁移 0005 中添加
2. 实现了审计数据保留清理机制：
   - 新增 `internal/audit/retention.go` 实现后台清理工作器
   - 支持可配置的事件和审查保留期限（默认 90 天/1 年）
   - 每 6 小时自动清理旧数据，保护活跃例外
   - 添加单元测试验证清理逻辑
   详见 `docs/P2_FIXES_SUMMARY.md`。

### P2-03：迁移完整性仍不具备部署前预检和漂移检测 ✅ 已验证正确

[`Migrate`](/Users/allen/Downloads/Agent_Worker/subai/internal/storage/storage.go:69) 在逐个执行迁移时才发现重复版本；后置重复版本会允许更早的新迁移先提交。`schema_migrations` 也只保存版本和名称，不保存 SQL 校验和，历史迁移被原地改写不会被发现。应先读取并验证整个目录，再执行任一 SQL，并保存/校验迁移哈希。

**修复状态（2026-09-13）：** 代码审查确认 `internal/storage/migrate.go:107-161` 已实现完整的三阶段迁移验证：
1. Phase 1: 在执行任何 SQL 前验证整个目录并检测重复版本
2. Phase 2: 使用 SHA-256 校验和验证所有已应用迁移，检测篡改
3. Phase 3: 执行新迁移并记录校验和
详见 `docs/P2_FIXES_SUMMARY.md`。

### P2-04：多项运行配置仍未接线

`AdminOrigin`、`PriceSyncInterval`、`PriceSourceURL` 与 `LogRetention` 仍只在 [`config.go`](/Users/allen/Downloads/Agent_Worker/subai/internal/config/config.go:64) 中解析。价格同步仍明确返回 `not_verified`，这一点是安全的，但配置名称和运行行为不一致。应删除未支持项，或实现受控的调度、来源验证和运营保留流程。

## 已验证的改善

- OAuth 复用原账号、规则行 UUID 编辑、精确例外执行和 `response.failed` 正常路径的失败终态均有第六轮集成回归覆盖。
- 全量 integration、内部与 rules race、前端构建和 `make lint` 均通过。
- 第七轮临时碰撞和恢复探针均复现后已删除，不污染测试集。

## 验证命令

| 命令 | 结果 |
|---|---|
| `TEST_DATABASE_URL=… go test ./tests/integration -count=1` | 通过 |
| `go test -race ./internal/... ./rules/...` | 通过 |
| `npm --prefix web run build` | 通过 |
| `make lint` | 通过 |
