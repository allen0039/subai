# Round 3 审查修复总结
时间：2026-09-12

## 修复清单

### R3-01：隔离原因追踪 (CRITICAL)
**问题**：recovery_hold 缺少结构化原因追踪，恢复流程盲目。

**修复**：
- 新增 `account_holds` 表，记录三种原因：
  - `over_reserve`：实际费用超预留（§18.3）
  - `unknown_pending`：未解决的 unknown 请求（§19）
  - `admin_action`：手动隔离
- 修改 `billing.Settle()`：over-reserve 时插入 hold 记录
- 修改 `billing.MarkUnknownWithAccount()`：标记 unknown 时插入 hold 记录
- 修改 `admin.AdjustUnknown()`：解决时移除对应的 hold
- 修改 `gateway.convergeUnknown()`：传递 accountID 以建立 hold

**文件**：
- `migrations/000011_account_holds.up.sql`
- `internal/billing/reserve.go` - MarkUnknownWithAccount, Settle
- `internal/gateway/handler.go` - convergeUnknown
- `internal/admin/audit_handlers.go` - AdjustUnknown

**验证**：
```sql
-- 检查 hold 原因
SELECT account_id, reason, details FROM account_holds;

-- 验证恢复流程
-- 1. over-reserve 触发 hold
-- 2. 解决所有 unknown 后移除 unknown_pending
-- 3. AccountHasOpenUnknowns=false 且 holds 为空时可恢复
```

---

### R3-02：OAuth 刷新覆盖隔离状态 (HIGH)
**问题**：`accounts.Refresh()` 无条件设置 `state='active'`，清除 `recovery_hold`。

**修复**：
- 修改 `internal/accounts/oauth.go:Refresh()`
- 添加条件：`WHERE state='active'`，仅刷新活跃账号
- 隔离账号的 OAuth token 更新但状态保持不变

**验证**：
```sql
-- 隔离账号刷新后状态不变
UPDATE accounts SET state='recovery_hold' WHERE id='...';
-- 执行 OAuth 刷新
SELECT state FROM accounts WHERE id='...'; -- 仍为 recovery_hold
```

---

### R3-03：空 usage 传递 (MEDIUM)
**问题**：上游请求失败时，空 `usage{}` 导致费用计算错误。

**修复**：
- 修改 `internal/upstream/anthropic.go:Do()`
- 确保始终从 API 响应填充 usage
- 添加默认值防御：`if usage.InputTokens == 0 && usage.OutputTokens == 0`

**验证**：
```go
// 测试失败响应
resp, err := upstream.Do(ctx, req)
require.NotZero(t, resp.Usage.InputTokens)
require.NotZero(t, resp.Usage.OutputTokens)
```

---

### R3-04：管理端路由权限混乱 (MEDIUM)
**问题**：
- `POST /admin/accounts/:id/adjust-unknown` 应在管理路由组
- 当前在用户路由组，绕过管理权限

**修复**：
- 移动到 `adminRoutes` 组
- 确保 `requireAdmin` 中间件生效

**文件**：
- `internal/admin/server.go` - registerRoutes

**验证**：
```bash
# 无 admin token 应返回 401
curl -X POST http://localhost:8080/admin/accounts/.../adjust-unknown

# 有 admin token 应返回 200
curl -H "Authorization: Bearer admin-token" \
     -X POST http://localhost:8080/admin/accounts/.../adjust-unknown
```

---

### R3-05：测试数据竞争 (LOW)
**问题**：`TestAuditResolveUnknown` 修改共享 `testNow()` 返回值。

**修复**：
- 使用独立的 `time.Now()` 而非全局固定时间
- 或使用测试专用的 `nowFunc` 参数

**文件**：
- `tests/integration/review_fixes_test.go`

**验证**：
```bash
go test -race ./tests/integration -run TestAuditResolveUnknown
```

---

## 迁移步骤

1. **应用数据库迁移**：
```bash
migrate -path ./migrations -database "postgresql://..." up
```

2. **验证表结构**：
```sql
\d account_holds
-- 确认 PRIMARY KEY (account_id, reason)
```

3. **运行测试**：
```bash
go test ./tests/integration -v -run TestR3
```

4. **部署代码**：
   - 重启 gateway 服务
   - 重启 admin 服务

---

## 遗留注意事项

1. **历史隔离账号**：现有 `recovery_hold` 账号无 hold 记录
   - 需要手动迁移或在首次触发时补充

2. **恢复流程**：管理员需检查 `account_holds` 表
   ```sql
   -- 安全恢复检查
   SELECT * FROM account_holds WHERE account_id='...';
   SELECT count(*) FROM reservations r
   JOIN requests q ON q.id=r.request_id
   WHERE q.account_id='...' AND r.state='unknown';
   ```

3. **监控指标**：建议添加：
   - `account_holds_by_reason` - 按原因分类的 hold 数量
   - `unknown_pending_age` - unknown 请求停留时长

---

## 回归风险评估

- **R3-01**：LOW - 新表和字段，不影响现有流程
- **R3-02**：LOW - 仅限制 OAuth 刷新范围
- **R3-03**：MEDIUM - usage 默认值可能影响费用计算（需仔细测试）
- **R3-04**：LOW - 路由移动，权限更严格
- **R3-05**：NONE - 仅测试代码

---

## 下一步

1. Code review 所有修改
2. 在 staging 环境运行完整测试套件
3. 监控 production 的 `account_holds` 表增长
4. 更新运维文档：隔离恢复 SOP
