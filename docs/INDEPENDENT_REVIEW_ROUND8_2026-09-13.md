# 第八轮独立全面审查（2026-09-13）

## 结论

当前重新优化后的版本包含对第七轮 HMAC、失败恢复、限速和迁移预检的有效改进，但**不能部署或验收**。本轮确认两项会阻断数据库启动/升级的 P0 问题，以及两项 OAuth P1 问题。全量集成测试无法开始，因为全新数据库迁移会在第 5 份 SQL 文件失败。

动态检查仅使用隔离 PostgreSQL 16 和合成服务，没有访问真实 Codex、OAuth 或 Moderation 服务。临时复现代码已删除，隔离数据库已停止。

## P0：迁移链路阻断

### P0-01：全新数据库无法应用 `0005_terminal_outcome.sql`

[`0005_terminal_outcome.sql`](/Users/allen/Downloads/Agent_Worker/subai/migrations/0005_terminal_outcome.sql:10) 建立的 partial index 条件包含 `expires_at > now()`。PostgreSQL 要求 index predicate 只使用 immutable 函数，`now()` 是 stable 函数。因此任何空数据库都会在这里停止迁移，报错：

```text
functions in index predicate must be marked IMMUTABLE (SQLSTATE 42P17)
```

这也阻断了完整 integration 测试。不能把动态时间条件直接写入 index predicate；应改为索引 `outcome` 和例外的三个 JSON 表达式/独立字段，并在查询中保留 `expires_at > now()`，或用清理任务删除过期记录。

### P0-02：现有安装无法升级到 checksum 迁移

[`Migrate`](/Users/allen/Downloads/Agent_Worker/subai/internal/storage/storage.go:154) 在执行任何未应用 SQL 之前就查询 `schema_migrations.checksum`。但第 6 份迁移才添加该列，旧版本创建的 `schema_migrations` 表没有它。因此升级会先因 `column "checksum" does not exist` 失败，`0006_migration_checksums.sql` 永远无法运行。

此外，`0006` 为历史行设置 `legacy`，而 loader 随后要求历史 checksum 等于当前 SQL 的 SHA-256；即使先手工补列，下一次也会因 `legacy` 与实际哈希不相等而拒绝启动。应在 loader 中检测列是否存在并先兼容地执行升级迁移，或将 checksum 元数据迁移与校验逻辑拆分；对已有行必须定义明确的信任/回填策略。

隔离库已分别复现：空库在 0005 失败，预先创建旧格式 `schema_migrations(version,name,applied_at)` 的库在 checksum 查询失败。

## P1：OAuth 授权不可持续且并发约束不可靠

### P1-01：第二次 OAuth 回调会违反 `oauth_sessions.state` 唯一约束

表定义将 `state` 声明为 `NOT NULL UNIQUE`，[`0001_init.sql`](/Users/allen/Downloads/Agent_Worker/subai/migrations/0001_init.sql:313)。但 [`CompleteCallback`](/Users/allen/Downloads/Agent_Worker/subai/internal/accounts/oauth.go:148) 将每个成功会话更新为 `state=''`。第一个回调留下空字符串后，第二个回调写同一个空字符串会触发唯一键冲突，整个 token 更新事务回滚。

最小 PostgreSQL 表复现已经确认第二次 `UPDATE … SET state=''` 返回 duplicate key。应保留原随机 state（它不含凭据），或使 state 可空并将已完成行写为 NULL；不要为所有完成会话写同一个唯一值。

### P1-02：每账号一个 pending OAuth session 仍然存在 check-then-insert 竞争

[`StartSession`](/Users/allen/Downloads/Agent_Worker/subai/internal/accounts/oauth.go:70) 先单独 `SELECT EXISTS`，之后才单独 `INSERT`。两次并发请求可同时读到“没有 pending”，然后各自插入。数据库没有 `(account_id) WHERE status='pending'` 的部分唯一索引，也没有账号行锁保护该临界区。

隔离库 48 个并发 `StartSession` 调用已得到多个成功会话。旧授权后完成仍可覆盖新凭据。应以事务锁定账号行后检查/插入，或用部分唯一索引实现数据库级约束，再将 unique violation 映射为可理解的 409。

## P2：一致性与运维问题

### P2-01：恢复摘要把失败请求记为完成

失败恢复路径已经正确写回 `failed_after_dispatch`，但 [`OnStartupRecovery`](/Users/allen/Downloads/Agent_Worker/subai/internal/gateway/state.go:143) 在 `case "failed"` 时增加 `summary.Completed`。启动日志和运营指标会把上游失败伪报为成功。应为 failed 增加独立计数，或至少不要计入 completed。

### P2-02：失败终态未从 active-state 索引中移除

第 4 份迁移新增 `failed_after_dispatch`，但 [`idx_requests_state`](/Users/allen/Downloads/Agent_Worker/subai/migrations/0001_init.sql:334) 的 partial predicate 没有把它列为终态。失败记录会持续占据为活跃状态优化的索引，长期会增加调度/恢复相关查询成本。需要单独迁移重建该索引。

### P2-03：部署示例仍列出已删除且重复的配置

[`config.go`](/Users/allen/Downloads/Agent_Worker/subai/internal/config/config.go:53) 已移除 `SUBAI_ADMIN_ORIGIN`、价格同步与日志保留配置，但 [`deploy/.env.example`](/Users/allen/Downloads/Agent_Worker/subai/deploy/.env.example:13) 仍包含这些变量，且 output/price/retention 三项重复出现。运维人员会误以为配置生效。应同步示例、README 与实际 Config。

## 已验证的改善

- `ContentHMAC` 已改为带长度前缀的编码，消除了第七轮复现的 segment 边界碰撞。
- `terminal_outcome` 已在结算前持久化，恢复逻辑能够按该字段恢复 `failed_after_dispatch`。
- 登录失败桶改为淘汰最旧项，优于随机 map 删除。
- storage loader 已有全目录预检和新安装的 checksum 写入路径。
- `go test -race ./internal/... ./rules/...`、前端 production build、`make lint` 和 Go server build 均通过。

## 验证结果

| 检查 | 结果 |
|---|---|
| 空库 migration / integration 启动 | 失败：0005 partial index 使用 `now()` |
| 旧 metadata 升级探针 | 失败：缺少 checksum 列 |
| 第二次 OAuth state 清理 SQL 探针 | 失败：unique violation |
| 并发 OAuth session 探针 | 复现多个 pending session |
| `go test -race ./internal/... ./rules/...` | 通过 |
| `npm --prefix web run build` | 通过 |
| `make lint` | 通过 |
| `go build -o /tmp/subai-review8-server ./cmd/server` | 通过 |
