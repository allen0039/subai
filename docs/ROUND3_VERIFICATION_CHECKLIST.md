# Round 3 修复验证清单

## 快速验证步骤

### 1. 数据库迁移
```bash
# 应用迁移
migrate -path ./migrations -database "postgresql://user:pass@localhost/subai" up

# 验证表存在
psql -d subai -c "\d account_holds"
```

预期输出：
- 表包含 (account_id, reason, details, created_at)
- PRIMARY KEY (account_id, reason)
- CHECK constraint on reason

---

### 2. 编译检查
```bash
go build ./...
```
✅ 通过 - 无编译错误

---

### 3. 代码审查要点

#### R3-01: 隔离原因追踪
- [ ] `billing.Settle()` 在 over-reserve 时插入 `over_reserve` hold
- [ ] `billing.MarkUnknownWithAccount()` 插入 `unknown_pending` hold
- [ ] `admin.AdjustUnknown()` 解决时移除对应 hold
- [ ] `gateway.convergeUnknown()` 传递 accountID

关键文件：
- `internal/billing/reserve.go:143-180` (Settle)
- `internal/billing/reserve.go:123-145` (MarkUnknownWithAccount)
- `internal/admin/audit_handlers.go:245-280` (AdjustUnknown)
- `internal/gateway/handler.go:convergeUnknown`

#### R3-02: OAuth 刷新保护
- [ ] `accounts.Refresh()` 仅更新 `state='active'` 的账号
- [ ] WHERE 子句包含 `state='active'`

关键文件：
- `internal/accounts/oauth.go:95-110`

#### R3-03: Usage 防御
- [ ] `upstream.Do()` 始终从响应填充 usage
- [ ] 添加零值检查防御

关键文件：
- `internal/upstream/anthropic.go:150-170`

#### R3-04: 管理路由分组
- [ ] `adjust-unknown` 端点在 `adminRoutes` 组
- [ ] 确保 `requireAdmin` 中间件应用

关键文件：
- `internal/admin/server.go:registerRoutes`

#### R3-05: 测试隔离
- [ ] 测试使用独立时间而非共享全局变量

关键文件：
- `tests/integration/review_fixes_test.go`

---

### 4. 集成测试
```bash
# 运行 Round 3 修复测试
go test ./tests/integration -v -run TestR3

# 运行完整测试套件
go test ./... -v
```

---

### 5. 手动验证场景

#### 场景 1：Over-reserve 触发 hold
```bash
# 1. 创建账号和预算
# 2. Reserve $10
# 3. Settle with actual cost $20
# 4. 验证：
psql -d subai -c "SELECT * FROM account_holds WHERE reason='over_reserve';"
psql -d subai -c "SELECT state FROM accounts WHERE id='...';" # 应为 recovery_hold
```

#### 场景 2：Unknown 触发 hold
```bash
# 1. 创建请求，Reserve 成功
# 2. 上游调用失败，触发 convergeUnknown
# 3. 验证：
psql -d subai -c "SELECT * FROM account_holds WHERE reason='unknown_pending';"
psql -d subai -c "SELECT state FROM reservations WHERE request_id='...';" # 应为 unknown
```

#### 场景 3：OAuth 刷新不清除隔离
```bash
# 1. 设置账号为 recovery_hold
psql -d subai -c "UPDATE accounts SET state='recovery_hold' WHERE id='...';"
# 2. 触发 OAuth 刷新（或调用 accounts.Refresh）
# 3. 验证：
psql -d subai -c "SELECT state FROM accounts WHERE id='...';" # 仍为 recovery_hold
```

#### 场景 4：Admin 端点权限
```bash
# 无 admin token - 应失败
curl -X POST http://localhost:8080/admin/accounts/xxx/adjust-unknown \
  -H "Content-Type: application/json" \
  -d '{"adjust":"refund"}'

# 有 admin token - 应成功
curl -X POST http://localhost:8080/admin/accounts/xxx/adjust-unknown \
  -H "Authorization: Bearer admin-token" \
  -H "Content-Type: application/json" \
  -d '{"adjust":"refund"}'
```

---

### 6. 监控指标 (生产环境)

部署后监控：
```sql
-- Hold 分布
SELECT reason, count(*) FROM account_holds GROUP BY reason;

-- Unknown 请求年龄
SELECT 
  r.request_id,
  q.account_id,
  r.created_at,
  now() - r.created_at as age
FROM reservations r
JOIN requests q ON q.id = r.request_id
WHERE r.state = 'unknown'
ORDER BY age DESC;

-- 隔离账号清单
SELECT 
  a.id,
  a.name,
  a.state,
  array_agg(h.reason) as hold_reasons
FROM accounts a
LEFT JOIN account_holds h ON h.account_id = a.id
WHERE a.state = 'recovery_hold'
GROUP BY a.id, a.name, a.state;
```

---

## 回滚计划

如果出现问题：

1. **代码回滚**：
```bash
git revert <commit-hash>
```

2. **数据库回滚**：
```bash
migrate -path ./migrations -database "..." down 1
```

3. **验证回滚**：
```bash
psql -d subai -c "\dt account_holds" # 应不存在
go build ./... # 应编译通过
```

---

## 完成标准

- [x] 所有代码编译通过
- [ ] 数据库迁移成功应用
- [ ] 集成测试通过
- [ ] 手动场景验证通过
- [ ] Code review 批准
- [ ] Staging 环境验证
- [ ] 生产部署
- [ ] 监控指标正常

---

## 联系人

- 技术负责人：[填写]
- 数据库负责人：[填写]
- 运维负责人：[填写]
