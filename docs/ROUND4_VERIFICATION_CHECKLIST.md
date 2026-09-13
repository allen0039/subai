# Round 4 修复验证清单

**修复日期**: 2026-09-12  
**验证负责人**: _________  
**验证日期**: _________

---

## 编译与静态检查

- [x] **编译通过**
  ```bash
  go build ./...
  ```
  - 结果: ✅ 无错误

- [ ] **类型检查**
  ```bash
  go vet ./...
  ```
  - 结果: _________

- [ ] **Lint检查**
  ```bash
  golangci-lint run
  ```
  - 结果: _________

---

## R4-01: 迁移加载顺序与schema一致性

### 文件检查

- [x] **删除误创建的 down 文件**
  - [ ] `0001_initial.down.sql` 已删除
  - [ ] `git status` 确认文件不存在

- [x] **schema 统一**
  - [ ] 检查 `0002_account_holds.up.sql`:
    ```sql
    CREATE TABLE account_holds (
        account_id UUID NOT NULL REFERENCES accounts(id),
        reason TEXT NOT NULL,
        details JSONB NOT NULL DEFAULT '{}',
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        PRIMARY KEY (account_id, reason)
    );
    ```
  - [ ] 字段名与代码中 `AccountHold` 结构体匹配
  - [ ] 主键约束正确 (account_id + reason)

### 迁移测试

- [ ] **干净数据库测试**
  ```bash
  # 创建测试数据库
  createdb subai_migration_test
  
  # 运行迁移
  DATABASE_URL=postgres://localhost/subai_migration_test ./subai migrate
  
  # 检查表结构
  psql subai_migration_test -c "\d account_holds"
  ```
  - 预期列: `account_id`, `reason`, `details`, `created_at`
  - 预期主键: `account_holds_pkey ON (account_id, reason)`
  - 预期外键: `account_holds_account_id_fkey REFERENCES accounts(id)`

- [ ] **枚举值测试**
  ```sql
  -- 插入三种 reason 类型
  INSERT INTO account_holds (account_id, reason, details)
  VALUES 
    ((SELECT id FROM accounts LIMIT 1), 'over_reserve', '{}'),
    ((SELECT id FROM accounts LIMIT 1), 'unknown_pending', '{}'),
    ((SELECT id FROM accounts LIMIT 1), 'admin_action', '{}');
  
  -- 查询确认
  SELECT reason, COUNT(*) FROM account_holds GROUP BY reason;
  ```
  - 预期: 三行结果，每种 reason 一行

- [ ] **幂等性测试**
  ```bash
  # 再次运行迁移
  ./subai migrate
  ```
  - 预期: 0002 跳过（已应用）
  - 无错误

---

## R4-04: 百分比预算查询无 conn busy

### 单元测试

- [ ] **运行测试**
  ```bash
  go test ./tests/integration -run TestR4_04_PercentBudgetQueryNoBusy -v
  ```
  - 预期: PASS
  - 无 "conn busy" 错误

### 集成测试

- [ ] **创建测试场景**
  ```sql
  -- 创建测试账户和密钥
  INSERT INTO accounts (name, state) VALUES ('test-pct-account', 'active')
  RETURNING id;
  INSERT INTO keys (account_id, name, key_hash, state)
  VALUES ('<account-id>', 'test-key', 'hash', 'active')
  RETURNING id;
  
  -- 创建基础固定策略
  INSERT INTO budget_policies (owner_type, owner_account_id, period, timezone, mode, amount, status)
  VALUES ('account', '<account-id>', 'month', 'UTC', 'fixed', 1000.00, 'active')
  RETURNING id;
  
  -- 创建百分比策略（25%）
  INSERT INTO budget_policies (owner_type, owner_key_id, period, timezone, mode, percent_bps, base_policy_id, status)
  VALUES ('key', '<key-id>', 'month', 'UTC', 'percent', 2500, '<base-policy-id>', 'active');
  ```

- [ ] **触发预算检查**
  ```bash
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer sk-test-key" \
    -H "Content-Type: application/json" \
    -d '{"model": "claude-3-sonnet", "messages": [{"role": "user", "content": "hi"}]}'
  ```
  - 预期: 请求成功（200）
  - 日志无 "conn busy" 错误

- [ ] **检查日志**
  ```bash
  grep "conn busy" /var/log/subai/app.log
  ```
  - 预期: 无结果

---

## R4-05: 审核规则动作完整处理

### 单元测试

- [ ] **运行测试**
  ```bash
  go test ./tests/integration -run TestR4_05_AuditRuleActionHandling -v
  ```
  - 预期: 所有动作类型 PASS
  - `block` → DecisionBlock
  - `review` → DecisionReview
  - `reject` → DecisionBlock
  - `unsupported` → DecisionUnsupported
  - `flag` → 继续处理，记录 hit

### 功能测试

- [ ] **测试 review 动作**
  - 创建规则: `{"pattern": "sensitive-data", "action": "review"}`
  - 发送包含 "sensitive-data" 的请求
  - 预期响应: `{"decision": "review", "hits": [...]}`

- [ ] **测试 unsupported 动作**
  - 创建规则: `{"pattern": "binary-content", "action": "unsupported"}`
  - 发送包含 "binary-content" 的请求
  - 预期响应: `{"decision": "unsupported", "hits": [...]}`

- [ ] **测试 flag 动作**
  - 创建规则: `{"pattern": "debug-mode", "action": "flag"}`
  - 发送包含 "debug-mode" 的请求
  - 预期: 请求继续处理，hits 中记录该规则

---

## R4-06: Usage 只从终态事件提取

### 单元测试

- [ ] **运行测试**
  ```bash
  go test ./tests/integration -run TestR4_06_UsageOnlyFromTerminalEvents -v
  ```
  - 预期: PASS
  - `response.completed` → ok=true
  - `response.failed` → ok=true
  - `response.content_block_delta` → ok=false
  - `ping` → ok=false

### 集成测试

- [ ] **监控 SSE 流**
  ```bash
  curl -N http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer sk-test-key" \
    -H "Content-Type: application/json" \
    -d '{"model": "claude-3-sonnet", "messages": [{"role": "user", "content": "Count to 10"}], "stream": true}'
  ```
  - 收集所有事件
  - 检查 `response.content_block_delta` 事件是否包含 usage（不应该提取）
  - 检查 `response.completed` 事件的 usage 是否被记录到 `requests` 表

- [ ] **数据库验证**
  ```sql
  -- 查询最近的流式请求
  SELECT id, input_tokens, output_tokens, cached_tokens
  FROM requests
  WHERE stream = true
  ORDER BY created_at DESC
  LIMIT 1;
  ```
  - 预期: usage 字段非 NULL
  - 值与 SSE `response.completed` 事件中的 usage 一致

---

## R4-07: 负 token 验证

### 单元测试

- [ ] **运行测试**
  ```bash
  go test ./tests/integration -run TestR4_07_NegativeTokenValidation -v
  ```
  - 预期: PASS
  - 负值被拒绝
  - `cached > input` 被拒绝
  - 合法值通过

### API 测试

- [ ] **测试负 input_tokens**
  ```bash
  curl -X POST http://localhost:8080/admin/audit/requests/<request-id>/adjust-unknown \
    -H "Authorization: Bearer <admin-token>" \
    -H "Content-Type: application/json" \
    -d '{"input_tokens": -100, "output_tokens": 50, "note": "test"}'
  ```
  - 预期响应: `400 Bad Request`
  - 错误信息: "token counts must be non-negative"

- [ ] **测试负 cached_tokens**
  ```bash
  curl -X POST .../adjust-unknown \
    -d '{"input_tokens": 100, "cached_input_tokens": -10, "output_tokens": 50, "note": "test"}'
  ```
  - 预期响应: `400 Bad Request`
  - 错误信息: "token counts must be non-negative"

- [ ] **测试 cached > input**
  ```bash
  curl -X POST .../adjust-unknown \
    -d '{"input_tokens": 100, "cached_input_tokens": 150, "output_tokens": 50, "note": "test"}'
  ```
  - 预期响应: `400 Bad Request`
  - 错误信息: "cached_input_tokens cannot exceed input_tokens"

- [ ] **测试合法值**
  ```bash
  curl -X POST .../adjust-unknown \
    -d '{"input_tokens": 100, "cached_input_tokens": 20, "output_tokens": 50, "note": "manual adjustment"}'
  ```
  - 预期响应: `200 OK`
  - 响应包含调整后的金额

---

## 回归测试

### Round 3 修复验证

- [ ] **R3-01: 隔离原因追踪**
  ```bash
  go test ./tests/integration -run TestR3_01_IsolationReasonTracking -v
  ```
  - 预期: PASS

- [ ] **R3-02: OAuth 覆盖状态**
  ```bash
  go test ./tests/integration -run TestR3_02_OAuthRefreshPreservesIsolation -v
  ```
  - 预期: PASS

- [ ] **R3-03: 空 usage 传递**
  ```bash
  go test ./tests/integration -run TestR3_03_UsageExtractionValidation -v
  ```
  - 预期: PASS

### 核心功能回归

- [ ] **完整请求流程**
  - 非流式请求成功
  - 流式请求成功
  - 审核触发正常
  - 计费记录正确

- [ ] **预算检查**
  - 固定预算策略生效
  - 百分比预算策略生效
  - 超限拒绝正确

- [ ] **账户隔离**
  - 隔离状态阻止请求
  - 恢复流程清除 hold 原因
  - 多原因隔离需全部清除

---

## 性能测试

- [ ] **并发请求测试**
  ```bash
  # 50 个并发请求
  ab -n 1000 -c 50 -H "Authorization: Bearer sk-test-key" \
    -p request.json -T application/json \
    http://localhost:8080/v1/chat/completions
  ```
  - 预期: 无 "conn busy" 错误
  - 95th percentile < 500ms

- [ ] **百分比预算查询性能**
  - 创建 10 个百分比策略引用同一基础策略
  - 触发预算检查
  - 查询延迟 < 10ms

---

## 监控指标

### 错误率

- [ ] **应用错误日志**
  ```bash
  grep "ERROR" /var/log/subai/app.log | tail -50
  ```
  - 预期: 无新增错误类型

- [ ] **数据库错误**
  ```bash
  grep "conn busy" /var/log/postgresql/postgresql.log
  ```
  - 预期: 无结果

### 业务指标

- [ ] **请求成功率**
  ```sql
  SELECT
    COUNT(*) FILTER (WHERE status='completed') AS completed,
    COUNT(*) FILTER (WHERE status='failed') AS failed,
    COUNT(*) FILTER (WHERE status='unknown') AS unknown
  FROM requests
  WHERE created_at > NOW() - INTERVAL '1 hour';
  ```
  - 预期: unknown 比例 < 0.1%

- [ ] **审核决策分布**
  ```sql
  SELECT decision, COUNT(*)
  FROM audit_events
  WHERE created_at > NOW() - INTERVAL '1 hour'
  GROUP BY decision;
  ```
  - 预期: review/unsupported 决策正常记录

---

## 文档验证

- [ ] **修复总结完整性**
  - [ ] `ROUND4_FIXES_SUMMARY.md` 存在
  - [ ] 所有 7 个问题都有详细说明
  - [ ] 包含部署建议和回滚计划

- [ ] **测试覆盖文档**
  - [ ] `round4_fixes_test.go` 包含所有测试用例
  - [ ] 测试用例与问题 ID 对应

- [ ] **迁移文档**
  - [ ] `0002_account_holds.up.sql` 有注释
  - [ ] `0002_account_holds.down.sql` 是幂等的

---

## 部署前检查

- [ ] **代码审查**
  - [ ] 所有修改经过 peer review
  - [ ] 无遗留 TODO/FIXME

- [ ] **配置检查**
  - [ ] staging 环境配置正确
  - [ ] 生产环境配置就绪

- [ ] **备份计划**
  - [ ] 数据库备份完成
  - [ ] 回滚脚本就绪

- [ ] **通知**
  - [ ] 通知运维团队部署窗口
  - [ ] 通知客户支持团队可能的影响

---

## 部署后验证

- [ ] **烟雾测试** (部署后 5 分钟内)
  - [ ] 健康检查端点响应 200
  - [ ] 创建测试请求成功
  - [ ] 日志无 FATAL/PANIC

- [ ] **监控告警** (部署后 30 分钟内)
  - [ ] 错误率 < 0.5%
  - [ ] 响应时间 p95 < 1s
  - [ ] 无数据库连接池耗尽

- [ ] **业务验证** (部署后 1 小时内)
  - [ ] 账单记录正常
  - [ ] 审核决策正确
  - [ ] 客户无异常反馈

---

## 签收

### 开发验证

- [ ] 所有单元测试通过
- [ ] 所有集成测试通过
- [ ] 代码审查完成

**签字**: _________ **日期**: _________

### QA 验证

- [ ] 功能测试通过
- [ ] 回归测试通过
- [ ] 性能测试通过

**签字**: _________ **日期**: _________

### 运维验证

- [ ] staging 部署成功
- [ ] 生产部署成功
- [ ] 监控告警正常

**签字**: _________ **日期**: _________

---

## 问题跟踪

| 发现时间 | 问题描述 | 严重程度 | 状态 | 解决方案 |
|---------|---------|---------|------|---------|
| | | | | |

---

**文档版本**: 1.0  
**最后更新**: 2026-09-12  
**下次审查**: 部署后 7 天
