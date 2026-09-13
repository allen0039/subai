# 审核容量（P0-03）

状态：blocked（无 Platform 审核 API Key 与真实账户限额）。本文记录设计参数、模拟测量方法与待实测清单。

## 设计参数（§6.3，可配置，非官方限制）

| 参数 | 默认 | 环境变量 |
|---|---|---|
| 全局等待队列 | 50 | SUBAI_AUDIT_QUEUE_MAX |
| 每 Key 等待 | 10 | SUBAI_AUDIT_PER_KEY_QUEUE |
| 排队期限 | 30s | SUBAI_AUDIT_WAIT_TIMEOUT_S |
| 单次调用超时 | 10s | SUBAI_AUDIT_CALL_TIMEOUT_S |
| 审核阶段总期限 | 60s（含多段与重试） | SUBAI_AUDIT_TOTAL_BUDGET_S |
| 瞬时错误重试 | ≤2，遵守 Retry-After | SUBAI_AUDIT_RETRIES |
| 缓存 TTL | 600s | SUBAI_AUDIT_CACHE_TTL_S |
| 吞吐预算 | 账户 RPM/TPM 的 80%（待实测后配置） | — |

## 模拟环境验证（已通过）

tests/integration（合成审核服务）：

- 500 / invalid JSON / 429+Retry-After → 统一 unavailable，上游零调用（TestModerationUnavailableZeroUpstream）。
- 完全相同输入第二次命中缓存，外部调用数=1（TestModerationCacheHit）。
- 审核拒绝/不可用/不支持时上游调用数为零（TestModerationFlagZeroUpstream 等，含计数断言）。

## 待实测清单（取得 Key 后回填）

1. omni-moderation-latest 账户实际 RPM/TPM 限额与等级。
2. 短输入（<1k token）与长输入（128k token 分段）P50/P95 延迟。
3. 429 比例与 Retry-After 分布；缓存命中率。
4. 图片输入实际支持格式/大小；不支持类别不得据零分认定安全（§5）。
5. 分段交叠窗口大小与段数上限的校准（§6.1 需真实长上下文负载）。

## 已知设计限制

- latest 别名可能升级；600s 短 TTL 只降低陈旧风险（§6.2）。
- 队列期限未经真实负载校准，不预先承诺全量实时审核容量（§11 容量用例）。
