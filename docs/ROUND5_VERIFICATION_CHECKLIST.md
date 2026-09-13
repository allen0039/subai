# Round 5 修复验证清单

**审查报告**: `INDEPENDENT_REVIEW_ROUND5_2026-09-12.md`  
**修复总结**: `ROUND5_FIXES_SUMMARY.md`  
**验证日期**: _______________

---

## 前置条件

- [ ] 所有代码已合并到目标分支
- [ ] 数据库迁移已应用（无新迁移，使用现有表）
- [ ] 测试数据库环境已配置（`TEST_DATABASE_URL`）
- [ ] Staging 环境已部署

---

## R5-01: 缓存令牌定价计算 ✓ [CRITICAL]

### 代码验证
- [x] `UsageFromTotal` 使用 `total - cached` 而非 `total + cached`
- [x] 添加负值检查
- [x] 添加 `cached > total` 检查
- [x] 函数签名未改变（向后兼容）

**验证人**: __________ **日期**: __________

### 单元测试验证
```bash
go test ./internal/billing -v -run TestUsageFromTotal
go test ./internal/billing -v -run TestModelPriceCost
```

- [ ] 所有测试通过
- [ ] 测试覆盖率 > 90%
- [ ] 边界情况全覆盖（负值、零值、cached > total）

**验证人**: __________ **日期**: __________

### 集成测试验证
```bash
go test ./tests/integration -v -run TestR5CachedAdjustmentCost
```

**测试用例**: 100 total, 80 cached, 0 output
- [ ] 期望成本: $0.000056
- [ ] 实际成本: __________
- [ ] 数据库 `usage_ledger` 记录正确：
  - [ ] `input_tokens = 20`
  - [ ] `cached_input_tokens = 80`
  - [ ] `cost = 0.000056`

**验证人**: __________ **日期**: __________

### 财务影响评估
```sql
-- 识别受影响的记录
SELECT 
    COUNT(*) as affected_count,
    SUM(cost) as total_charged,
    SUM(/* 重新计算的成本 */) as should_be_charged,
    SUM(cost) - SUM(/* 重新计算 */) as overcharge_amount
FROM usage_ledger
WHERE cached_input_tokens > 0
  AND created_at >= '2026-09-01'
  AND entry_type = 'charge';
```

- [ ] 受影响记录数: __________
- [ ] 过度收费总额: $ __________
- [ ] 退款流程已启动: [ ] 是 [ ] 否
- [ ] 退款完成日期: __________

**验证人**: __________ **日期**: __________

---

## R5-02: 隔离账号选择和激活 ✓ [HIGH]

### 代码验证

#### 调度器过滤
**文件**: `internal/scheduler/scheduler.go`

- [x] `loadAccount` 包含 `NOT EXISTS (SELECT 1 FROM account_holds ...)`
- [x] `groupAccounts` 包含 `NOT EXISTS (SELECT 1 FROM account_holds ...)`
- [x] 过滤逻辑在 SQL 层而非应用层（性能）

**验证人**: __________ **日期**: __________

#### 管理 API 阻止激活
**文件**: `internal/admin/resources.go`

- [x] `patchAccount` 在激活前检查 holds
- [x] 检查发生在事务内部
- [x] 返回 409 状态码和描述性错误消息

**验证人**: __________ **日期**: __________

### 集成测试验证
```bash
go test ./tests/integration -v -run TestR5HoldBlocksActivationAndSelection
```

- [ ] 添加 hold 后无法激活账号（返回 409）
- [ ] 添加 hold 后调度器无法选择账号（返回 `ErrNoHealthyAccount`）
- [ ] 测试覆盖所有 hold 原因类型

**验证人**: __________ **日期**: __________

### 生产环境验证

#### 手动测试步骤
1. 创建测试账号并添加路由
2. 通过 API 添加 hold：
   ```bash
   curl -X POST /api/admin/accounts/{id}/holds \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"reason":"admin_pause","details":{"note":"test"}}'
   ```
3. 尝试激活账号：
   ```bash
   curl -X PATCH /api/admin/accounts/{id} \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"state":"active","version":1}'
   ```
   - [ ] 返回 409 错误
   - [ ] 错误消息: "cannot set state to active: account has unresolved hold reasons"

4. 发送测试请求（使用该账号的 API key）：
   - [ ] 请求失败或选择了其他账号
   - [ ] 日志包含 "no healthy account" 或类似消息

5. 移除 hold：
   ```bash
   curl -X DELETE /api/admin/accounts/{id}/holds/admin_pause \
     -H "Authorization: Bearer $TOKEN"
   ```
6. 激活账号：
   - [ ] 成功返回 200
   - [ ] 后续请求可以使用该账号

**验证人**: __________ **日期**: __________

---

## R5-03: 测试隔离 - account_holds 清理 ✓ [MEDIUM]

### 代码验证
**文件**: `tests/integration/integration_test.go`

- [x] `setupTestDatabase` 包含 `"account_holds"` 在 TRUNCATE 列表中
- [x] `account_holds` 的清理顺序正确（在 `accounts` 之前）

**验证人**: __________ **日期**: __________

### 测试隔离验证
```bash
# 运行多次以检测不确定性
for i in {1..5}; do
  go test ./tests/integration -v -run TestR5HoldBlocksActivationAndSelection
done
```

- [ ] 所有 5 次运行都通过
- [ ] 无残留数据警告
- [ ] 无外键约束错误

**验证人**: __________ **日期**: __________

---

## R5-04: 测试配置灵活性 ✓ [MEDIUM]

### 代码验证
**文件**: `tests/integration/round4_fixes_test.go`

- [x] 无 `testPool` 变量定义
- [x] 所有测试使用 `newTestEnv(t)` 获取数据库连接
- [x] 无硬编码的 `localhost/subai_test` 连接字符串

**验证人**: __________ **日期**: __________

### 配置灵活性验证
```bash
# 测试 1: 使用默认 TEST_DATABASE_URL
export TEST_DATABASE_URL="postgres://user:pass@localhost/subai_test"
go test ./tests/integration -v -run TestR4

# 测试 2: 使用不同的数据库
export TEST_DATABASE_URL="postgres://user:pass@testdb.example.com/subai_ci"
go test ./tests/integration -v -run TestR4
```

- [ ] 两种配置都能运行测试
- [ ] 无连接错误
- [ ] 测试结果一致

**验证人**: __________ **日期**: __________

---

## R5-05: 管理端点输入验证 ✓ [MEDIUM]

### 代码验证
**文件**: `internal/admin/routes.go`

- [x] `/api/admin/proxies/{id}/test` 验证 UUID
- [x] `/api/admin/proxies/{id}` 验证 UUID
- [x] `/api/admin/audit/events/{id}/review` 验证 UUID
- [x] 验证发生在路径解析后、业务逻辑前
- [x] 返回 400 状态码和描述性错误

**验证人**: __________ **日期**: __________

### 集成测试验证
```bash
go test ./tests/integration -v -run TestR5AdminInputValidation
```

- [ ] 所有恶意输入被拒绝（返回 400）
- [ ] 错误消息明确（"invalid proxy ID" 等）
- [ ] 有效 UUID 仍可通过

**验证人**: __________ **日期**: __________

### 安全渗透测试

#### SQL 注入尝试
```bash
# 测试 1: 单引号
curl /api/admin/proxies/\' OR \'1\'=\'1/test
# 期望: 400 "invalid proxy ID"

# 测试 2: 联合查询
curl /api/admin/proxies/123\' UNION SELECT \*/test
# 期望: 400 "invalid proxy ID"
```

- [ ] 所有 SQL 注入尝试被阻止
- [ ] 无数据库错误泄露

**验证人**: __________ **日期**: __________

#### 路径遍历尝试
```bash
# 测试 1: 相对路径
curl /api/admin/proxies/../../../etc/passwd/test
# 期望: 400 "invalid proxy ID"

# 测试 2: 编码路径
curl /api/admin/proxies/%2e%2e%2f/test
# 期望: 400 "invalid proxy ID"
```

- [ ] 所有路径遍历尝试被阻止
- [ ] 无文件系统访问

**验证人**: __________ **日期**: __________

---

## R5-06: 相同优先级路由轮换 ✓ [LOW]

### 代码验证
**文件**: `internal/scheduler/scheduler.go`

- [x] `AcquireAccount` 使用 `r.Priority` 而非循环索引 `i`
- [x] 相同 priority 的路由获得相同 tier 值
- [x] `addAcct` 函数的 `order` 参数仍在递增（保持插入顺序）

**验证人**: __________ **日期**: __________

### 集成测试验证
```bash
go test ./tests/integration -v -run TestR5EqualPriorityRotation
```

**测试场景**: 2 个账号，都有 priority=100 的路由，发送 6 次请求

- [ ] 每个账号被选中恰好 3 次
- [ ] 选择分布均匀（无一个账号被连续选中 > 2 次）

**验证人**: __________ **日期**: __________

### 生产环境监控

#### 部署前基线
```sql
-- 记录当前账号选择分布
SELECT 
    a.label,
    COUNT(*) as request_count,
    AVG(r.priority) as avg_priority
FROM requests req
JOIN api_keys k ON k.id = req.api_key_id
JOIN key_routes r ON r.api_key_id = k.id
JOIN accounts a ON a.id = r.target_id
WHERE req.created_at >= now() - interval '1 hour'
  AND r.target_type = 'account'
GROUP BY a.label
ORDER BY request_count DESC;
```

- [ ] 基线数据已记录
- [ ] 部署前分布: __________

**验证人**: __________ **日期**: __________

#### 部署后验证
- [ ] 相同优先级的账号请求分布更均匀
- [ ] 无 "单一账号过载" 告警
- [ ] 无客户投诉关于账号选择不公平

**验证人**: __________ **日期**: __________

---

## 回归测试

### 完整测试套件
```bash
# 单元测试
go test ./internal/... -v -cover

# 集成测试
go test ./tests/integration/... -v
```

- [ ] 所有单元测试通过 (___/___通过)
- [ ] 所有集成测试通过 (___/___通过)
- [ ] 代码覆盖率 ≥ 80%

**验证人**: __________ **日期**: __________

### 端到端测试
- [ ] 正常请求流程（无缓存令牌）
- [ ] 缓存令牌请求流程
- [ ] 账号隔离流程（over_reserve → hold → resolve）
- [ ] 管理 API 完整流程
- [ ] 审核系统流程

**验证人**: __________ **日期**: __________

---

## 性能验证

### 调度器性能
```sql
-- 测量账号选择查询性能
EXPLAIN ANALYZE
SELECT a.id, a.label, a.state, a.concurrency_limit, a.priority
FROM accounts a
WHERE a.state = 'active'
  AND NOT EXISTS (SELECT 1 FROM account_holds h WHERE h.account_id = a.id)
LIMIT 100;
```

- [ ] 查询时间 < 10ms
- [ ] 使用索引（无 Seq Scan on accounts）
- [ ] `account_holds.account_id` 有索引

**验证人**: __________ **日期**: __________

### 定价计算性能
```bash
# 基准测试
go test ./internal/billing -bench=BenchmarkUsageFromTotal -benchmem
```

- [ ] 每次操作 < 100ns
- [ ] 无内存分配
- [ ] 性能与修复前相当

**验证人**: __________ **日期**: __________

---

## 监控和告警

### 新增指标
- [ ] `billing.usage_from_total_errors` 计数器
- [ ] `scheduler.account_holds_filtered` 计数器
- [ ] `admin.input_validation_errors` 计数器

### 告警规则
- [ ] `billing.usage_from_total_errors` > 10/min → P2
- [ ] `scheduler.no_healthy_account_errors` 增长 > 50% → P3
- [ ] `admin.input_validation_errors` > 100/hour → P4（可能的攻击）

**验证人**: __________ **日期**: __________

---

## 文档更新

- [x] `ROUND5_FIXES_SUMMARY.md` 已创建
- [x] 本验证清单已创建
- [ ] API 文档更新（定价公式）
- [ ] 运维手册更新（账号 hold 处理流程）
- [ ] 故障排查指南更新

**验证人**: __________ **日期**: __________

---

## 最终批准

### 技术审查
- [ ] 所有代码审查评论已解决
- [ ] 所有测试通过
- [ ] 性能回归检查通过
- [ ] 安全审查通过

**技术负责人**: __________ **日期**: __________

### 业务审查
- [ ] R5-01 财务影响已评估
- [ ] 退款流程已准备
- [ ] 客户沟通计划已准备（如需要）

**业务负责人**: __________ **日期**: __________

### 部署批准
- [ ] Staging 验证通过
- [ ] 回滚计划已准备
- [ ] 部署时间窗口已确定: __________

**部署负责人**: __________ **签名**: __________ **日期**: __________

---

## 部署后验证（生产环境）

### 立即验证（部署后 1 小时内）
- [ ] 服务健康检查通过
- [ ] 无错误率激增
- [ ] 定价计算正确（抽查 10 个缓存请求）
- [ ] 有 hold 的账号未被选择（检查日志）

**验证人**: __________ **日期**: __________

### 短期验证（部署后 24 小时内）
- [ ] 无客户投诉
- [ ] 财务指标正常
- [ ] 调度器分布均匀
- [ ] 无安全事件

**验证人**: __________ **日期**: __________

### 中期验证（部署后 1 周内）
- [ ] R5-01 退款完成
- [ ] 账号 hold 机制正常使用
- [ ] 无遗留问题

**验证人**: __________ **日期**: __________

---

## 问题追踪

| 问题编号 | 描述 | 严重程度 | 状态 | 负责人 | 截止日期 |
|---------|------|---------|------|--------|---------|
| | | | | | |
| | | | | | |
| | | | | | |

---

**清单版本**: 1.0  
**最后更新**: 2026-09-13  
**下次审查**: __________
