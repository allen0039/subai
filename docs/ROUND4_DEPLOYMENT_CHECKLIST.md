# Round 4 部署检查清单

**版本**: 1.0  
**生成时间**: 2026-09-12  
**适用环境**: Staging → Production

---

## 📋 部署前准备

### 1. 代码审查
- [ ] 所有 7 个修复的代码变更已通过团队审查
- [ ] Git diff 确认没有意外的调试代码或注释
- [ ] 所有新文件已加入版本控制
- [ ] 确认分支从 `main` 最新代码合并

### 2. 测试验证
- [ ] 单元测试通过: `go test ./internal/...`
- [ ] 集成测试通过: `go test ./tests/integration -v`
- [ ] 编译无错误: `go build ./...`
- [ ] 静态检查通过: `go vet ./...`

### 3. 依赖检查
- [ ] 无新的外部依赖引入
- [ ] `go.mod` 和 `go.sum` 无意外变化
- [ ] 所有导入路径正确

---

## 🔧 Staging 环境部署

### Phase 1: 数据库迁移 (估计 5 分钟)

#### 1.1 备份数据库
```bash
# 创建快照
pg_dump subai_staging > backup_$(date +%Y%m%d_%H%M%S).sql

# 验证备份
ls -lh backup_*.sql
```
- [ ] 数据库备份完成
- [ ] 备份文件大小合理（> 0 字节）
- [ ] 备份存储在安全位置

#### 1.2 应用迁移
```bash
# 运行新迁移
go run cmd/migrate/main.go up

# 验证迁移状态
go run cmd/migrate/main.go status
```
- [ ] 迁移 `014_create_account_holds.sql` 成功应用
- [ ] 表 `account_holds` 已创建
- [ ] 索引 `idx_account_holds_reason` 已创建
- [ ] 无迁移错误或警告

#### 1.3 验证模式
```sql
-- 检查表结构
\d account_holds

-- 预期输出:
-- Column     | Type         | Collation | Nullable | Default
-- -----------+--------------+-----------+----------+---------
-- account_id | uuid         |           | not null |
-- reason     | text         |           | not null |
-- details    | jsonb        |           | not null | '{}'::jsonb
-- created_at | timestamptz  |           | not null | now()

-- 检查约束
SELECT conname, contype FROM pg_constraint 
WHERE conrelid = 'account_holds'::regclass;
```
- [ ] 4 个列存在且类型正确
- [ ] 主键约束存在: `(account_id, reason)`
- [ ] 外键约束存在: `account_id -> accounts(id)`

### Phase 2: 应用部署 (估计 10 分钟)

#### 2.1 构建新版本
```bash
# 设置版本号
export VERSION=v1.4.0-round4

# 构建
go build -o subai-gateway-${VERSION} ./cmd/gateway
go build -o subai-admin-${VERSION} ./cmd/admin

# 验证二进制
./subai-gateway-${VERSION} --version
```
- [ ] 构建成功，无错误
- [ ] 二进制文件可执行
- [ ] 版本号正确

#### 2.2 滚动更新（零停机）
```bash
# 1. 启动新实例（监听不同端口）
./subai-gateway-${VERSION} --port 8081 &
NEW_PID=$!

# 2. 健康检查
curl http://localhost:8081/health
# 预期: {"status":"ok"}

# 3. 逐步切换流量（假设使用 nginx）
# 编辑 nginx.conf，添加新 upstream
# upstream gateway {
#   server localhost:8080 weight=1;  # 旧实例
#   server localhost:8081 weight=1;  # 新实例
# }
sudo nginx -s reload

# 4. 观察 5 分钟，监控错误日志
tail -f /var/log/subai/gateway.log

# 5. 完全切换到新实例
# 从 nginx upstream 移除旧实例
# upstream gateway {
#   server localhost:8081;
# }
sudo nginx -s reload

# 6. 优雅关闭旧实例
kill -SIGTERM $OLD_PID
```
- [ ] 新实例启动成功
- [ ] 健康检查通过
- [ ] 流量切换无错误
- [ ] 旧实例优雅关闭

#### 2.3 烟雾测试
```bash
# 测试正常请求
curl -X POST http://staging-api.example.com/v1/messages \
  -H "x-api-key: test_key_123" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# 测试审计规则触发 (R4-05)
curl -X POST http://staging-api.example.com/v1/messages \
  -H "x-api-key: test_key_123" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [{"role": "user", "content": "Test content with forbidden pattern"}]
  }'

# 测试管理端点
curl http://staging-api.example.com/admin/accounts?filter=percent_budget_gt_80

# 测试 adjust-usage 验证 (R4-07)
curl -X POST http://staging-api.example.com/admin/audit/requests/test_req/adjust-usage \
  -H "Content-Type: application/json" \
  -d '{"input_tokens": -100}'  
# 预期: 400 Bad Request
```
- [ ] 正常请求返回 200 OK
- [ ] 审计规则正确阻止违规请求
- [ ] 管理查询返回正确数据
- [ ] 负 token 被拒绝（400 错误）

### Phase 3: 功能验证 (估计 15 分钟)

#### 3.1 R4-01: 迁移顺序验证
```sql
-- 检查 account_holds 表可以正常写入
INSERT INTO account_holds (account_id, reason, details)
SELECT id, 'test', '{"test":true}'::jsonb
FROM accounts LIMIT 1;

-- 清理测试数据
DELETE FROM account_holds WHERE reason = 'test';
```
- [ ] 插入成功
- [ ] 外键约束生效
- [ ] 删除成功

#### 3.2 R4-02: 从 unknown 恢复验证
```bash
# 1. 创建一个 unknown 状态的预留
# (需要手动模拟或等待实际 unknown 情况)

# 2. 检查 account_holds 表
SELECT * FROM account_holds WHERE reason = 'unknown_pending';

# 3. 通过管理 API 调整
curl -X POST http://staging-api.example.com/admin/requests/{request_id}/adjust-unknown \
  -d '{"action": "settle", "actual_cost": 0.05}'

# 4. 验证 hold 已清除
SELECT * FROM account_holds WHERE reason = 'unknown_pending';
# 预期: 0 行
```
- [ ] unknown 状态正确记录 hold 原因
- [ ] 调整后 hold 原因被清除
- [ ] 账户状态恢复为 active

#### 3.3 R4-03: 预留查询验证
```sql
-- 测试 busy 状态账户可以预留
UPDATE accounts SET state = 'busy' WHERE id = (SELECT id FROM accounts LIMIT 1);

-- 尝试预留（通过 API 或直接调用 Reserve 函数）
-- 预期: 成功，不返回 "account_unavailable" 错误

-- 清理
UPDATE accounts SET state = 'active' WHERE state = 'busy';
```
- [ ] busy 状态账户可以成功预留
- [ ] active 状态账户可以成功预留
- [ ] recovery_hold 状态账户被拒绝

#### 3.4 R4-04: 百分比预算查询验证
```bash
# 创建一个 busy 状态的账户，预算使用 > 80%
# (可通过 SQL 手动设置测试数据)

curl http://staging-api.example.com/admin/accounts?filter=percent_budget_gt_80

# 验证响应包含 busy 状态的账户
```
- [ ] 查询返回 active 状态账户
- [ ] 查询返回 busy 状态账户
- [ ] 查询排除 recovery_hold 等其他状态
- [ ] 百分比计算正确

#### 3.5 R4-05: 审计规则动作验证
```bash
# 测试每种审计规则动作

# block
curl -X POST .../messages \
  -d '{"messages": [{"role": "user", "content": "trigger block rule"}]}'
# 预期: 403 或 blocked 响应，不调用上游 API

# review
curl -X POST .../messages \
  -d '{"messages": [{"role": "user", "content": "trigger review rule"}]}'
# 预期: 202 或 pending_review 响应

# reject
curl -X POST .../messages \
  -d '{"messages": [{"role": "user", "content": "trigger reject rule"}]}'
# 预期: 400 或 rejected 响应

# unsupported
curl -X POST .../messages \
  -d '{"model": "unsupported-model-xxx"}'
# 预期: 400 或 unsupported 响应

# flag (需要调用调节 API)
# 预期: 请求正常处理，但标记为 flagged
```
- [ ] `block` 动作立即阻止请求
- [ ] `review` 动作标记待审查
- [ ] `reject` 动作拒绝请求
- [ ] `unsupported` 动作返回不支持错误
- [ ] `flag` 动作调用调节 API

#### 3.6 R4-06: Usage 提取验证
```bash
# 监控日志，观察 usage 提取逻辑
tail -f /var/log/subai/gateway.log | grep -i usage

# 发送正常请求
curl -X POST .../messages -d '...'

# 验证日志中只有终端事件的 usage 被记录
# 预期日志:
# [INFO] usage extracted from event: response.completed
# 不应该出现:
# [INFO] usage extracted from event: response.content_block_delta
```
- [ ] 只有 `response.completed` 事件提取 usage
- [ ] 只有 `response.failed` 事件提取 usage
- [ ] 中间事件不提取 usage
- [ ] 最终账单金额正确

#### 3.7 R4-07: 负 token 验证
```bash
# 测试所有负值情况

# 负 input_tokens
curl -X POST .../admin/audit/requests/test/adjust-usage \
  -d '{"input_tokens": -100, "output_tokens": 100}'
# 预期: 400 "token counts must be non-negative"

# 负 cached_tokens
curl -X POST .../admin/audit/requests/test/adjust-usage \
  -d '{"input_tokens": 100, "cached_input_tokens": -50, "output_tokens": 100}'
# 预期: 400 "token counts must be non-negative"

# 负 output_tokens
curl -X POST .../admin/audit/requests/test/adjust-usage \
  -d '{"input_tokens": 100, "output_tokens": -100}'
# 预期: 400 "token counts must be non-negative"

# cached > input
curl -X POST .../admin/audit/requests/test/adjust-usage \
  -d '{"input_tokens": 100, "cached_input_tokens": 200, "output_tokens": 100}'
# 预期: 400 "cached_input_tokens cannot exceed input_tokens"

# 正常情况
curl -X POST .../admin/audit/requests/test/adjust-usage \
  -d '{"input_tokens": 100, "cached_input_tokens": 50, "output_tokens": 100}'
# 预期: 200 OK
```
- [ ] 拒绝负 input_tokens
- [ ] 拒绝负 cached_tokens
- [ ] 拒绝负 output_tokens
- [ ] 拒绝 cached > input
- [ ] 接受合法的零值
- [ ] 接受合法的正值

### Phase 4: 监控设置 (估计 10 分钟)

#### 4.1 设置告警
```yaml
# Prometheus 告警规则示例
groups:
  - name: round4_fixes
    interval: 30s
    rules:
      # R4-01: account_holds 写入监控
      - alert: AccountHoldsSpike
        expr: rate(account_holds_inserts_total[5m]) > 10
        for: 5m
        annotations:
          summary: "异常高的账户隔离率"
          
      # R4-06: 缺失 usage 告警
      - alert: UsageMissingSpike
        expr: rate(usage_missing_total[5m]) > 5
        for: 5m
        annotations:
          summary: "缺失 usage 事件增加"
          
      # R4-03: 预留失败监控
      - alert: ReserveFailureRate
        expr: rate(reserve_failures_total[5m]) / rate(reserve_attempts_total[5m]) > 0.05
        for: 5m
        annotations:
          summary: "预留失败率超过 5%"
```
- [ ] 告警规则已配置
- [ ] 告警通知渠道已测试
- [ ] 仪表盘已更新

#### 4.2 日志监控
```bash
# 设置日志聚合过滤器
# Elasticsearch / Splunk / CloudWatch Logs

# 监控关键错误模式
- "account_holds insert failed"
- "usage extraction failed"
- "reserve query failed"
- "negative token rejected"
```
- [ ] 日志过滤器已配置
- [ ] 错误日志路由到告警系统
- [ ] 日志留存策略已确认

---

## 🚀 生产环境部署

### Pre-deployment Checklist
- [ ] Staging 环境运行稳定 ≥ 24 小时
- [ ] 所有 staging 测试通过
- [ ] 无未解决的 staging 问题
- [ ] 生产数据库备份完成
- [ ] 回滚计划已准备
- [ ] 值班工程师已就位

### Deployment Window
**推荐时间**: 工作日低峰时段（如周二/周三 上午 10:00-12:00）  
**避免时间**: 周五、节假日、已知高峰期

### 部署步骤
1. **数据库迁移** (T+0min, 估计 5 分钟)
   - [ ] 创建生产数据库备份
   - [ ] 应用迁移 `014_create_account_holds.sql`
   - [ ] 验证表结构

2. **金丝雀部署** (T+5min, 估计 20 分钟)
   - [ ] 部署 1 个实例到 5% 流量
   - [ ] 监控 10 分钟：错误率、延迟、CPU、内存
   - [ ] 增加到 25% 流量
   - [ ] 监控 10 分钟

3. **全量部署** (T+25min, 估计 15 分钟)
   - [ ] 滚动更新所有实例
   - [ ] 每次更新间隔 2 分钟
   - [ ] 实时监控健康检查

4. **验证** (T+40min, 估计 20 分钟)
   - [ ] 运行生产烟雾测试
   - [ ] 验证关键指标：请求成功率、p99 延迟、错误率
   - [ ] 检查 `account_holds` 表写入
   - [ ] 验证 usage 提取日志

5. **监控** (T+60min - T+24h)
   - [ ] 持续监控告警
   - [ ] 每小时检查关键指标
   - [ ] 记录任何异常行为

---

## 🔙 回滚计划

### 触发条件
- 错误率 > 5% 持续 5 分钟
- p99 延迟增加 > 50%
- 任何 CRITICAL 告警触发
- 数据不一致报告

### 回滚步骤

#### 应用回滚 (5 分钟)
```bash
# 1. 立即切换到旧版本实例
# 编辑 nginx upstream，移除新实例
sudo nginx -s reload

# 2. 重启旧版本实例
./subai-gateway-v1.3.0 &

# 3. 验证流量恢复
curl http://api.example.com/health
```

#### 数据库回滚 (10 分钟) - 仅在必要时
```sql
-- 警告: 这会丢失 account_holds 表中的所有数据

-- 1. 停止所有应用实例（防止写入冲突）

-- 2. 删除依赖
DROP INDEX IF EXISTS idx_account_holds_reason;
DROP TABLE IF EXISTS account_holds;

-- 3. 恢复到之前的迁移版本
UPDATE schema_migrations SET version = 13 WHERE version = 14;

-- 4. 验证
\dt account_holds
-- 预期: relation does not exist
```

#### 回滚验证
- [ ] 应用恢复到旧版本
- [ ] 健康检查通过
- [ ] 请求成功率恢复正常
- [ ] 告警清除

---

## 📊 成功标准

### 部署成功指标
- ✅ 错误率 < 1%
- ✅ p99 延迟变化 < 10%
- ✅ CPU/内存使用率在正常范围
- ✅ 无 CRITICAL 告警
- ✅ 所有功能测试通过

### 业务指标
- ✅ 请求吞吐量保持稳定
- ✅ 账户隔离准确性提升（可审计）
- ✅ Usage 计费准确性提升
- ✅ 无负 token 计费事故

---

## 📝 部署后任务

### 立即任务 (部署后 1 小时内)
- [ ] 在团队频道宣布部署完成
- [ ] 更新部署文档和变更日志
- [ ] 归档 staging 测试结果
- [ ] 记录任何部署问题或观察

### 短期任务 (部署后 24 小时内)
- [ ] 生成部署后报告（指标、问题、经验教训）
- [ ] 更新运维文档
- [ ] 删除临时测试数据
- [ ] 与产品团队同步修复效果

### 长期任务 (部署后 1 周内)
- [ ] 分析 `account_holds` 表数据，识别隔离模式
- [ ] 审查 usage 提取准确性
- [ ] 评估 R4-06 修复对账单准确性的影响
- [ ] 规划下一轮优化

---

## 🆘 紧急联系人

| 角色 | 姓名 | 联系方式 | 时区 |
|------|------|---------|------|
| 值班工程师 | [Name] | [Phone/Slack] | [Timezone] |
| 数据库管理员 | [Name] | [Phone/Slack] | [Timezone] |
| 产品负责人 | [Name] | [Phone/Slack] | [Timezone] |
| SRE 负责人 | [Name] | [Phone/Slack] | [Timezone] |

---

## 📚 参考文档

- [Round 4 修复总结](./ROUND4_FIXES_SUMMARY.md)
- [Round 4 验证报告](./ROUND4_VERIFICATION_REPORT.md)
- [独立审查报告](./INDEPENDENT_REVIEW_ROUND3_2026-09-12.md)
- [迁移文档](../migrations/README.md)
- [运维手册](./OPERATIONS.md)

---

**检查清单版本**: 1.0  
**最后更新**: 2026-09-12  
**维护者**: DevOps Team
