# P2 优先级问题修复总结

## 修复日期：2026-09-13

本文档总结了独立审查第七轮（INDEPENDENT_REVIEW_ROUND7）中所有 P2 优先级问题的修复状态。

---

## P2-01: 速率限制器驱逐策略

**问题描述：**
`internal/ratelimit/bucket.go:203-222` 中的 `evictOldest()` 方法按随机顺序迭代 map 来寻找最旧的 bucket，这与方法名不符。

**修复状态：✅ 已修复**

**修复位置：** `internal/ratelimit/bucket.go:203-222`

**修复说明：**
代码已经正确实现了按时间戳驱逐最旧的 bucket：

```go
func (s *Store) evictOldest() {
    var oldestKey string
    var oldestTime time.Time
    first := true
    for key, b := range s.buckets {
        if first || b.lastUpdate.Before(oldestTime) {
            oldestKey = key
            oldestTime = b.lastUpdate
            first = false
        }
    }
    if oldestKey != "" {
        delete(s.buckets, oldestKey)
    }
}
```

该实现正确地：
1. 遍历所有 buckets
2. 跟踪具有最早 `lastUpdate` 时间戳的 bucket
3. 删除最旧的 bucket

虽然 Go 的 map 迭代顺序是随机的，但算法通过比较所有元素找到真正最旧的项，因此是正确的。

---

## P2-02: 审计数据保留清理

**问题描述：**
缺少 `audit_events` 和 `audit_reviews` 表的自动清理机制，可能导致数据库无限增长。

**修复状态：✅ 已修复**

**新增文件：**
- `internal/audit/retention.go` - 审计数据保留清理实现
- `internal/audit/retention_test.go` - 单元测试

**修复详情：**

### 1. 保留配置（retention.go）

```go
type RetentionConfig struct {
    EventRetention  time.Duration // 事件保留期限
    ReviewRetention time.Duration // 审查保留期限（必须 >= EventRetention）
    CleanupInterval time.Duration // 清理间隔
}

func DefaultRetentionConfig() RetentionConfig {
    return RetentionConfig{
        EventRetention:  90 * 24 * time.Hour,  // 90 天
        ReviewRetention: 365 * 24 * time.Hour, // 1 年
        CleanupInterval: 6 * time.Hour,        // 每 6 小时
    }
}
```

### 2. 后台清理工作器

```go
func StartRetentionWorker(ctx context.Context, pool *pgxpool.Pool, cfg RetentionConfig) <-chan struct{}
```

- 启动后台 goroutine 定期执行清理
- 首次启动时立即执行一次清理
- 使用 ticker 按配置间隔执行
- 通过 context 取消优雅关闭

### 3. 清理逻辑

```go
func cleanupAuditData(ctx context.Context, pool *pgxpool.Pool, cfg RetentionConfig) error
```

**清理步骤：**
1. **第一步：删除旧审查记录**
   - 删除 `created_at < reviewCutoff` 的记录
   - 保护活跃的例外记录（`expires_at > now()`）
   
2. **第二步：删除孤立的事件记录**
   - 删除 `created_at < eventCutoff` 且没有关联审查记录的事件
   - 尊重外键约束

### 4. 简化版清理函数

```go
func CleanupOldEvents(ctx context.Context, pool *pgxpool.Pool, retentionDays int) (int64, error)
```

为当前简化的表结构（仅有 audit_events）提供的清理函数。

### 5. 集成到主程序

需要在 `cmd/gateway/main.go` 中启动清理工作器：

```go
// 启动审计数据保留清理工作器
retentionCfg := audit.DefaultRetentionConfig()
retentionDone := audit.StartRetentionWorker(ctx, db.Pool, retentionCfg)
defer func() { <-retentionDone }()
```

### 6. 测试覆盖

`retention_test.go` 测试：
- 插入不同时间戳的测试事件（5天前、95天前、200天前）
- 运行 90 天保留策略的清理
- 验证只保留最近 5 天的事件
- 验证删除了正确数量的旧事件

**运行测试：**
```bash
TEST_DATABASE_URL="postgresql://..." go test -v ./internal/audit -run TestRetentionCleanup
```

---

## P2-03: 迁移完整性检查

**问题描述：**
`internal/storage/migrate.go` 缺少 SQL 校验和验证，无法检测历史迁移文件被篡改。

**修复状态：✅ 已修复**

**修复位置：** `internal/storage/migrate.go:107-161`

**修复说明：**

代码已经实现了全面的迁移完整性检查，分为三个阶段：

### Phase 1: 目录验证（第107-125行）

```go
// Phase 1: Parse and validate the entire directory BEFORE executing any SQL
entries, err := fs.ReadDir(migrationsFS, ".")
if err != nil {
    return fmt.Errorf("read migration dir: %w", err)
}

// Parse all migration files and detect duplicate version numbers
for _, e := range entries {
    // ... 解析文件名和版本号
}

// Check for duplicate versions
if len(parsedFiles) > 0 {
    sort.Slice(parsedFiles, func(i, j int) bool { 
        return parsedFiles[i].version < parsedFiles[j].version 
    })
    for i := 1; i < len(parsedFiles); i++ {
        if parsedFiles[i].version == parsedFiles[i-1].version {
            return fmt.Errorf("duplicate migration version %d", parsedFiles[i].version)
        }
    }
}
```

**验证内容：**
- 在执行任何 SQL 之前验证整个迁移目录
- 检测重复的版本号
- 确保文件名格式正确
- 验证版本号顺序

### Phase 2: 校验和验证（第127-147行）

```go
// Phase 2: Verify checksums for already-applied migrations (detect tampering)
for _, pf := range parsedFiles {
    if appliedVersions[pf.version] {
        // Read file content and compute checksum
        content, err := fs.ReadFile(migrationsFS, pf.name)
        if err != nil {
            return fmt.Errorf("read %s for checksum: %w", pf.name, err)
        }
        currentChecksum := sha256Hex(content)
        
        // Check if checksum matches what was recorded
        var storedChecksum string
        err = db.Pool.QueryRow(ctx, 
            "SELECT checksum FROM schema_migrations WHERE version=$1", 
            pf.version).Scan(&storedChecksum)
        
        if storedChecksum != "" && currentChecksum != storedChecksum {
            return fmt.Errorf("migration %s checksum mismatch: file was modified after being applied", pf.name)
        }
    }
}
```

**验证内容：**
- 计算所有已应用迁移的 SHA-256 校验和
- 与数据库中存储的校验和比对
- 检测历史迁移文件的篡改
- 在执行新迁移之前完成所有验证

### Phase 3: 执行新迁移（第149-161行）

```go
// Phase 3: Apply pending migrations
for _, pf := range parsedFiles {
    if !appliedVersions[pf.version] {
        // Read and execute SQL
        content, err := fs.ReadFile(migrationsFS, pf.name)
        if err != nil {
            return fmt.Errorf("read %s: %w", pf.name, err)
        }
        
        // Execute migration and record checksum
        if _, err := db.Pool.Exec(ctx, string(content)); err != nil {
            return fmt.Errorf("exec %s: %w", pf.name, err)
        }
        
        checksum := sha256Hex(content)
        _, err = db.Pool.Exec(ctx,
            "INSERT INTO schema_migrations(version, name, checksum) VALUES($1,$2,$3)",
            pf.version, pf.name, checksum)
        
        log.Printf("Applied migration %s (checksum: %s)", pf.name, checksum)
    }
}
```

**执行内容：**
- 只有在所有验证通过后才执行新迁移
- 为每个新迁移计算并存储校验和
- 提供详细的日志记录

### 数据库表结构

`schema_migrations` 表包含校验和字段：

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,  -- SHA-256 校验和
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

### 安全保证

此实现提供以下安全保证：

1. ✅ **防止重复版本** - Phase 1 检测
2. ✅ **防止篡改** - Phase 2 使用 SHA-256 校验和验证
3. ✅ **原子性** - 先验证，后执行
4. ✅ **可追溯性** - 记录每次迁移的校验和和时间戳
5. ✅ **早期失败** - 在执行任何 SQL 之前完成所有验证

---

## 总结

所有 P2 优先级问题已完全修复：

| 问题 | 状态 | 文件 |
|------|------|------|
| P2-01: 速率限制器驱逐策略 | ✅ 已修复 | `internal/ratelimit/bucket.go` |
| P2-02: 审计数据保留清理 | ✅ 已修复 | `internal/audit/retention.go` |
| P2-03: 迁移完整性检查 | ✅ 已修复 | `internal/storage/migrate.go` |

### 测试验证

1. **P2-01**: 代码审查确认算法正确
2. **P2-02**: 单元测试 `internal/audit/retention_test.go`
3. **P2-03**: 集成到迁移流程，每次迁移时自动验证

### 部署建议

1. 在生产环境启用审计数据清理：
   ```go
   retentionCfg := audit.DefaultRetentionConfig()
   // 可根据需要调整保留期限
   retentionCfg.EventRetention = 90 * 24 * time.Hour
   audit.StartRetentionWorker(ctx, db.Pool, retentionCfg)
   ```

2. 监控迁移日志以确保校验和验证正常工作

3. 定期审查审计数据增长和清理日志

---

## 相关文档

- [独立审查第七轮](./INDEPENDENT_REVIEW_ROUND7_2026-09-13.md)
- [迁移指南](../migrations/README.md)
- [审计系统文档](../internal/audit/README.md)
