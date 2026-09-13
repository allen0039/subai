# OAuth 并发修复 - 部署检查清单

**部署日期**: _____________  
**执行人**: _____________  
**审核人**: _____________

---

## 部署前检查 (Pre-Deployment)

### 代码审查
- [ ] 代码变更已通过 peer review
- [ ] 所有测试通过 (`go test ./...`)
- [ ] 验证脚本通过 (`./scripts/verify_batch_b_fixes.sh`)
- [ ] 无 linting 错误 (`golangci-lint run`)

### 环境准备
- [ ] 生产数据库备份已创建
- [ ] Staging 环境已成功测试完整流程
- [ ] 监控告警已配置（OAuth 错误率）
- [ ] 回滚计划已准备

### 文档
- [ ] 变更文档已创建并审核
- [ ] 团队已收到部署通知
- [ ] On-call 工程师已确认待命

---

## 步骤 1: 数据库迁移 (5-10分钟)

### 1.1 连接到生产数据库

```bash
psql $DATABASE_URL
```

**检查点**: 
- [ ] 已连接到正确的数据库
- [ ] 确认当前 schema_migrations 版本
  ```sql
  SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT 5;
  ```
  预期: 最新版本应该是 6 (migration_checksums)

### 1.2 检查表状态

```sql
-- 检查当前 pending sessions 数量
SELECT COUNT(*) FROM oauth_sessions WHERE status='pending';

-- 检查是否有同账号多个 pending 的情况
SELECT account_id, COUNT(*) as pending_count
FROM oauth_sessions
WHERE status='pending'
GROUP BY account_id
HAVING COUNT(*) > 1;
```

**记录结果**:
- Pending sessions 数量: _____________
- 多 pending 账号数: _____________

**检查点**:
- [ ] 如果有多 pending 的情况，记录下来（修复后应消失）

### 1.3 应用迁移

```bash
psql $DATABASE_URL -f migrations/0007_oauth_concurrency.sql
```

**预期输出**: 
```
CREATE INDEX
```

**检查点**:
- [ ] 命令成功执行（无错误）
- [ ] 执行时间 < 1 分钟（使用 CONCURRENTLY）

### 1.4 验证索引创建

```sql
-- 检查索引存在
\d oauth_sessions

-- 应该看到：
-- "idx_oauth_sessions_one_pending_per_account" UNIQUE, btree (account_id) WHERE status='pending'

-- 测试约束生效
BEGIN;
INSERT INTO oauth_sessions(account_id, state, verifier, redirect_uri, expires_at, status)
VALUES ('test-account', 'state1', 'verifier1', 'https://example.com', NOW() + INTERVAL '10 minutes', 'pending');

-- 这应该失败
INSERT INTO oauth_sessions(account_id, state, verifier, redirect_uri, expires_at, status)
VALUES ('test-account', 'state2', 'verifier2', 'https://example.com', NOW() + INTERVAL '10 minutes', 'pending');
ROLLBACK;
```

**检查点**:
- [ ] 索引在 `\d oauth_sessions` 输出中可见
- [ ] 第二个 INSERT 失败并显示 "duplicate key" 错误
- [ ] ROLLBACK 成功（测试数据未写入）

**时间戳**: _____________

---

## 步骤 2: 代码部署 (15-30分钟)

### 2.1 准备新版本

```bash
cd /opt/subai
git fetch origin
git checkout <commit-hash-or-tag>
go build -o subai-new ./cmd/server
```

**检查点**:
- [ ] 代码已拉取到正确的 commit
- [ ] 编译成功，生成 `subai-new` 二进制文件
- [ ] 二进制文件大小合理（与之前版本接近）

### 2.2 滚动重启 - 实例 1

```bash
# 健康检查
curl http://instance1:8080/health

# 停止旧进程
systemctl stop subai@instance1

# 替换二进制
mv subai subai-old
mv subai-new subai

# 启动新进程
systemctl start subai@instance1

# 等待启动
sleep 5
```

**检查点**:
- [ ] 健康检查返回 200 OK
  ```bash
  curl http://instance1:8080/health
  ```
- [ ] 日志无启动错误
  ```bash
  tail -n 50 /var/log/subai/instance1.log
  ```
- [ ] 进程正在运行
  ```bash
  systemctl status subai@instance1
  ```

**如果失败**: 
```bash
systemctl stop subai@instance1
mv subai-old subai
systemctl start subai@instance1
# 停止部署，调查问题
```

**时间戳**: _____________

### 2.3 监控实例 1 (5 分钟)

```bash
# 实时监控日志
tail -f /var/log/subai/instance1.log | grep -E 'ERROR|WARN|oauth'
```

**检查点** (每分钟检查):
- [ ] 分钟 1: 无异常错误
- [ ] 分钟 2: OAuth 请求正常处理
- [ ] 分钟 3: 无数据库连接问题
- [ ] 分钟 4: 无 panic 或 fatal 错误
- [ ] 分钟 5: 指标正常（延迟、错误率）

**记录指标**:
- 请求成功率: _____________ %
- 平均延迟: _____________ ms
- OAuth 错误数: _____________

### 2.4 滚动重启 - 实例 2

重复步骤 2.2 和 2.3 的检查

**检查点**:
- [ ] 实例 2 健康检查通过
- [ ] 实例 2 日志无错误
- [ ] 实例 2 监控指标正常

**时间戳**: _____________

### 2.5 滚动重启 - 实例 3

重复步骤 2.2 和 2.3 的检查

**检查点**:
- [ ] 实例 3 健康检查通过
- [ ] 实例 3 日志无错误
- [ ] 实例 3 监控指标正常

**时间戳**: _____________

### 2.6 滚动重启 - 剩余实例

对每个剩余实例重复相同流程，间隔 1-2 分钟

**检查点**:
- [ ] 所有实例健康检查通过
- [ ] 所有实例运行新版本代码

**完成时间**: _____________

---

## 步骤 3: 部署验证 (10分钟)

### 3.1 功能测试

**测试 1: 并发 Session 创建**

```bash
# 创建测试账号的两个并发 reauth 请求
ACCOUNT_ID="test-$(date +%s)"

curl -X POST "https://api.example.com/accounts/$ACCOUNT_ID/reauthorize" \
  -H "Authorization: Bearer $ADMIN_TOKEN" &
  
curl -X POST "https://api.example.com/accounts/$ACCOUNT_ID/reauthorize" \
  -H "Authorization: Bearer $ADMIN_TOKEN" &

wait
```

**预期结果**:
- 第一个请求: 返回 200 OK，包含 authorize URL
- 第二个请求: 返回 409 Conflict，错误信息 "already has a pending reauthorization"

**检查点**:
- [ ] 第一个请求成功
- [ ] 第二个请求返回正确的错误
- [ ] 数据库中只有 1 个 pending session
  ```sql
  SELECT COUNT(*) FROM oauth_sessions 
  WHERE account_id='$ACCOUNT_ID' AND status='pending';
  ```

**测试 2: OAuth 完成流程**

```bash
# 使用真实 OAuth provider 测试完整流程
# （这需要实际点击 authorize URL 并完成 OAuth 流程）
```

**检查点**:
- [ ] OAuth callback 成功
- [ ] Token 正确存储
- [ ] Session status 更新为 'completed'
- [ ] 日志无 "duplicate key" 错误

### 3.2 数据库验证

```sql
-- 检查迁移版本
SELECT version, name, checksum FROM schema_migrations 
WHERE version = 7;

-- 预期: version=7, name='oauth_concurrency', checksum=非空

-- 检查不再有多 pending 的情况
SELECT account_id, COUNT(*) as pending_count
FROM oauth_sessions
WHERE status='pending'
GROUP BY account_id
HAVING COUNT(*) > 1;

-- 预期: 0 rows
```

**检查点**:
- [ ] 版本 7 迁移已记录
- [ ] 无多 pending 账号

### 3.3 日志分析

```bash
# 检查部署后的错误日志
journalctl -u subai@* --since "10 minutes ago" | grep -i error

# 检查 OAuth 相关日志
journalctl -u subai@* --since "10 minutes ago" | grep -i oauth

# 检查数据库错误
journalctl -u subai@* --since "10 minutes ago" | grep -E "duplicate key|deadlock"
```

**检查点**:
- [ ] 无 "duplicate key violation" 错误
- [ ] 无 "unique constraint violation" 错误
- [ ] 无新的 panic 或 fatal 错误
- [ ] OAuth 请求正常处理

### 3.4 监控指标

访问监控仪表板，检查：

**检查点**:
- [ ] HTTP 错误率 < 0.1%
- [ ] 数据库连接池正常
- [ ] OAuth 完成成功率 > 99%
- [ ] 平均响应时间 < 200ms
- [ ] 无告警触发

**记录指标**:
- 错误率: _____________ %
- 成功率: _____________ %
- P50 延迟: _____________ ms
- P99 延迟: _____________ ms

---

## 步骤 4: 持续监控 (24 小时)

### 4.1 第一小时监控

**每 15 分钟检查**:
- [ ] 15 分钟: OAuth 错误计数
- [ ] 30 分钟: Pending session 分布
- [ ] 45 分钟: Token refresh 成功率
- [ ] 60 分钟: 整体系统健康

```bash
# 错误计数脚本
watch -n 900 'psql $DATABASE_URL -c "
SELECT 
  COUNT(*) FILTER (WHERE status='\''completed'\'') as completed,
  COUNT(*) FILTER (WHERE status='\''failed'\'') as failed,
  COUNT(*) FILTER (WHERE status='\''pending'\'') as pending
FROM oauth_sessions 
WHERE created_at > NOW() - INTERVAL '\''1 hour'\'';"'
```

### 4.2 首日监控要点

**检查频率**: 每 2-4 小时

**关键指标**:
1. OAuth 错误日志
   ```bash
   grep -c "duplicate key.*oauth_sessions" /var/log/subai/*.log
   # 目标: 0
   ```

2. Pending session 冲突
   ```bash
   grep -c "ErrPendingSessionExists" /var/log/subai/*.log
   # 目标: < 5/day（合法情况）
   ```

3. Token refresh 浪费
   ```bash
   grep -c "credential changed concurrently" /var/log/subai/*.log
   # 目标: < 1/day
   ```

**检查点** (每次检查时填写):

| 时间 | Duplicate Key | Pending 冲突 | Refresh 浪费 | 备注 |
|------|---------------|--------------|--------------|------|
| __:__ | _____ | _____ | _____ | __________ |
| __:__ | _____ | _____ | _____ | __________ |
| __:__ | _____ | _____ | _____ | __________ |
| __:__ | _____ | _____ | _____ | __________ |

---

## 回滚程序

### 何时回滚

立即回滚如果：
- [ ] OAuth 成功率下降 > 5%
- [ ] "duplicate key" 错误持续出现
- [ ] 数据库连接池耗尽
- [ ] 任何 P0/P1 级别的生产问题

### 回滚步骤

**1. 回滚代码** (5 分钟)

```bash
# 在所有实例执行
systemctl stop subai@*
mv subai subai-failed
mv subai-old subai
systemctl start subai@*

# 验证回滚
curl http://localhost:8080/health
```

**检查点**:
- [ ] 所有实例回滚到旧版本
- [ ] 健康检查通过
- [ ] 日志显示旧版本启动成功

**2. （可选）回滚迁移**

```sql
-- 只有在迁移本身有问题时才执行
DROP INDEX CONCURRENTLY IF EXISTS idx_oauth_sessions_one_pending_per_account;

-- 删除迁移记录
DELETE FROM schema_migrations WHERE version = 7;
```

**注意**: 通常不需要回滚迁移，因为新索引向后兼容旧代码。

**3. 通知团队**

- [ ] 在 #engineering 发布回滚通知
- [ ] 创建 incident report
- [ ] 安排 postmortem 会议

---

## 部署后任务

### 立即任务
- [ ] 在 #engineering 发布成功通知
- [ ] 更新部署文档
- [ ] 归档这份检查清单

### 24 小时后
- [ ] 审查 24 小时监控数据
- [ ] 确认成功指标达成
- [ ] 写部署总结报告

### 1 周后
- [ ] 删除旧版本二进制 (`subai-old`)
- [ ] 确认无遗留问题
- [ ] 关闭相关 issue/ticket

---

## 签名

**部署执行人**: _____________  签名: _____________ 日期: _____________

**代码审核人**: _____________  签名: _____________ 日期: _____________

**运维负责人**: _____________  签名: _____________ 日期: _____________

---

## 附录：有用的命令

### 查看实时日志
```bash
journalctl -u subai@* -f | grep --color=always -E 'ERROR|WARN|$'
```

### 检查数据库连接
```bash
psql $DATABASE_URL -c "SELECT count(*) FROM pg_stat_activity WHERE datname='subai';"
```

### 检查实例版本
```bash
curl -s http://localhost:8080/version | jq .
```

### 紧急停止所有实例
```bash
systemctl stop subai@*
```

### 数据库查询模板
```sql
-- 最近 1 小时的 OAuth 活动
SELECT 
  status, 
  COUNT(*) as count,
  MIN(created_at) as first,
  MAX(created_at) as last
FROM oauth_sessions
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY status;

-- 查找可疑的多 pending
SELECT 
  account_id,
  COUNT(*) as pending_count,
  STRING_AGG(state, ', ') as states
FROM oauth_sessions
WHERE status='pending'
GROUP BY account_id
HAVING COUNT(*) > 1;
```

---

**文档版本**: 1.0  
**最后更新**: 2026-09-13  
**维护者**: Engineering Team
