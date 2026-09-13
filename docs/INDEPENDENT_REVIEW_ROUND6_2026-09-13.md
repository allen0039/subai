# 第六轮完整独立审查（2026-09-13）

## 结论

当前版本**不能作为验收通过版本**。第五轮指出的迁移加载、百分比预算查询、本地审核动作、账号 hold 调度阻断、审核复核路径拆分和同优先级轮转已有明显改善，但本轮仍确认 8 项高优先级问题和 5 项中优先级问题。其中恢复接口、OAuth 重新授权、审核规则管理、审核例外和失败终态均存在可重复的功能断点；完整 lint、单元测试和集成测试也都没有通过。

本轮只审查和验证，没有修改业务代码。所有动态测试使用独立 PostgreSQL 16 和合成上游/审核服务，没有连接真实账号、真实 Codex 上游或真实 Moderation API。

## 高优先级问题

### P1-01：真实 unknown 请求永远无法通过人工处置接口

网关生成的请求 ID 是 `req_` 加 32 位十六进制字符串（`internal/gateway/state.go` 的 `NewRequestID`），数据库也把 `requests.id` 定义为 `TEXT` 形式的 opaque ID。（`migrations/0001_init.sql:172-174`）。但管理路由在去掉 `/resolve-unknown` 后使用 UUID 正则验证请求 ID（`internal/admin/routes.go:243-249`）。

这不是边界条件，而是所有真实请求都会命中的格式冲突。排除无法编译的新测试文件后，5 项集成测试均因此得到 `400 invalid request ID`，包括原有 unknown 处置、超预留处置、冻结价格处置和第五轮缓存 token 调整用例。

建议：为请求 ID 使用与 `NewRequestID` 一致的明确校验器，或仅校验长度和允许字符；不要复用资源 UUID 校验。修复后必须用真实生成的 ID 跑完整 HTTP 处置流程，并验证幂等、账本、预留和账号恢复。

### P1-02：OAuth `reuse_account_id` 不会复用账号，重新授权会创建新账号

`StartSession` 会把 `reuse_account_id` 写入 `oauth_sessions.account_id`，但 `CompleteCallback` 查询会话时不读取该列，并且回调完成后无条件 `INSERT INTO accounts`（`internal/accounts/oauth.go:99-139`）。原账号的密文凭证、`credential_version` 和 `reauth_required` hold 都不会更新或解除。

独立探针中，传入原账号 UUID 创建 OAuth 会话后，回调返回了另一个新 UUID，而会话表仍显示原 UUID。管理界面发起授权时也没有发送 `reuse_account_id`（`web/src/App.tsx:473-506`），所以界面没有重新授权入口。

建议：回调事务内锁定并读取会话绑定账号；复用时更新原账号凭证和版本、清除 `reauth_required`、仅在不存在复用账号时创建新账号。界面应从账号行发起绑定该账号的重新授权。

### P1-03：审核规则编辑接口和界面均不可用

规则列表返回数据库行 UUID `id` 和业务规则标识 `rule_id`。通用界面若编辑资源会使用 `row.id`；路由也只允许 UUID（`internal/admin/routes.go:206-213`），但 `patchAuditRule` 随后把这个 UUID 当成 `rule_id` 查询（`internal/admin/audit_handlers.go:350-353`）。对一条 seed 规则的真实 PATCH 探针返回 `404 rule not found`。

同时，审核规则页面没有传入任何 `editFields`（`web/src/App.tsx:353-367`），因此用户界面连编辑按钮都不会显示。即使修正 ID，`validateRulePatch` 也只反序列化 matcher，没有调用规则编译器检查正则和 matcher 约束（`internal/admin/audit_handlers.go:397-405`）。

建议：统一路由契约，明确使用行 UUID 或 `rule_id`；保存前以完整 Rule 调用 `audit.Compile`；补规则变更的真实 HTTP、重载和请求命中测试。

### P1-04：“精确例外”只写记录，不会影响任何后续审核

`exception_created` 会向 `audit_reviews` 写入 JSON 和到期时间（`internal/admin/audit_handlers.go:473-515`），但全仓生产代码没有任何读取或应用 `audit_reviews` 的路径。API、界面和实施计划都声称可以创建带 TTL 的精确例外，实际行为只是保存一条审计数据。

界面还只提交 `{outcome, note}`，没有在选择 `exception_created` 时收集后端必需的 `exception_rule_id`（`web/src/App.tsx:408-415`），所以从界面创建例外会直接得到 400。

建议：定义例外对 rule、category、key/member、内容摘要等维度的精确作用域，在本地规则判定前查询仍有效的例外，并把命中例外写入审计事件；或删除“创建例外”能力和相关承诺，只保留复核记录。

### P1-05：`response.failed` 会被记账后标记为 `completed`

usage 提取器接受 `response.completed` 和 `response.failed`，但 handler 只保存 usage 数值，不保存终态事件类型。流正常 EOF 后，只要 usage 已知，就会结算并无条件把请求置为 `completed`（`internal/gateway/handler.go:436-492`）。

独立合成上游只返回 `response.failed` 和有效 usage。结果是 HTTP 流原样包含 `response.failed`，账本新增 1 条 charge，但数据库请求状态为 `completed`。管理状态与客户端看到的上游结果相反，故障统计、重试决策和运营排查会失真。

建议：结算和业务终态分开建模。失败事件可以按协议确认的 usage 计费，但请求应进入明确的失败终态，并保留上游错误信息；为 completed、failed、incomplete 和异常 EOF 分别建立回归测试。

### P1-06：无效审核配置可以触发进程空指针 panic

`config.Load` 只解析数字，没有验证并发、队列、超时、重试次数、请求大小和输出上限的有效范围（`internal/config/config.go:59-103`）。当 `SUBAI_AUDIT_RETRIES=-1` 时，Moderation 客户端一次都不执行，返回 `outcome=nil, err=nil`，`Pipeline.Run` 随后解引用 `outcome.Categories` 并 panic。

该行为已用最小探针捕获为 `runtime error: invalid memory address or nil pointer dereference`。零或负总预算、并发和大小限制也会形成不同的拒绝、绕过或错误行为。

建议：启动时集中验证所有配置范围和相互关系，错误配置应以清楚的错误拒绝启动；Moderation 客户端还应保证任何 nil outcome 都以 error 返回。

### P1-07：未知固定费用维度被静默按 0 计费

`fixedFeeTotal` 的注释和 `docs/BILLING_BOUNDS.md` 都要求未知费用维度使严格上界失效并拒绝请求，但实现遇到未知键后直接返回 `decimal.Zero`，`ModelPriceFor` 仍返回成功（`internal/billing/billing.go:108-143`）。

探针把模型的 `fixed_fees` 设置为 `{"unknown_dimension":"1.25"}`，读取结果为 `fixed_fee=0` 且无错误。这会在未来官方同步或数据库导入出现新计费维度时低估预留并漏计费用。

建议：`fixedFeeTotal` 返回 `(decimal.Decimal, error)`，未知键、非法结构和非法金额都必须使该模型价格不可用；增加严格拒绝测试。

### P1-08：发布测试门禁处于失败状态，修复报告中的通过结论不成立

当前完整测试不是“部分失败”，而是有三种独立故障：

1. `make lint` 失败：`tests/integration/round5_admin_validation_test.go:21` 调用不存在的 `hashToken`。
2. 完整 integration 包无法编译：同文件第 67 行把 `*strings.Reader` 赋给 `io.ReadCloser` 类型的 `req.Body`。
3. `go test -race ./internal/...` 的 `TestModelPriceCost` 三个用例失败 1000 倍。生产公式按每百万 token 计算是正确的，测试却用 `2000/200/10000` 表示 `$2/$0.2/$10`，测试价格单位写错。

排除无法编译的文件后，46 个顶层集成测试只有 40 个通过；5 个因 P1-01 失败，另 1 个测试错误地要求 `cached_tokens <= uncached_input_tokens`，混淆了内部 Usage 契约。`docs/ROUND5_FIXES_SUMMARY.md` 所称“PASS（7 个测试）”和“所有测试在有数据库时通过”与当前可执行证据不符。

建议：先修复测试代码和契约，再将 lint、race、完整 integration 作为统一阻断门禁；文档只记录实际执行过的命令、版本、测试数和结果。

## 中优先级问题

### P2-01：管理写接口的 HTTP 方法门禁不一致

独立探针以 `DELETE /api/admin/requests/<UUID>/resolve-unknown` 携带 JSON，接口返回 200 并把 unknown 请求改为 completed。静态检查还确认：

- `GET /api/admin/keys/{id}/revoke` 可以撤销 Key；
- `GET /api/admin/proxies/{id}/test` 会发出外部探测；
- member、client、key、proxy、budget policy 和 audit rule 的单资源路由没有统一要求 PATCH；
- 多个只读集合端点没有拒绝非 GET 方法。

建议：路由层先按资源和动作执行精确方法分派，错误方法统一返回 405 和 `Allow` 头；为每个 mutation 补至少一个错误方法测试。

### P2-02：规则集版本不能唯一标识实际规则快照

`LoadRuleset` 把每条规则的 `Version` 固定为 1，并用 `rule_id/action/matcher.type/enabled` 计算最终版本；matcher pattern、scope、severity、message 和数据库真实版本均未参与（`internal/audit/seed.go:48-82`）。

探针创建 SECRET-001 的新版本并修改 pattern 后，加载前后的 ruleset version 都是 `a15c0dad9f6379c3`。这使审计事件中的 `rule_version` 无法证明当时使用的具体规则内容，规则重载也不会按版本自然区分审核缓存。

建议：对规范化后的完整规则快照计算稳定哈希，包含真实规则版本和所有影响匹配、动作与证据的字段；规则重载成功后可显式清理旧缓存。

### P2-03：登录失败限速表可被匿名请求无限扩张

登录失败记录使用进程内 `map[username|ip][]time.Time`。过期记录只在同一个 key 再次登录时清理，随机用户名会永久留下不同 map key（`internal/auth/auth.go:189-206`）。未认证请求可持续制造新 key，形成内存耗尽风险。

建议：使用有全局容量和定期回收的限速器，或在外层代理/共享存储中限速；`clientIP` 还需按明确的可信代理配置解析来源地址。

### P2-04：管理会话同时暴露给 JavaScript，部署又没有浏览器安全头

服务端设置 HttpOnly、Secure、SameSite=Strict Cookie，但登录响应也返回原始 token，前端长期保存到 `localStorage` 并在每次请求中设置 Bearer（`internal/admin/admin.go:86-105`、`web/src/api.ts:1-29`）。任何同源 XSS 都能读取管理员会话。Caddyfile 未设置 CSP、frame、content-type 和 referrer 等基础安全头。

建议：同源管理界面只使用 HttpOnly Cookie，不向 JavaScript 返回或持久化 token；同时为静态管理界面设置严格 CSP 和常用响应安全头。

### P2-05：多个配置项和日常运维闭环仍未实现

`AdminOrigin`、`PriceSyncInterval`、`PriceSourceURL`、`LogRetention` 和 `PerAccountConcurrency` 只被加载，生产代码没有使用。官方价格同步按钮固定返回 `not_verified`，界面没有手工价格覆盖表单；状态页只显示 unknown 数量，没有请求列表和处置入口；审核规则和 OAuth 重新授权也缺少可用界面流程。

日志保留缺失已在旧文档中列为已知限制，但 README 和实施状态仍把 P5/P6 描述为完成。数据库中的 requests、audit/admin events 和过期 session 会持续增长，需要明确清理和归档策略，账本则应独立保留。

## 迁移与部署复核

迁移加载器现在会排除 `.down.sql`、按数值版本排序，并在单个事务中应用每个迁移；`0001`、`0002`、`0003` 在独立数据库中可反复加载，已有 hold 不会丢失。上一轮的危险降级脚本问题已修复。

仍建议改善两点：在执行任何新迁移前先完成全目录版本/文件名预检，避免遇到后置重复版本时已经提交前面的迁移；为已应用迁移保存校验和，检测历史 SQL 被原地改写。目前只按 version 跳过，没有 drift 检测。

Docker 镜像构建成功，最终进程使用非 root 用户，数据库默认不暴露宿主机。`deploy/.env.example` 中 `SUBAI_OUTPUT_BOUND`、`SUBAI_PRICE_SYNC_INTERVAL_H` 和 `SUBAI_LOG_RETENTION_DAYS` 重复出现，建议清理以免运维人员误以为存在两组配置。

## 本轮验证结果

| 检查 | 结果 |
|---|---|
| `go build -o /tmp/subai-review6-server ./cmd/server` | 通过 |
| `npm --prefix web run build` | 通过 |
| `docker build -f deploy/Dockerfile -t subai-review6 .` | 通过 |
| `go vet ./cmd/... ./internal/... ./rules/...` | 通过 |
| `npm audit --omit=dev` | 通过，生产依赖 0 个已知漏洞 |
| 排除 billing 错误测试后的核心包 race 测试 | 通过 |
| `make lint` | 失败，integration 测试编译错误 |
| `go test -race ./internal/... ./rules/...` | 失败，billing 新增测试单位错误 |
| 完整 integration 包 | 无法编译 |
| 可编译的 46 个顶层 integration 测试 | 40 通过、6 失败 |
| 临时回归探针 | 复现规则编辑 404、OAuth 新建账号、DELETE 人工处置、负重试 panic、failed→completed、规则版本不变、未知固定费→0 |

## 已确认改善

- 迁移器不再执行 `.down.sql`，当前三份迁移可在现有 schema 上重复运行。
- 百分比预算查询已先完整读取 rows，再查询基础策略，原先 `conn busy` 已消失。
- 本地 `review/reject/unsupported` 动作已有显式处理，不再继续上游调用。
- usage 只从明确的 terminal event 接受，非终态 usage 不再导致结算。
- 人工 usage 已通过 `UsageFromTotal` 正确转换总输入和缓存输入；当前 HTTP 路由错误阻断了端到端成功测试。
- 账号激活会检查 hold，调度的直接账号和账号组查询也排除 hold。
- 调度 tier 已使用实际 route priority，同优先级账号轮转测试通过。
- 审核事件 `/review` 路径拆分问题已经修复。

## 验证边界

本轮没有真实 Codex/OAuth 凭证，也没有真实 Moderation Platform Key，因此真实上游 endpoint、SSE 字段、token 刷新、工具调用费用上界、官方审核延迟/限额和官方价格源仍属于 P0 blocked 项。构建和合成测试通过不能替代这些生产联调。

建议修复顺序：先处理 P1-01、P1-02、P1-03、P1-05，恢复关键运维和请求状态闭环；随后处理配置 panic、审核例外和严格费用；最后修复全部测试门禁，再开始真实 P0 联调和验收。
