# Round 5 修复实施计划

**创建时间**: 2026-09-13  
**目标**: 修复 INDEPENDENT_REVIEW_ROUND5_2026-09-12.md 中的 6 个问题  
**当前状态**: 🔴 未开始实施

---

## 问题优先级矩阵

| ID | 严重程度 | 影响范围 | 估算工作量 | 优先级 |
|----|---------|---------|-----------|--------|
| R5-01 | P1 | 计费准确性 | 2h | 🔴 高 |
| R5-02 | P1 | 安全/隔离 | 3h | 🔴 高 |
| R5-03 | P1 | 测试基础设施 | 1h | 🟡 中（先决条件）|
| R5-04 | P1 | 测试质量 | 2h | 🟡 中 |
| R5-05 | P2 | 管理 API | 0.5h | 🟢 低 |
| R5-06 | P2 | 调度公平性 | 2h | 🟢 低 |

**建议执行顺序**: R5-03 → R5-01 → R5-02 → R5-05 → R5-06 → R5-04

---

## R5-03: 测试清理遗漏 account_holds [先决条件]

### 问题描述
`cleanupTestData()` 遗漏 `account_holds` 表，导致连续运行测试时迁移失败（表已存在）。

### 根因
- `account_holds` 未在清理列表中
- `DROP accounts CASCADE` 只删除外键约束，不删除引用表本身

### 修复方案
```go
// tests/integration/integration_test.go:155-163
func cleanupTestData(ctx context.Context, pool *pgxpool.Pool) {
    tables := []string{
        "admin_events",
        "admin_sessions",
        "usage_ledger",
        "reservations",
        "budget_periods",
        "budget_policies",
        "account_holds",  // ← 新增
        "requests",
        "audit_events",
        "key_routes",
        "api_keys",
        "members",
        "group_accounts",
        "accounts",
        "groups",
        "price_versions",
        "audit_rules",
        "egress_policies",
        "proxy_profiles",
    }
    for _, t := range tables {
        pool.Exec(ctx, "DELETE FROM "+t)
    }
    pool.Exec(ctx, "DELETE FROM schema_migrations")
}
```

### 验证
```bash
# 连续运行 3 次，不应有 "already exists" 错误
for i in {1..3}; do
  echo "=== Run $i ==="
  go test -v ./tests/integration -run TestAccountStateTransitions -count=1
done
```

---

## R5-01: 缓存 Token 计费语义不一致 [P1]

### 问题描述
管理员通过 `/adjust-unknown` 补账时，`input_tokens` 字段语义不明确：
- **接口校验**: 假设是"总输入" (`cached <= input`)
- **计费逻辑**: 假设是"非缓存输入" (`Usage.InputTokens`)

导致实际计费错误：
```
输入: input_tokens=100, cached=80, output=0
预期: (20×input_rate + 80×cached_rate) = $0.000056
实际: (100×input_rate + 80×cached_rate) = $0.000216
```

### 根因分析
```go
// internal/admin/audit_handlers.go:522
if req.CachedInputTokens > req.InputTokens {
    return // 假设 input_tokens 是总输入
}

// audit_handlers.go:545
usage := billing.Usage{
    InputTokens:  req.InputTokens,       // ← 直接传递，未减去缓存
    CachedTokens: req.CachedInputTokens,
    OutputTokens: req.OutputTokens,
}
```

```go
// internal/billing/billing.go:31
type Usage struct {
    InputTokens  int64 // 注释说明: "uncached input tokens"
    CachedTokens int64
    OutputTokens int64
}
```

### 修复方案

#### 方案 A: API 语义 = 总输入（推荐）
管理员填写的是**总输入 token 数**，后端负责转换。

```go
// internal/billing/billing.go 新增转换函数
func UsageFromTotal(inputTotal, cached, output int64) (Usage, error) {
    if inputTotal < 0 || cached < 0 || output < 0 {
        return Usage{}, errors.New("tokens must be non-negative")
    }
    if cached > inputTotal {
        return Usage{}, errors.New("cached tokens cannot exceed input total")
    }
    return Usage{
        InputTokens:  inputTotal - cached,  // 非缓存部分
        CachedTokens: cached,
        OutputTokens: output,
    }, nil
}
```

```go
// internal/admin/audit_handlers.go:545
usage, err := billing.UsageFromTotal(req.InputTokens, req.CachedInputTokens, req.OutputTokens)
if err != nil {
    s.writeErr(w, 400, err.Error())
    return
}
```

```go
// web/src/pages/AuditPage.tsx (前端提示)
<FormField label="Input Tokens (total including cached)" ... />
<FormField label="Cached Input Tokens" ... />
```

#### 方案 B: API 语义 = 非缓存（替代）
如果选择此方案，需要修改校验逻辑，但会让管理员操作更复杂。

### 测试用例
```go
// tests/integration/round5_fixes_test.go
func TestR5_01_CachedTokenBilling(t *testing.T) {
    e := newTestEnv(t)
    
    // 1. 创建 unknown 请求
    ctx := context.Background()
    _, err := e.db.Pool.Exec(ctx, `
        INSERT INTO requests(id, api_key_id, account_id, model, state, price_version_id)
        SELECT 'req_r5_cached', k.id, a.id, 'gpt-5-codex', 'unknown', p.id
        FROM api_keys k, accounts a, price_versions p
        LIMIT 1
    `)
    require.NoError(t, err)
    
    // 2. 管理员调整: 总输入100, 缓存80, 输出0
    result := e.resolveUnknownViaAPI(t, "req_r5_cached", 100, 80, 0)
    
    // 3. 验证费用 (假设: input=$0.002/K, cached=$0.0002/K)
    // 正确: (20*0.002 + 80*0.0002) / 1000 = 0.000056
    assert.Equal(t, "0.000056", result.Cost)
    
    // 4. 验证账本记录
    var input, cached, output int64
    var cost string
    err = e.db.Pool.QueryRow(ctx, `
        SELECT input_tokens, cached_input_tokens, output_tokens, cost::text
        FROM usage_ledger
        WHERE request_id = 'req_r5_cached'
    `).Scan(&input, &cached, &output, &cost)
    require.NoError(t, err)
    
    assert.Equal(t, int64(20), input)   // 非缓存部分
    assert.Equal(t, int64(80), cached)
    assert.Equal(t, int64(0), output)
    assert.Equal(t, "0.000056", cost)
}
```

---

## R5-02: Hold 可被管理员绕过 [P1]

### 问题描述
1. 管理员可以直接 `PATCH /accounts/{id}` 设置 `state=active`，无视未解除的 `account_holds`
2. 调度器选择账号时只检查 `state`，不检查 `holds` 表
3. 导致有 hold 的账号仍能被分配请求

### 根因分析
```go
// internal/admin/resources.go:298-320
func (s *Server) patchAccount(w http.ResponseWriter, r *http.Request, id string) {
    // ... 解析请求 ...
    
    // ❌ 没有检查 account_holds
    tag, err := s.DB.Pool.Exec(ctx, `
        UPDATE accounts SET state=$2, version=version+1, updated_at=now()
        WHERE id=$1 AND version=$3
    `, id, *req.State, req.Version)
    
    // ...
}
```

```go
// internal/scheduler/scheduler.go:171
func (s *Scheduler) loadAccount(ctx context.Context, accountID string) (*Account, error) {
    var a Account
    err := s.db.Pool.QueryRow(ctx, `
        SELECT id, label, credentials_ciphertext, priority, state,
               COALESCE(concurrency_limit, 0)
        FROM accounts
        WHERE id = $1 AND state = 'active'  // ❌ 未检查 holds
    `, accountID).Scan(...)
    return &a, err
}
```

### 修复方案

#### 1. 管理 API 阻止激活有 hold 的账号
```go
// internal/admin/resources.go:298-320
func (s *Server) patchAccount(w http.ResponseWriter, r *http.Request, id string) {
    // ... 解析请求 ...
    
    // 新增: 阻止激活有 hold 的账号
    if req.State != nil && *req.State == "active" {
        var hasHold bool
        err := s.DB.Pool.QueryRow(ctx, `
            SELECT EXISTS(SELECT 1 FROM account_holds WHERE account_id=$1)
        `, id).Scan(&hasHold)
        if err != nil {
            s.writeErr(w, 500, err.Error())
            return
        }
        if hasHold {
            s.writeErr(w, 409, "cannot set state to active: account has unresolved hold reasons")
            return
        }
    }
    
    tag, err := s.DB.Pool.Exec(ctx, `
        UPDATE accounts SET state=$2, version=version+1, updated_at=now()
        WHERE id=$1 AND version=$3
    `, id, *req.State, req.Version)
    // ...
}
```

#### 2. 调度器过滤有 hold 的账号
```go
// internal/scheduler/scheduler.go:171
func (s *Scheduler) loadAccount(ctx context.Context, accountID string) (*Account, error) {
    var a Account
    err := s.db.Pool.QueryRow(ctx, `
        SELECT id, label, credentials_ciphertext, priority, state,
               COALESCE(concurrency_limit, 0)
        FROM accounts
        WHERE id = $1
          AND state = 'active'
          AND NOT EXISTS (
              SELECT 1 FROM account_holds h
              WHERE h.account_id = accounts.id
          )
    `, accountID).Scan(...)
    return &a, err
}
```

```go
// internal/scheduler/scheduler.go:297 (groupAccounts 也需要)
func (s *Scheduler) groupAccounts(ctx context.Context, groupID string) ([]Account, error) {
    rows, err := s.db.Pool.Query(ctx, `
        SELECT a.id, a.label, a.credentials_ciphertext, a.priority, a.state,
               COALESCE(a.concurrency_limit, 0)
        FROM accounts a
        JOIN group_accounts ga ON ga.account_id = a.id
        WHERE ga.group_id = $1
          AND a.state = 'active'
          AND NOT EXISTS (
              SELECT 1 FROM account_holds h
              WHERE h.account_id = a.id
          )
    `, groupID)
    // ...
}
```

### 测试用例
```go
// tests/integration/round5_fixes_test.go
func TestR5_02_HoldBlocksActivation(t *testing.T) {
    e := newTestEnv(t)
    ctx := context.Background()
    
    // 1. 获取测试账号
    var accountID string
    err := e.db.Pool.QueryRow(ctx, `SELECT id FROM accounts LIMIT 1`).Scan(&accountID)
    require.NoError(t, err)
    
    // 2. 添加 over_reserve hold
    _, err = e.db.Pool.Exec(ctx, `
        INSERT INTO account_holds(account_id, reason)
        VALUES($1, 'over_reserve')
    `, accountID)
    require.NoError(t, err)
    
    // 3. 尝试通过管理 API 激活 (应该被拒绝)
    req := httptest.NewRequest(http.MethodPatch,
        "/api/admin/accounts/"+accountID,
        strings.NewReader(`{"state":"active","version":1}`))
    req.Header.Set("Authorization", "Bearer "+e.adminToken)
    
    rec := httptest.NewRecorder()
    e.admin.Routes().ServeHTTP(rec, req)
    
    assert.Equal(t, 409, rec.Code)
    assert.Contains(t, rec.Body.String(), "unresolved hold")
}

func TestR5_02_HoldBlocksScheduling(t *testing.T) {
    e := newTestEnv(t)
    ctx := context.Background()
    
    // 1. 获取有路由的账号和 key
    var accountID, keyID string
    err := e.db.Pool.QueryRow(ctx, `
        SELECT a.id, k.id
        FROM accounts a
        JOIN key_routes kr ON kr.target_id = a.id
        JOIN api_keys k ON k.id = kr.api_key_id
        WHERE a.state = 'active'
        LIMIT 1
    `).Scan(&accountID, &keyID)
    require.NoError(t, err)
    
    // 2. 添加 hold
    _, err = e.db.Pool.Exec(ctx, `
        INSERT INTO account_holds(account_id, reason)
        VALUES($1, 'over_reserve')
    `, accountID)
    require.NoError(t, err)
    
    // 3. 调度器不应选择这个账号
    account, release, err := e.gw.Sched.AcquireAccount(ctx, keyID, 1, accountID)
    if err == nil {
        release()
        t.Fatalf("scheduler selected held account: %+v", account)
    }
    
    // 预期错误: ErrNoHealthyAccount 或类似
    assert.Error(t, err)
}
```

---

## R5-05: 审核事件复核 URL 解析错误 [P2]

### 问题描述
`POST /api/admin/audit/events/{uuid}/review` 返回 500，因为 `pathID()` 返回 `{uuid}/review`，未剥离动作后缀。

### 修复方案
```go
// internal/admin/routes.go:190-192
mux.Handle("/api/admin/audit/events/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
    id := pathID(r, "/api/admin/audit/events/")
    if strings.HasSuffix(r.URL.Path, "/review") {
        base := strings.TrimSuffix(id, "/review")
        if !resourceUUID.MatchString(base) {
            s.writeErr(w, 400, "invalid event ID")
            return
        }
        s.reviewAuditEvent(w, r, base)  // ← 传递干净的 UUID
        return
    }
    if !resourceUUID.MatchString(id) {
        s.writeErr(w, 400, "invalid event ID")
        return
    }
    s.reviewAuditEvent(w, r, id)
}))
```

### 测试
```go
// tests/integration/round5_admin_validation_test.go
func TestR5_05_AuditEventReviewURL(t *testing.T) {
    e := newTestEnv(t)
    
    // 1. 创建测试审核事件
    var eventID string
    err := e.db.Pool.QueryRow(context.Background(), `
        INSERT INTO audit_events(request_id, decision, provider, confidence)
        SELECT 'req_test', 'review', 'local', 0.95
        RETURNING id
    `).Scan(&eventID)
    require.NoError(t, err)
    
    // 2. 调用复核 API
    req := httptest.NewRequest(http.MethodPost,
        "/api/admin/audit/events/"+eventID+"/review",
        strings.NewReader(`{"outcome":"false_positive"}`))
    req.Header.Set("Authorization", "Bearer "+e.adminToken)
    
    rec := httptest.NewRecorder()
    e.admin.Routes().ServeHTTP(rec, req)
    
    // 不应该返回 500 (UUID 解析错误)
    assert.NotEqual(t, 500, rec.Code)
    assert.NotContains(t, rec.Body.String(), "invalid input syntax for type uuid")
}
```

---

## R5-06: 相同优先级路由不轮换 [P2]

### 问题描述
调度器使用循环索引作为 `tier`，而不是路由的 `Priority` 值。导致相同优先级的路由被视为不同层级，无法轮换。

### 根因
```go
// internal/scheduler/scheduler.go:179-193
for tier, r := range routes {  // ← tier 是索引 0,1,2...
    // ...
    if len(candidates) > 0 {
        chosen := s.rotate(keyID, candidates)  // ← 同层轮换
        return chosen, func() { s.dec(chosen.ID) }, nil
    }
}
```

### 修复方案
```go
// internal/scheduler/scheduler.go:179-250
func (s *Scheduler) AcquireAccount(ctx context.Context, keyID string, weight int, hintAccountID string) (*Account, func(), error) {
    // ... 获取路由 ...
    
    // 按优先级分组
    type tier struct {
        priority int
        routes   []Route
    }
    tiers := []tier{}
    currentPriority := -1
    currentTier := -1
    
    for _, r := range routes {
        if r.Priority != currentPriority {
            tiers = append(tiers, tier{priority: r.Priority, routes: []Route{r}})
            currentPriority = r.Priority
            currentTier++
        } else {
            tiers[currentTier].routes = append(tiers[currentTier].routes, r)
        }
    }
    
    // 遍历每个优先级层
    for _, t := range tiers {
        var candidates []Account
        
        for _, r := range t.routes {
            var accts []Account
            switch r.TargetType {
            case "account":
                a, err := s.loadAccount(ctx, r.TargetID)
                if err == nil {
                    accts = append(accts, *a)
                }
            case "group":
                accts, _ = s.groupAccounts(ctx, r.TargetID)
            }
            candidates = append(candidates, accts...)
        }
        
        if len(candidates) > 0 {
            chosen := s.rotate(keyID, candidates)
            return chosen, func() { s.dec(chosen.ID) }, nil
        }
    }
    
    return nil, nil, ErrNoHealthyAccount
}
```

```go
// internal/scheduler/scheduler.go:270 (rotate 也需要用 keyID)
func (s *Scheduler) rotate(keyID string, candidates []Account) *Account {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    offset := s.rot[keyID]  // ← 已修复：使用 keyID
    s.rot[keyID]++
    
    // ... 选择逻辑 ...
}
```

### 测试
```go
// tests/integration/round5_fixes_test.go
func TestR5_06_EqualPriorityRotation(t *testing.T) {
    e := newTestEnv(t)
    ctx := context.Background()
    
    // 1. 获取一个 key
    var keyID string
    err := e.db.Pool.QueryRow(ctx, `SELECT id FROM api_keys LIMIT 1`).Scan(&keyID)
    require.NoError(t, err)
    
    // 2. 创建第二个账号
    var account2ID string
    err = e.db.Pool.QueryRow(ctx, `
        INSERT INTO accounts(label, credentials_ciphertext, priority)
        SELECT 'acct_rotation_test', credentials_ciphertext, priority
        FROM accounts LIMIT 1
        RETURNING id
    `).Scan(&account2ID)
    require.NoError(t, err)
    
    // 3. 添加相同优先级的路由
    _, err = e.db.Pool.Exec(ctx, `
        INSERT INTO key_routes(api_key_id, target_type, target_id, priority)
        VALUES($1, 'account', $2, 100)
    `, keyID, account2ID)
    require.NoError(t, err)
    
    // 4. 连续 6 次获取，统计分布
    distribution := map[string]int{}
    for i := 0; i < 6; i++ {
        account, release, err := e.gw.Sched.AcquireAccount(ctx, keyID, 1, "")
        require.NoError(t, err)
        distribution[account.ID]++
        release()
    }
    
    // 5. 验证两个账号都被选中，且接近均匀分布
    assert.Equal(t, 2, len(distribution), "should use both accounts")
    for accountID, count := range distribution {
        assert.Equal(t, 3, count, "account %s should be selected 3 times", accountID)
    }
}
```

---

## R5-04: 测试数据库配置问题 [P1]

### 问题描述
`round4_fixes_test.go` 硬编码数据库 URL，忽略 `TEST_DATABASE_URL`，且使用不存在的 schema。

### 修复方案
删除 `round4_fixes_test.go` 中的硬编码测试，迁移到使用 `newTestEnv()` 的标准测试。

```go
// tests/integration/round4_fixes_test.go:337-390 删除 testPool 相关代码
// 所有测试改用:
func TestR4_XX_Something(t *testing.T) {
    e := newTestEnv(t)  // ← 使用标准环境
    ctx := context.Background()
    
    // ... 使用 e.db.Pool ...
}
```

修改 `TestR4_04_PercentBudgetQueryNoBusy` 调用实际的 `Reserve()`:
```go
func TestR4_04_PercentBudgetQueryNoBusy(t *testing.T) {
    e := newTestEnv(t)
    ctx := context.Background()
    
    // 1. 创建固定预算策略
    var baseID string
    err := e.db.Pool.QueryRow(ctx, `
        INSERT INTO budget_policies(name, scope, amount)
        VALUES('base_policy', 'account', 1000)
        RETURNING id
    `).Scan(&baseID)
    require.NoError(t, err)
    
    // 2. 创建百分比策略
    _, err = e.db.Pool.Exec(ctx, `
        INSERT INTO budget_policies(name, scope, base_policy_id, percent)
        VALUES('key_policy', 'key', $1, 25)
    `, baseID)
    require.NoError(t, err)
    
    // 3. 调用实际的 Reserve()
    reservations, err := billing.Reserve(ctx, e.db.Pool,
        memberID, keyID, accountID, groupID,
        requestID, decimal.NewFromInt(100), time.Now())
    
    // 不应该返回 "conn busy" 错误
    assert.NoError(t, err)
    assert.NotEmpty(t, reservations)
}
```

---

## 实施检查清单

### 阶段 1: 测试基础设施 (R5-03)
- [ ] 修改 `integration_test.go:cleanupTestData()` 添加 `account_holds`
- [ ] 运行连续测试验证清理正确
- [ ] 提交: `fix(test): add account_holds to cleanup list`

### 阶段 2: 核心计费和安全 (R5-01, R5-02)
- [ ] 实现 `billing.UsageFromTotal()` 转换函数
- [ ] 修改 `audit_handlers.go` 使用转换函数
- [ ] 添加 `TestR5_01_CachedTokenBilling` 集成测试
- [ ] 修改 `resources.go:patchAccount()` 检查 holds
- [ ] 修改 `scheduler.go:loadAccount()` 和 `groupAccounts()` 过滤 holds
- [ ] 添加 `TestR5_02_HoldBlocksActivation` 和 `TestR5_02_HoldBlocksScheduling`
- [ ] 提交: `fix(billing): correct cached token accounting in manual adjustments`
- [ ] 提交: `fix(accounts): prevent activation and scheduling of held accounts`

### 阶段 3: 管理 API (R5-05)
- [ ] 修改 `routes.go` 审核事件路由，剥离 `/review` 后缀
- [ ] 添加 UUID 格式验证
- [ ] 添加 `TestR5_05_AuditEventReviewURL` 测试
- [ ] 提交: `fix(admin): parse audit event review URL correctly`

### 阶段 4: 调度公平性 (R5-06)
- [ ] 重构 `scheduler.go:AcquireAccount()` 按优先级分组
- [ ] 修改 `rotate()` 使用 keyID
- [ ] 添加 `TestR5_06_EqualPriorityRotation` 测试
- [ ] 提交: `fix(scheduler): rotate among equal-priority routes`

### 阶段 5: 测试质量 (R5-04)
- [ ] 删除 `round4_fixes_test.go` 的硬编码数据库代码
- [ ] 重写测试使用 `newTestEnv()`
- [ ] 修改测试调用实际业务逻辑而非复制校验代码
- [ ] 提交: `refactor(test): use standard test environment for Round 4 tests`

### 阶段 6: 验证
- [ ] 运行完整单元测试: `go test ./internal/...`
- [ ] 运行完整集成测试: `go test ./tests/integration/...`
- [ ] 运行竞态检测: `go test -race ./...`
- [ ] 编译服务器: `go build ./cmd/server`
- [ ] 编译前端: `npm --prefix web run build`
- [ ] 更新 `ROUND5_VERIFICATION_CHECKLIST.md`

---

## 预期输出

### 代码变更
- **修改**: 5 个文件
  - `tests/integration/integration_test.go`
  - `internal/billing/billing.go`
  - `internal/admin/audit_handlers.go`
  - `internal/admin/resources.go`
  - `internal/admin/routes.go`
  - `internal/scheduler/scheduler.go`

- **新增**: 3 个测试文件或扩展
  - `tests/integration/round5_fixes_test.go` (扩展)
  - `tests/integration/round5_admin_validation_test.go` (扩展)

### 测试覆盖
- R5-01: 1 个集成测试（计费准确性）
- R5-02: 2 个集成测试（激活阻止 + 调度过滤）
- R5-03: 验证脚本（连续运行）
- R5-04: 重写现有测试
- R5-05: 1 个集成测试（URL 解析）
- R5-06: 1 个集成测试（轮换公平性）

### 文档更新
- `docs/ROUND5_FIXES_SUMMARY.md` - 修复总结
- `docs/ROUND5_VERIFICATION_CHECKLIST.md` - 更新验证状态
- `docs/ROUND5_IMPLEMENTATION_PLAN.md` - 本文档

---

## 风险评估

| 修复 | 风险等级 | 缓解措施 |
|-----|---------|---------|
| R5-01 | 🟡 中 | 影响历史数据，需要说明管理员如何修正错误账本 |
| R5-02 | 🟡 中 | 可能导致更多 "no healthy account" 错误，需要监控 |
| R5-03 | 🟢 低 | 仅测试基础设施，不影响生产 |
| R5-04 | 🟢 低 | 测试质量改进，不影响生产 |
| R5-05 | 🟢 低 | URL 解析修复，向后兼容 |
| R5-06 | 🟢 低 | 调度公平性改进，不改变正确性 |

---

## 后续工作

1. **Round 4 数据库依赖测试**: 
   - `TestR4_01_MigrationSchemaCompatibility`
   - `TestR4_04_PercentBudgetQueryNoBusy`
   - 需要 PostgreSQL 环境才能完全验证

2. **历史数据修正**: R5-01 修复后，需要工具或脚本重新计算错误的账本记录

3. **监控和告警**: R5-02 后，需要监控 "no healthy account" 错误率

4. **文档更新**: 
   - API 文档明确 `input_tokens` 语义
   - 管理员手册说明如何解除 hold

5. **性能优化**: R5-02 增加了子查询，需要确认索引效率
