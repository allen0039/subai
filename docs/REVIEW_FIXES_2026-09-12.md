# 审查修复记录（对应 INDEPENDENT_REVIEW_2026-09-12.md）

每项：审查发现 → 修复实现 → 回归证据。全部修复后 `make lint`、`make test`、`make test-integration`（20 项）通过。

## 上线前必须修复（P1）

| # | 发现 | 修复 | 回归证据 |
|---|---|---|---|
| 1 | Docker 构建缺少 migrations | deploy/Dockerfile build 阶段补 `COPY migrations`；新增 .dockerignore；web 改 `npm ci` | `docker build -f deploy/Dockerfile .` 完整通过（本日实测） |
| 2 | 生产准入与展示不一致；synthetic 价格可进数据面 | 新增 `gateway.Readiness` 统一判定（审核 Key + 非 synthetic 激活价格 + `SUBAI_PRODUCTION_READY` 运营开关）；数据面与管理页 `/api/admin/status`（含 `not_ready_reasons`）使用同一实例；测试模式需显式 `SUBAI_ALLOW_SYNTHETIC_PRICES=1` | tests/integration `TestUnverifiedPriceZeroUpstream`（503 + 上游零调用） |
| 3 | 客户端断连后无法结算/进 unknown | dispatch 之后全部收尾（Transition/Settle/MarkUnknown/RetainUnknown）改用 `context.WithoutCancel` + 5 分钟超时的 finishCtx；错误不再静默忽略（logStorageErr） | `TestClientDisconnectStillSettles`（流中取消→账本 1 条 charge、状态 completed） |
| 4 | 启动恢复未释放资金/未隔离账号/已记账被改 unknown | `OnStartupRecovery` 重写：未发送→逐请求事务释放预留并扣减 reserved→cancelled_before_dispatch；dispatch 后无账本 charge→unknown+预留转 unknown+账号 recovery_hold；有 charge→收敛 completed（修复记账与状态写之间的崩溃窗口）；返回 RecoverySummary | `TestStartupRecoveryReleasesFundsAndHoldsAccount`（断言 reservation released/unknown、reserved=0.25、账号 recovery_hold） |
| 5 | fallback 可能重放已发送请求 | `egress.Policy.Dispatch` 增加 `sent *bool`：任何字节已交付下游即 `ErrStreamStarted`，不切换出口不重放；HTTP 状态响应视为上游已接收也不重试；handler 收到流断错误一律进 unknown | `TestFallbackNoReplayAfterFirstFrame`（首帧后断连：上游计数=1、状态 unknown、预留保留） |
| 6 | OAuth 回调未挂外层路由；刷新未接入 | 路由组装提取到 `internal/server.Build`（主入口与测试共用同一 mux），`/api/oauth/callback` 挂外层；`gateway.Server.Refresh`（CredentialRefresher）在取凭证时对过期（<60s）token 先刷新，刷新失败且仍过期则拒绝 | `TestOAuthCallbackOnMainRouter`（真实主路由上回调成功 + state 重放失败） |
| 7 | SSE 丢失 CRLF 流、多行 data 拼接、无帧上限 | `internal/gateway/sse.go` 重写：按行解析（\n、\r\n、孤立\r）、多行 data 按 SSE 规范以 \n 连接、4MiB 帧上限、EOF 前的尾帧仍派发、读错误显式上抛；原始字节透传客户端 | internal/gateway `TestScanSSECRLF`、`TestScanSSETrailingFrameAndErrors`、`TestScanSSEFrameTooLarge` |
| 8 | 审核响应缺 flagged 字段被判 allow | `Flagged` 改为 `*bool`；results 数量必须恰为 1；flagged 缺失→unavailable（errType=missing_flagged_field） | `TestModerationMissingFlaggedField`（503 + 上游零调用） |

## 功能与运行可靠性（P2）

| # | 发现 | 修复 | 证据 |
|---|---|---|---|
| 9 | unknown 无处置入口、函数不闭环 | `POST /api/admin/requests/{id}/resolve-unknown`（管理员会话 + 证据 token 数 + 幂等 ON CONFLICT）；`AdjustUnknown` 收敛到 completed 终态、超预留走与正常结算相同的告警策略；全部 unknown 解决后账号自动恢复 active | `TestResolveUnknownFlow`（completed + adjustment×1 + 账号恢复 active） |
| 10 | 审核后复查用旧缓存/旧配置 | 新增 `auth.LookupKeyFresh`（绕过缓存直读 DB）；审核后用新快照**完整重验**（模型允许列表 + 并发上限取自新值）；admin 写操作通过 Reloader 调 `InvalidateKeyCache`（并重载规则/路由缓存） | 实现于 handler 第 7 步 + admin Reloader；撤销时效由缓存失效保证 |
| 11 | 调度忽略路由优先级、满载账号阻塞、Slots 实例不一致 | `scheduler` 重写：按路由 priority 分层遍历；层内跳过满载账号（limit>0 且 inflight≥limit）；`AcquireAccount` 选择+占槽一体（占槽失败换下一候选）；Slots 构造注入 | `TestSchedulerSkipsFullAccount`（满载 A 被跳过，选中空闲 B） |
| 12 | 全局等待无上限、body 内存限制未落实 | 队列重写为三界限：globalWaiting（等待数，独立于执行）、perKey、maxInFlight（执行信号量，`SUBAI_AUDIT_MAX_INFLIGHT` 默认 8）；`gateway.BodyMemoryGate` 对在途审核 body 计量，超限返回 429 queue_full（§22） | internal/audit/queue.go；handler 准入路径 |
| 13 | 界面无分页、默认值不真实、reviews 计数错、闭环缺失 | ResourcePage 分页（limit+1 探测下一页 + 上一页/下一页）；创建表单用字段真实 default 初始化；reviews 由后端解析为数组；新增 PATCH /budget-policies/{id}（启停）、DELETE /groups/{id}、DELETE /groups/{id}/accounts/{accountId} 及对应界面按钮 | web/src/components.tsx、App.tsx（build 通过） |
| 14 | 单活锁连接断开旧进程不停服 | main.go 启动 2s 周期监控锁连接（`pg_locks` 按 pid 校验），丢失即 `log.Fatal` 停服 | cmd/server/main.go `watchSingletonLock` |

## 其他建议落实

- 鉴权缓存容量上限（20k 条，溢出先清过期再清空）；StickyStore 写入时机会性清理过期项。
- Makefile 与 Dockerfile 统一 `npm ci`；.dockerignore 排除 node_modules/dist/bin。
- 集成测试在无 `TEST_DATABASE_URL` 时输出明确的 skipped 行（每个测试 `t.Skip` 可见），不再可能被误读为已执行。

## 未项（诚实清单）

- 真实上游/真实审核/真实 OAuth 的 P0 联调仍未进行（无凭证），兼容矩阵不变。
- 审计日志 14 天清理任务仍未实现（同前）。
- 压测脚本、OpenAPI 机器可读文件仍未交付。
- `TestClientDisconnectStillSettles` 用 recorder 模拟断连（ctx 取消），真实 TCP 断开的写失败路径待 P6 部署验收覆盖。
