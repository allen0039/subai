# 综合修复计划 (Round 3-5)

## 概述

基于独立审查报告 Round 3-5，共有 11 个需要修复的问题：
- **P1 级别**: 8 个（影响计费正确性、隔离机制、测试可靠性）
- **P2 级别**: 3 个（影响管理功能、性能优化）

## 修复状态总览

| ID | 严重程度 | 问题 | 状态 | 审查轮次 |
|---|---|---|---|---|
| R3-01 | P1 | 多请求隔离原因追踪 | ✅ 已修复 | Round 3 |
| R3-02 | P1 | OAuth 刷新覆盖隔离状态 | ✅ 已修复 | Round 3 |
| R3-03 | P1 | 空 usage 验证不足 | ✅ 已修复 | Round 3 |
| R3-04 | P2 | 管理端 Key 路由创建 | ✅ 已修复 | Round 3 |
| R3-05 | P2 | 队列测试数据竞争 | ✅ 已修复 | Round 3 |
| R5-01 | P1 | 缓存 token 计费语义 | 🔄 待修复 | Round 5 |
| R5-02 | P1 | 管理更新绕过隔离 | 🔄 待修复 | Round 5 |
| R5-03 | P1 | 测试清理遗漏 account_holds | 🔄 待修复 | Round 5 |
| R5-04 | P1 | 数据库测试配置错误 | 🔄 待修复 | Round 5 |
| R5-05 | P2 | 审核事件复核 URL 错误 | 🔄 待修复 | Round 5 |
| R5-06 | P2 | 相同优先级路由未轮转 | 🔄 待修复 | Round 5 |

## Round 3 修复总结 ✅

### R3-01: 隔离原因追踪系统
- **已完成**: 创建 `account_holds` 表，记录结构化原因
- **已完成**: 所有隔离路径记录 hold 原因
- **已完成**: 只有清除所有 hold 才能恢复 active

### R3-02: OAuth 刷新状态保护
- **已完成**: 刷新只更新凭证，添加 `WHERE state='active'` 条件
- **已完成**: 不会覆盖 recovery_hold 状态

### R3-03: Usage 提取验证
- **已完成**: 只从终态事件提取 usage
- **已完成**: 负数、cached > input 校验

### R3-04: 管理路由权限
- **已完成**: adjust-unknown 移至管理路由组

### R3-05: 测试同步
- **已完成**: 队列测试使用 channel 同步

## Round 5 待修复问题

### R5-01 [P1] 缓存 token 计费语义不一致

**问题**: 管理补账接口的 input_tokens 语义与 billing.Usage.InputTokens（uncached）不一致

**影响**: 
- 测试用例：100 总输入，80 缓存 → 期望 $0.000056，实际 $0.000216
- 费用计算错误 3.86 倍

**修复方案**:
1. 明确管理 API 使用"总输入"语义
2. 在调用 billing.Settle 前归一化：`uncached = total_input - cached`
3. 添加端到端 HTTP 测试验证账本金额

**文件**:
- `internal/admin/audit_handlers.go:522,545`
- `tests/integration/round5_fixes_test.go` (新增)

---

### R5-02 [P1] 管理更新绕过隔离检查

**问题**: PATCH `/api/admin/accounts/{id}` 可以强制设置 state=active，即使存在未解除的 hold

**影响**: 
- 账号状态与 account_holds 不一致
- 调度器可能选择仍有隔离原因的账号

**修复方案**:
1. `patchAccount` 添加 hold 检查：存在 hold 时拒绝设置为 active
2. 统一 `AddHoldTx`/`RemoveHold` 在同一事务内协调状态
3. 调度器 `AcquireAccount` 添加 hold 过滤

**文件**:
- `internal/admin/resources.go:298-320`
- `internal/scheduler/scheduler.go:171`
- `internal/accounts/holds.go`

---

### R5-03 [P1] 测试清理遗漏 account_holds

**问题**: `cleanupDB` 没有清理 account_holds 表，连续运行测试失败

**影响**: 第二次运行报错 "relation account_holds already exists"

**修复方案**:
```go
"account_holds",  // 添加到清理列表
"accounts",       // CASCADE 删除约束但不删除表本身
```

**文件**:
- `tests/integration/integration_test.go:155-163`

---

### R5-04 [P1] 数据库测试配置错误

**问题**: 
- `testPool` 硬编码 localhost 连接，忽略 TEST_DATABASE_URL
- 没有运行迁移，使用不存在的 schema

**修复方案**:
1. 删除 `testPool` 函数
2. 所有测试使用 `newTestEnv(t)` 复用真实配置
3. 确保测试调用实际业务入口（Reserve/applicablePolicies）

**文件**:
- `tests/integration/round4_fixes_test.go:337-390`

---

### R5-05 [P2] 审核事件复核 URL 解析错误

**问题**: `/api/admin/audit/events/{uuid}/review` 保留 `/review` 后缀传给 UUID 解析

**影响**: 返回 500 错误 "invalid input syntax for type uuid"

**修复方案**:
```go
if strings.HasSuffix(r.URL.Path, "/review") {
    base := strings.TrimSuffix(id, "/review")
    if !resourceUUID.MatchString(base) { ... }
    s.reviewAuditEvent(w, r, base)
    return
}
```

**文件**:
- `internal/admin/routes.go:190-192` (已部分修复)

---

### R5-06 [P2] 相同优先级路由未轮转

**问题**: 
- 代码以循环下标作为 tier，而非 Route.Priority
- 相同优先级的两个账号不会轮转，总是选第一个

**影响**: 负载不均衡

**修复方案**:
1. 按 Priority 值聚合候选账号
2. 同优先级内使用轮转选择
3. 添加测试验证 6 次调用分布为 3:3

**文件**:
- `internal/scheduler/scheduler.go:179-193,216`
- `tests/integration/round5_fixes_test.go` (新增)

## 修复顺序

### Phase 1: 测试基础设施 (优先)
1. ✅ R5-03: 修复测试清理
2. ✅ R5-04: 修复数据库测试配置

### Phase 2: 隔离一致性
3. ✅ R5-02: 管理更新隔离检查

### Phase 3: 计费正确性
4. ✅ R5-01: 缓存 token 计费语义

### Phase 4: 管理功能
5. ✅ R5-05: 审核事件 URL
6. ✅ R5-06: 路由轮转优化

## 验证清单

### 单元测试
- [ ] `go test ./internal/... -race -count=1`

### 集成测试
- [ ] Round 3 测试：`go test ./tests/integration -run TestR3`
- [ ] Round 4 测试：`go test ./tests/integration -run TestR4`
- [ ] Round 5 测试：`go test ./tests/integration -run TestR5`
- [ ] 完整集成：`go test ./tests/integration -count=2` (连续两次)

### 构建
- [ ] `make lint`
- [ ] `go build ./...`
- [ ] `npm --prefix web run build`

### 端到端（手动）
- [ ] 管理员设置隔离 → 验证调度器不选择
- [ ] OAuth 刷新期间设置隔离 → 验证刷新后保持隔离
- [ ] 补账计费验证账本金额正确
- [ ] 相同优先级账号负载均衡

## 完成标准

1. ✅ 所有 P1 问题已修复并有测试覆盖
2. ✅ 所有 P2 问题已修复
3. ✅ 集成测试连续运行两次通过
4. ✅ 无数据竞争
5. ✅ 构建和 lint 通过
