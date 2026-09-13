# Round 5 部署检查清单

**部署日期**: ___________  
**部署人员**: ___________  
**环境**: [ ] Staging [ ] Production

---

## 部署前 (Pre-Deployment)

### 代码准备
- [ ] 所有代码已合并到目标分支
- [ ] Code review 已批准
- [ ] 所有测试通过
- [ ] 编译检查通过 (`go build ./...`)

### 数据库准备
- [ ] 确认 `account_holds` 表存在
- [ ] 验证索引存在: `account_holds.account_id`
- [ ] 数据库备份已完成
- [ ] 迁移脚本已准备（如需要）

### 环境准备
- [ ] Staging 环境已部署并测试
- [ ] 监控和告警已配置
- [ ] 回滚计划已准备
- [ ] 团队已通知部署窗口

### 测试验证
- [ ] 单元测试通过: `go test ./internal/billing -v`
- [ ] 集成测试通过: `go test ./tests/integration -run TestR5 -v`
- [ ] 性能基准测试完成
- [ ] 手动烟雾测试完成

---

## 部署中 (During Deployment)

### 部署步骤
1. [ ] 停止接收新流量（如需要）
2. [ ] 部署新代码到服务器
3. [ ] 验证服务健康检查通过
4. [ ] 逐步恢复流量（金丝雀/蓝绿）

### 验证步骤
- [ ] 服务启动无错误
- [ ] 日志无异常错误
- [ ] 关键端点响应正常
- [ ] 数据库连接正常

---

## 部署后 (Post-Deployment)

### 立即验证 (前 15 分钟)
- [ ] 错误率正常（< 1%）
- [ ] 响应时间正常（p99 < 500ms）
- [ ] 无 panic 或 crash
- [ ] CPU/内存使用率正常

### R5-01 定价验证
- [ ] 抽查 5 个缓存令牌请求，验证定价正确
- [ ] 查询示例：
  ```sql
  SELECT request_id, input_tokens, cached_input_tokens, cost 
  FROM usage_ledger 
  WHERE cached_input_tokens > 0 
    AND created_at > now() - interval '15 minutes'
  LIMIT 5;
  ```
- [ ] 计算验证: `cost = (uncached * 0.002 + cached * 0.0002 + output * 0.01) / 1000`

### R5-02 账号隔离验证
- [ ] 有 hold 的账号未被选择（检查日志）
- [ ] 尝试激活有 hold 的账号返回 409
- [ ] 查询验证：
  ```sql
  -- 应该返回 0 行
  SELECT COUNT(*) 
  FROM requests r
  JOIN accounts a ON a.id = r.account_id
  JOIN account_holds h ON h.account_id = a.id
  WHERE r.created_at > now() - interval '15 minutes';
  ```

### R5-05 输入验证
- [ ] 尝试恶意输入被拒绝（返回 400）
- [ ] 测试命令：
  ```bash
  curl /api/admin/proxies/not-a-uuid/test
  # 期望: 400 "invalid proxy ID"
  ```

### R5-06 路由轮换
- [ ] 相同优先级的账号请求分布均匀
- [ ] 查询验证：
  ```sql
  SELECT a.label, COUNT(*) as count
  FROM requests r
  JOIN accounts a ON a.id = r.account_id
  WHERE r.created_at > now() - interval '15 minutes'
  GROUP BY a.label
  ORDER BY count DESC;
  ```

---

## 短期监控 (前 24 小时)

### 指标监控
- [ ] `billing.usage_from_total_errors` = 0
- [ ] `scheduler.no_healthy_account_errors` 无异常增长
- [ ] `admin.input_validation_errors` < 10/hour
- [ ] 整体错误率 < 0.5%
- [ ] 平均响应时间未增加 > 10%

### 业务监控
- [ ] 无客户投诉关于计费
- [ ] 无客户投诉关于账号不可用
- [ ] 财务指标正常
- [ ] API 使用量正常

### 日志审查
- [ ] 无 "negative token count" 错误
- [ ] 无 "cached exceeds total" 错误
- [ ] 有 hold 的账号被正确过滤（日志确认）
- [ ] 无 SQL 错误或注入尝试

---

## R5-01 财务回溯（部署后 1 周内）

### 影响评估
- [ ] 识别受影响的时间范围: _______ 到 _______
- [ ] 运行审计查询：
  ```sql
  SELECT 
      COUNT(*) as affected_requests,
      SUM(cost) as total_charged,
      SUM((input_tokens * 0.002 + cached_input_tokens * 0.0002 + output_tokens * 0.01) / 1000) as should_be,
      SUM(cost) - SUM((input_tokens * 0.002 + cached_input_tokens * 0.0002 + output_tokens * 0.01) / 1000) as overcharge
  FROM usage_ledger
  WHERE cached_input_tokens > 0
    AND entry_type = 'charge'
    AND created_at BETWEEN '___' AND '___';
  ```
- [ ] 受影响请求数: _______
- [ ] 过度收费总额: $ _______

### 退款处理
- [ ] 退款脚本已准备
- [ ] 退款脚本已在 staging 测试
- [ ] 客户沟通邮件已准备
- [ ] 退款已执行
- [ ] 客户已通知
- [ ] 财务记录已更新

---

## 问题追踪

### 部署中遇到的问题
| 时间 | 问题描述 | 严重程度 | 解决方案 | 状态 |
|------|---------|---------|---------|------|
|      |         |         |         |      |
|      |         |         |         |      |

### 后续行动项
| 行动项 | 负责人 | 截止日期 | 状态 |
|--------|--------|---------|------|
|        |        |         |      |
|        |        |         |      |

---

## 回滚决策（如需要）

### 回滚触发条件（任一）
- [ ] 错误率 > 10%
- [ ] 计费异常报告 > 5 起
- [ ] p99 响应时间 > 2x baseline
- [ ] 客户投诉激增 > 10 起/小时
- [ ] 其他严重问题: _______

### 回滚执行
- [ ] 回滚决策已批准（批准人: _______）
- [ ] 回滚命令已执行
- [ ] 服务恢复验证
- [ ] 团队已通知回滚
- [ ] 事后分析会议已安排

---

## 最终签署

### Staging 部署
- **部署人员**: _________ **日期**: _________ **时间**: _________
- **验证人员**: _________ **日期**: _________ **时间**: _________
- **状态**: [ ] 通过 [ ] 失败 [ ] 部分通过

### Production 部署
- **部署人员**: _________ **日期**: _________ **时间**: _________
- **验证人员**: _________ **日期**: _________ **时间**: _________
- **状态**: [ ] 通过 [ ] 失败 [ ] 需要回滚

### 最终批准
- **技术负责人**: _________ **签名**: _________ **日期**: _________
- **业务负责人**: _________ **签名**: _________ **日期**: _________

---

## 备注

_使用此区域记录任何额外的观察、问题或建议_

---

**检查清单版本**: 1.0  
**创建日期**: 2026-09-13  
**基于**: Round 5 修复
