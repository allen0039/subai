# 审查交接报告（docs/REVIEW_HANDOFF.md）

按 DEVELOPMENT_HANDOFF.md 模板编写。日期：2026-09-12。本报告由实施工具生成，供独立审查使用；本实现未经审查，不得视为已通过验收。

> **2026-09-12 复审后更新**：独立审查（INDEPENDENT_REVIEW_2026-09-12.md）提出的 8 项 P1、6 项 P2 已全部修复并附回归测试，逐项对照见 docs/REVIEW_FIXES_2026-09-12.md；第二轮审查（INDEPENDENT_REVIEW_ROUND2_2026-09-12.md）的 6 项 P1、4 项 P2 亦已全部修复，见 docs/REVIEW_FIXES_ROUND2_2026-09-12.md。集成测试现为 29 项顶层（3 个子测试）；本轮修复了 fallback 在"上游已接收未响应"场景下的重放、超预留隔离撤销、历史请求按现价计费、恢复不可重入、收尾 context 与执行共用截止时间、Compose 生产开关未注入，以及表单数值/分页契约/body 内存准入时序/队列等待计数四项 P2。本文第 2、3、4 节中与旧实现对应的表述以修复记录为准：集成测试现为 20 项（新增 7 项回归）；"公平队列"含独立的全局等待计数与执行上限（SUBAI_AUDIT_MAX_INFLIGHT）；"单活保证"含锁连接失效自动停服；管理面 CRUD 已补预算启停与账号组删除/移除；生产价格准入由统一 Readiness 落地（synthetic 需显式 SUBAI_ALLOW_SYNTHETIC_PRICES=1）。
>
> 本节修正前原报告声明的"每个写接口持久化回读集成测试"等表述中，部分（如出口故障集成断言）当时以代码审查级保证代替了测试，已在新回归中补齐 fallback 零重放断言。

## 1. 实现版本与启动步骤

- 版本：第一版实施完成（对应 IMPLEMENTATION_PLAN.md v0.2）；Go 1.27.1、Node 24、PostgreSQL 16。
- 启动（本地）：
  ```bash
  export SUBAI_MASTER_KEY=$(openssl rand -hex 32)
  make dev   # SUBAI_DEV_BOOTSTRAP_ADMIN=admin:pass 创建首个管理员（dev only）
  ```
- 启动（Compose）：`cd deploy && cp .env.example .env && docker compose up -d --build`，详见 deploy/README.md。
- 数据面在未配置 `SUBAI_MODERATION_API_KEY` 时保持未就绪（503 明确提示），不会静默连接真实上游。

## 2. 已实现 / 未实现 / 偏离

### 已实现（模拟链路 + 真实持久化）

- 动态主体：members/clients/api_keys/keys 路由全部数据库管理，无固定数量假设（第 6+ 客户端接入测试通过）。
- 数据面：POST /v1/responses（SSE 转发 + usage 结算）、GET /v1/models；chat/completions 与 WS 明确 422 不支持。
- 审核前置：AuditDocument 提取（含工具参数/输出的受限深度提取）、本地规则引擎（11 条默认规则、每条 ≥2 正反例 fixture、RE2 线性时间正则、规范化副本匹配、秘密命中不外发）、HMAC 版本化缓存、公平队列（全局/每 Key 上限 + 等待期限）、官方 Moderation 适配器（unavailable fail-closed）。
- 预算：NUMERIC(30,12) decimal、时区感知周期（day/week，快照不可覆盖）、fixed/percent（bps，引用 fixed 基础策略）、原子多作用域预留（按 policy_id 顺序锁行）、幂等结算（唯一索引 + ON CONFLICT）、unknown 状态保留资金、actual>reservation 不截断并告警。
- 调度：路由优先级、组内低并发优先 + 轮转、会话黏性、双层并发槽。
- 出口：direct/http/socks5 profile、egress 策略 failure_mode=stop/fallback、备用耗尽返回失败不隐式直连、探测使用服务端固定目标。
- OAuth：随机 state + PKCE + 10 分钟单次消费 + 每账号刷新互斥 + credential_version 乐观保护。
- 管理面：§17.2 全部资源 CRUD + 乐观锁 409 + 分页 + 秘密单次显示 + admin_events 脱敏；React 管理界面（浏览器实测：登录、状态页、审核规则表、真实 API 读取）。
- 状态机与恢复：§19 全部状态、崩溃恢复（未发送→cancelled_before_dispatch，已发送→unknown+保留预留）、启动恢复先于接受流量。

### 未实现 / blocked（需真实凭证与环境）

- P0-01..P0-04 真实验证（上游协议、费用上界实测、审核容量、价格源解析）——见 docs/COMPATIBILITY.md、BILLING_BOUNDS.md、AUDIT_CAPACITY.md、PRICE_SOURCE.md。
- 真实 Codex OAuth 端点联调（合成授权服务器已测）。
- 价格自动同步的官方解析器（sync 接口明确返回 not_verified，不伪造）。
- 图片审核、输出缓冲审核（§7 后续项）。
- 审计日志 14 天自动清理任务（保留期已配置，清理 job 未实现——见"偏离"）。

### 与规格的偏离

1. D-001：通过注入 max_output_tokens 获得可证明输出上界——规格允许但需 P0-02 验证上游尊重该参数。
2. D-005：迁移只前进，无自动回滚（备份恢复代替）。
3. D-007：单活用数据库 advisory lock 强制；队列/缓存/并发槽为进程内存态。
4. 审计日志清理 job 未实现：log_retention 配置存在但无后台清理循环。**这是已知缺口**，审查时应确认是否阻塞验收。
5. WebSocket/chat/completions 以 422 unsupported_transport 显式拒绝（§10 允许的缩小支持范围）。

## 3. 测试命令、结果、真实与模拟的区别

```bash
make lint              # PASS（go vet + gofmt）
make test              # PASS：internal/audit 规则正反例、提取器、缓存键绑定
make test-integration  # PASS：13 项集成测试（dockerized PostgreSQL + 合成上游/审核）
```

集成测试关键断言（tests/integration/integration_test.go）：

- 审核拒绝/不可用/不支持/预算不足/无路由/坏 Key 时**上游请求数为零**（计数器断言，非日志推断）。
- 秘密命中：审核 API 与上游调用数均为 0，审计摘要无完整秘密。
- 缓存：相同输入第二次审核外部调用数=1。
- 结算幂等：8 并发 Settle 只产生 1 条 charge 行。
- 启动恢复、第 6+ 动态客户端、OAuth state 单次消费。

**真实与模拟的区别**：全部上游/审核交互均为合成服务；真实凭证下的行为（SSE 事件 schema、usage 字段、429 分布、图片支持）一律未验证，标记 pending/blocked，不构成兼容证据。

## 4. 核心安全性质证据

- **审核前置**：pipeline 在账号选择与预留之前；`audit_failure_mode=stop`；审核决定先持久化后 dispatch；日志失败则上游前停止（recordAuditEvent 错误路径）。测试：TestModerationFlagZeroUpstream、TestModerationUnavailableZeroUpstream。
- **预算并发**：先预留后 dispatch；多作用域原子性（事务回滚无部分预留）；唯一索引幂等。测试：TestBudgetExceededZeroUpstream、TestNoBudgetRefused、TestSettleIdempotent。
- **出口隔离**：代理故障 failure_mode=stop 不直连；fallback 耗尽报错；系统环境代理不继承。测试：egress 代码路径 + 运行手册探测命令（单元级验证在 egress 包，集成断言在代理断开场景待补——见第 6 节）。
- **重启恢复**：TestStartupRecovery + 恢复先于流量（main.go 顺序）。

## 5. 依赖与外部接口限制；需要用户提供

- 依赖：pgx v5、shopspring/decimal、x/crypto、x/net、yaml.v3（go.sum 锁定）；web 端 react/react-dom/vite/typescript（package-lock.json 锁定）。
- 需要用户提供（进入真实联调前）：
  1. Codex 上游真实端点与 SSE schema 样本（P0-01），或允许以真实账号抓取；
  2. Platform 审核 API Key 与账户限额等级（P0-03）；
  3. OAuth authorize/token endpoint、client_id、注册回调域名（§22：不臆造可用域名）；
  4. 官方价格页机器可读性确认或认可的替代来源（P0-04）；
  5. VPS 环境（OS、域名、代理连通方式）用于 P6 真实部署验收。

## 6. 建议审查优先检查的文件与未解决问题

优先文件（对应 §24 审查重点）：

1. internal/gateway/handler.go — 生命周期顺序：审核→重查 Key→选号→预留→并发→dispatch；错误映射。
2. internal/billing/reserve.go — 预留锁顺序、幂等结算、unknown/adjustment、超预留告警。
3. internal/audit/pipeline.go + rules.go + extract.go — 覆盖级别判定、秘密命中不外发、缓存键版本绑定。
4. internal/egress/egress.go — 无隐式直连；fallback 语义。
5. internal/accounts/oauth.go — state 单次消费事务；刷新互斥。
6. cmd/server/main.go — 单活锁、种子不覆盖管理员改动、启动恢复顺序。
7. migrations/0001_init.sql — 约束完整性（外键/唯一/CHECK）。

未解决问题：

1. 审计日志 14 天清理 job 缺失（偏离 4）。
2. 代理断开场景缺少专门集成断言（当前为代码审查级保证 + 探测接口）；建议补 TestEgressProxyDown。
3. 客户端取消（断连）路径未在集成层测试。
4. `requests` 无 TTL 清理（完整 body 本就不落库，但状态行会累积；需运维策略）。
5. 管理界面未做无障碍与移动端适配（功能可用性已浏览器实测）。
6. `estimateReservation` 对超长输入（数 MB）预留可能显著高于实际——保守方向的偏差，符合严格预算语义但影响体验，待 P0-02 校准。

## 7. 交付物清单

- 源码 + go.sum / web/package-lock.json 依赖锁 ✓
- migrations/0001_init.sql ✓；rules/defaults/rules.yaml + tests/fixtures/rules_fixtures.yaml ✓
- docs/API.md（端点契约）✓；OpenAPI 文件未单独生成（API.md + 代码为准）——如需机器可读 OpenAPI 请列入下阶段
- deploy/：Dockerfile、docker-compose.yml、Caddyfile、.env.example、README（运行+备份恢复）✓
- docs/：COMPATIBILITY、BILLING_BOUNDS、AUDIT_CAPACITY、PRICE_SOURCE、DECISIONS、KNOWN_LIMITATIONS、IMPLEMENTATION_STATUS、本报告 ✓
- 测试执行入口：Makefile（lint/test/test-integration/build/migrate/dev）✓
