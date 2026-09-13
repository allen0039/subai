# 实施状态

对照 IMPLEMENTATION_PLAN.md v0.2 第 23 节任务拆分逐项记录。状态含义：
`passed`（有可复现命令与结果）、`failed`、`not_run`（未执行）、`blocked`（缺少环境，如真实凭证）。
本文件随开发更新；"模拟"指使用 tests/integration 中的合成上游/合成审核服务，真实联调一律标记为 not_run 或 blocked，不计入通过率。

最后更新：2026-09-12（第一版实施完成；第一轮审查 8×P1+6×P2 与第二轮审查 6×P1+4×P2 全部修复，见 docs/REVIEW_FIXES_2026-09-12.md 与 docs/REVIEW_FIXES_ROUND2_2026-09-12.md）。

## 复现命令与结果（2026-09-12 实测）

```text
make lint             → PASS（go vet ./... + gofmt 全树）
make test             → PASS（ok subai/internal/audit：规则正反例/提取器/缓存键/归一化）
make test-integration → PASS（29 项顶层 / 31 条 PASS 含子测试 = 13 原有 + 8 第一轮回归 + 8 第二轮回归，dockerized PostgreSQL 16）
docker build -f deploy/Dockerfile . → PASS（镜像含 migrations 与 web/dist；P1-1 验收）
端到端（本地 :8081，真实库）：
  - 登录/成员/客户端/Key 创建 → 201，明文 Key 仅出现一次
  - 默认 11 条规则入库（builtin）；/api/admin/status 返回真实计数
  - 坏 Key → 401 invalid_api_key；无路由 → 403 access_denied；
    未配置审核 Key → 503（明确 not-ready 提示）；chat/completions → 422 unsupported_transport
  - 第二实例启动 → singleton lock 拒绝退出
  - 管理界面浏览器实测：登录、状态页、审核规则表渲染真实数据
```

| 任务ID | 阶段 | 状态 | 证据/说明 |
|---|---|---|---|
| P0-01 | 协议验证 | blocked | 无真实 Codex 客户端与 Pro 账号凭证；docs/COMPATIBILITY.md 矩阵就绪，真实项均 pending |
| P0-02 | 费用上界 | blocked | 设计与实现见 docs/BILLING_BOUNDS.md（注入 max_output_tokens + 保守输入估算）；真实有效性待凭证 |
| P0-03 | 审核容量 | blocked | docs/AUDIT_CAPACITY.md 含设计参数与模拟验证；真实限额/延迟待 Platform Key |
| P0-04 | 官方价格源 | blocked | sync 接口明确返回 not_verified 不激活；docs/PRICE_SOURCE.md |
| P1-01 | 基础服务 | passed | 集成测试 happy path/坏 Key/无路由/错误映射 + 端到端实测（见上） |
| P1-02 | OAuth | passed（模拟）/ blocked（真实） | PKCE/state 单次消费/刷新互斥以合成授权服务器验证（TestOAuthPKCEFlow）；真实端点待配置 |
| P2-01 | 默认规则 | passed | 11 条规则 + 每条 ≥2 正反例（internal/audit rules_test）；凭证命中零外发、日志遮蔽（TestSecretBlockedZeroUpstreamZeroModeration） |
| P3-01 | 官方审核 | passed（模拟）/ blocked（真实） | 提取覆盖测试；flagged/500/invalid_json/429 → 上游零调用（计数断言） |
| P3-02 | 排队缓存 | passed | TestModerationCacheHit（外部调用数=1）；队列上限/等待期限实现于 internal/audit/queue.go |
| P4-01 | 预算 | passed | 无预算拒绝/预算不足 429/8 并发结算幂等（TestSettleIdempotent）/unknown 保留资金 |
| P4-02 | 调度 | passed | 无路由 403、双层并发槽、组选择；重启恢复（TestStartupRecovery） |
| P5-01 | 管理界面 | passed | 全部写接口真实持久化（端到端 curl）+ 浏览器实测渲染真实数据；乐观锁 409 在 patch 路径 |
| P6-01 | 部署恢复 | passed（模拟环境） | Compose/Caddyfile/备份恢复手册交付；单活锁实测；启动恢复测试通过。完整新库部署+备份恢复演练待 VPS 执行 |
| P6-02 | 扩展接入 | passed（模拟） | TestSixthClientDynamic：6 个新客户端零代码接入；压测脚本未交付（记录于 REVIEW_HANDOFF 未解决问题） |

| 复审项 | 状态 | 对照 |
|---|---|---|
| 第一轮 P1-1..P1-8（Docker/准入/断连/对账/重放/OAuth路由/SSE/审核字段） | fixed + 回归 | docs/REVIEW_FIXES_2026-09-12.md §P1 |
| 第一轮 P2-9..P2-14（unknown 处置/权限重验/调度/队列内存/界面闭环/锁监控） | fixed | 同上 §P2 |
| 第二轮 R2-01..R2-06（重放分类/隔离保持/冻结价格/恢复可重入/收尾context/Compose注入） | fixed + 回归 | docs/REVIEW_FIXES_ROUND2_2026-09-12.md |
| 第二轮 R2-07..R2-10（表单类型/分页契约/内存准入时序/队列计数） | fixed | 同上 |

## 已知未验证核心项（真实联调开关默认关闭）

- Codex 上游真实协议（端点、SSE schema、usage 字段）——P0-01。
- 官方 Moderation 真实分类与图片支持——P0-03。
- 官方价格页机器可读性——P0-04。
- max_output_tokens 在真实上游的约束有效性——P0-02。

