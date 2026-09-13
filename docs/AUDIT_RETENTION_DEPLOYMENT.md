# 审计数据保留清理 - 部署指南

## 概述

审计数据保留清理功能自动删除超过保留期限的旧审计记录，防止数据库无限增长（P2-02修复）。

## 快速启动

### 1. 在主程序中启用清理工作器

在 `cmd/gateway/main.go` 或您的主入口文件中添加：

```go
import (
    "subai/internal/audit"
)

func main() {
    // ... 现有的初始化代码 ...
    
    // 启动审计数据保留清理工作器
    retentionCfg := audit.DefaultRetentionConfig()
    retentionDone := audit.StartRetentionWorker(ctx, db.Pool, retentionCfg)
    
    // 在程序退出时等待清理工作器完成
    defer func() {
        <-retentionDone
        log.Println("Audit retention worker stopped")
    }()
    
    // ... 其余的服务器启动代码 ...
}
```

### 2. 默认配置

`DefaultRetentionConfig()` 提供以下默认值：

- **事件保留期**: 90 天
- **审查保留期**: 365 天（1年）
- **清理间隔**: 每 6 小时

### 3. 自定义配置

如果需要调整保留策略，可以自定义配置：

```go
retentionCfg := audit.RetentionConfig{
    EventRetention:  60 * 24 * time.Hour,  // 保留 60 天的事件
    ReviewRetention: 180 * 24 * time.Hour, // 保留 180 天的审查
    CleanupInterval: 12 * time.Hour,       // 每 12 小时清理一次
}
retentionDone := audit.StartRetentionWorker(ctx, db.Pool, retentionCfg)
```

## 工作原理

### 清理流程

清理工作器执行以下操作：

1. **立即执行一次清理** - 启动时立即运行清理，处理启动前累积的旧数据
2. **定期清理** - 按配置的间隔（默认 6 小时）自动清理
3. **两阶段删除**:
   - Phase 1: 删除超过保留期的 `audit_reviews` 记录
   - Phase 2: 删除超过保留期且无关联审查的 `audit_events` 记录

### 例外保护

清理逻辑会自动保护活跃的审计例外：

```go
// 只删除已过期或没有例外的审查记录
DELETE FROM audit_reviews
WHERE created_at < $cutoff
  AND (expires_at IS NULL OR expires_at <= NOW())
```

即使审查记录本身很旧，只要其例外仍然有效（`expires_at > NOW()`），该记录就会被保留。

## 监控和日志

清理工作器会记录以下日志：

```
2026-09-13T10:00:00Z audit retention: deleted 1523 reviews (older than 2025-09-13T10:00:00Z), 3847 events (older than 2026-06-15T10:00:00Z)
```

### 推荐的监控指标

建议监控以下数据库指标：

```sql
-- 审计事件总数
SELECT COUNT(*) FROM audit_events;

-- 最旧的审计事件
SELECT MIN(triggered_at) FROM audit_events;

-- 审查记录总数
SELECT COUNT(*) FROM audit_reviews;

-- 活跃例外数量
SELECT COUNT(*) FROM audit_reviews 
WHERE exception IS NOT NULL 
  AND (expires_at IS NULL OR expires_at > NOW());
```

## 测试

### 单元测试

运行审计保留测试：

```bash
# 设置测试数据库
export TEST_DATABASE_URL="postgresql://user:pass@localhost:5432/test_db"

# 运行测试
go test -v ./internal/audit -run TestRetentionCleanup
```

### 手动测试清理

如果需要手动触发清理（用于测试或紧急情况）：

```go
import (
    "context"
    "subai/internal/audit"
)

// 手动清理 90 天前的数据
ctx := context.Background()
deleted, err := audit.CleanupOldEvents(ctx, pool, 90)
if err != nil {
    log.Printf("cleanup failed: %v", err)
} else {
    log.Printf("deleted %d old events", deleted)
}
```

## 数据库要求

确保 `audit_events` 表有以下索引以优化清理性能：

```sql
CREATE INDEX IF NOT EXISTS idx_audit_events_triggered_at 
ON audit_events(triggered_at);

CREATE INDEX IF NOT EXISTS idx_audit_events_account 
ON audit_events(account_id, triggered_at);
```

这些索引应该已经在迁移文件中创建。

## 故障排除

### 清理失败

如果看到清理失败的日志：

```
audit retention: cleanup failed: <error message>
```

**可能原因**：
1. 数据库连接问题
2. 外键约束冲突
3. 磁盘空间不足
4. 权限不足

**解决方法**：
1. 检查数据库连接和权限
2. 验证外键约束是否正确设置
3. 确保有足够的磁盘空间和 `DELETE` 权限

### 清理太慢

如果清理操作耗时过长：

1. **检查索引**：确保 `triggered_at` 和 `created_at` 字段有索引
2. **分批删除**：修改清理逻辑使用 `LIMIT` 分批删除大量记录
3. **调整清理间隔**：增加清理频率以减少每次清理的数据量

### 无法删除某些记录

如果某些旧记录没有被删除：

1. **检查例外状态**：验证这些记录是否有活跃的例外
2. **检查外键**：确认没有其他表引用这些记录
3. **查看日志**：检查是否有错误日志

## 生产环境建议

### 保留策略

根据您的合规要求调整保留期限：

- **金融/医疗行业**：可能需要保留 7 年或更长
- **一般 SaaS**：90-365 天通常足够
- **高流量系统**：考虑更短的保留期以控制数据增长

### 性能优化

1. **在低峰时段清理**：调整 `CleanupInterval` 使清理发生在流量较低的时段
2. **监控清理时长**：如果清理耗时超过 1 分钟，考虑优化或分批
3. **归档而非删除**：对于需要长期保留的数据，考虑归档到冷存储

### 备份

在启用自动清理前：

1. **确认备份策略**：确保有定期的数据库备份
2. **测试恢复**：验证可以从备份恢复审计数据
3. **记录保留策略**：在文档中明确说明数据保留期限

## 相关文档

- [P2 问题修复总结](./P2_FIXES_SUMMARY.md)
- [审计系统概述](../internal/audit/README.md)
- [数据库迁移指南](../migrations/README.md)
