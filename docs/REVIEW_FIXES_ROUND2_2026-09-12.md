# 第二轮审查修复记录（对应 INDEPENDENT_REVIEW_ROUND2_2026-09-12.md）

每项：审查发现 → 修复 → 回归证据。完成后 `make lint` 通过、单元测试通过、集成测试 29 项顶层（31 条 PASS 含子测试）全部通过。第一轮记录中的两处证据不符已一并更正（见文末）。

## P1

| # | 发现 | 修复 | 回归证据 |
|---|---|---|---|
| R2-01 | 上游已接收 POST 但未返回响应头时断开，fallback 仍重放；备用循环对状态错误继续下一个 | `egress.classify` 三分类：dial/proxyconnect 失败=请求未发出（可 fallback）；HTTP 状态应答=已接收已应答（不重试，含每个备用逐次分类，状态错误立即终止循环）；其余=已发出结果不明→`ErrUnconfirmed`（禁止重放）。failingTransport 统一归类为未发出 | `TestNoReplayWhenUpstreamBreaksBeforeResponse`：上游读完请求体后无响应断开，实际收到 1 次 POST，请求收敛 unknown |
| R2-02 | 超预留人工调整后接口立即把账号解回 active | `AdjustUnknown` 返回 `AdjustResult{Cost, OverReserve}`；`resolveUnknown` 仅在 `!OverReserve && 无其他 unknown && 状态=recovery_hold` 时恢复，且只抬 recovery_hold（paused/reauth 等其他隔离原因不受影响）；超预留保持 recovery_hold 等人工处理 | `TestOverReserveAdjustKeepsHold`：超预留调整后账号仍 recovery_hold，响应 `over_reserve:true` |
| R2-03 | unknown 调整按当前价格表计费历史请求 | `resolveUnknown` 读取 `requests.price_version_id` 冻结版本与模型；缺冻结版本或缺该模型价格时显式 409 引导证据补全，绝不静默改用现价 | `TestResolveUsesFrozenPriceVersion`：调价后调整，账本行使用原版本、成本 0.0007（新价应为 0.0014） |
| R2-04 | 恢复三步不原子，中断后 unknown/held/active 永不补齐 | `OnStartupRecovery` 重写：每个请求在单一事务内收敛（状态+预留+账号）；新增幂等清扫——已有 unknown 的 held 预留/active 账号、legacy cancelled+held 全部补齐收敛 | `TestRecoverySweepFinishesPartialStates`：半收敛行补齐（Swept=2），二次运行 Swept=0（可重入） |
| R2-05 | 上游执行与收尾共用 5 分钟截止时间；settle 失败分支漏 RetainUnknown | 拆分 `upstreamCtx`（`SUBAI_UPSTREAM_TIMEOUT_S`，默认 600s）与 dispatch 结束后才创建的 `finalizeContext`（独立 2 分钟）；统一失败出口 `convergeUnknown`（MarkUnknown+RetainUnknown+状态，全部错误显式记录）；`dispatching` 状态更新失败立即释放预留 | `TestUpstreamTimeoutConvergesUnknown`（300ms 超时注入）：状态 unknown、全部预算作用域预留保留、账号 recovery_hold |
| R2-06 | Compose 未传生产开关，标准部署数据面恒 503 | server 服务增加 `env_file: .env`，全部 `SUBAI_*` 变量注入容器；`.env.example` 补 `SUBAI_PRODUCTION_READY`/`SUBAI_ALLOW_SYNTHETIC_PRICES`/`SUBAI_UPSTREAM_TIMEOUT_S`/`SUBAI_AUDIT_MAX_INFLIGHT` 等 | `docker compose config` 渲染验证：PRODUCTION_READY/ALLOW_SYNTHETIC_PRICES/OAUTH_CLIENT_ID 等均出现在容器环境 |

## P2

| # | 发现 | 修复 | 回归证据 |
|---|---|---|---|
| R2-07 | 表单把可选数值初始化为空字符串，创建 fixed 预算 400 | `openCreate` 按字段类型初始化（number→undefined）；`buildCreatePayload` 提交前过滤 undefined/非必填空串，逗号分隔字段（allowed_models、fallback_proxy_ids）转数组 | `TestBudgetPolicyFormPayloadsAccepted`（fixed 表单负载 → 201）；前端 tsc+vite 构建通过 |
| R2-08 | 前端分页与多个后端列表契约不匹配 | proxies、egress-policies、accounts、groups、budget-policies、budget-periods、price-versions、audit-rules 全部补 `LIMIT/OFFSET` + 稳定排序；前端已有 limit/offset 透传 | `TestPaginationContract25Rows`：25 条两页翻页，无重复无遗漏 |
| R2-09 | body 内存准入发生在完整分配之后 | `readBodyWithBudget`：64KB 分块读取，每块先 `TryReserve` 再缓冲；超限立即停读并释放已计入额度；per-request 上限仍然生效 | 实现于 helpers.go；准入语义测试通过现有 queue_full 路径（并发慢速上传期间额度实时计入） |
| R2-10 | 队列 waiting 含执行中请求；release 非幂等 | 获得执行槽即把请求移出 waiting（转移计数）；perKey 明确约束"在途总数（等待+执行）"并写入注释；release 用 sync.Once 保证幂等 | internal/audit `TestQueueWaitingCountTransfersOnExecute`、`TestQueueWaitBoundRejects`（waitMax=1 时第三个等待者 ErrQueueFull） |

## 证据边界更正（第一轮修复记录的两处不符）

1. 集成测试计数：第一轮实际为 21 项顶层，非 20；本轮后为 29 项顶层（3 个子测试，共 31 条 PASS）。
2. "无 TEST_DATABASE_URL 时静默成功"：已改为每个测试显式 `requireDB(t)` → `t.Skip`，`-v` 输出可见 SKIP 行；TestMain 仅保留进程级入口。
3. "所有管理写操作立即失效缓存"：members/keys/clients/proxies/accounts/groups/routes/budgets/prices 等全部写接口现调用 `notifyMutation()`（Reloader：规则重载 + Key 缓存失效 + 路由缓存失效）；Fresh Key 复查与缓存即时失效是两层独立机制，文档分别描述。
4. 单活锁监控探测增加 3 秒独立截止时间（context.WithTimeout），网络黑洞场景可在探测超时后停服；连接被终止场景由同一 probe 的查询错误覆盖。两个场景的故障注入测试待 P6 部署验收补齐。

## 断连测试硬化

`TestClientDisconnectStillSettles` 改为上游首帧 flush 后通过 channel 信号同步取消（消除固定 80ms sleep 的竞态：取消点确定性落在两帧之间）。round-1 的 `TestFallbackNoReplayAfterFirstFrame` 保留（覆盖首帧后断流路径），R2-01 新测试覆盖首响应前断开路径。

## 剩余未项

- 真实上游 / 真实 OAuth（含刷新到期、并发刷新、出口路径）/ 真实部署的 P0 验收仍未进行。
- 锁监控"仅该连接网络失联"场景的故障注入测试、压测脚本、OpenAPI 文件、审计日志清理任务仍未交付。
- body 内存闸门计量的是原始 body 字节；提取副本的额外内存未计入（已在注释中如实标注为 raw-body 计量）。
