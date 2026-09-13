# 初版独立审查 · 2026-09-12

结论：已具备可运行的模拟业务链路，但尚不适合按生产可用版本验收。主要问题集中在部署、计费恢复、流式故障处理、生产准入与管理流程闭环。

本次检查源码、迁移、部署文件、前后端接口和交接文档；未修改业务实现。当前目录没有 .git，不能检查提交差异。本文所有判断来自本地实现，不把尚未联调的真实上游行为视为已验证。

## 验证结果

- `make lint`：通过（临时复现文件移除后）。
- `go test ./... -count=1`、`go vet ./...`：通过；注意没有 TEST_DATABASE_URL 时集成测试会直接退出成功，不能据此认定集成测试执行过。
- 单独创建一次性 PostgreSQL 16 容器，在隔离数据库执行 13 项现有集成测试：全部通过，约 4 秒。上游和审核仍为合成服务。容器已停止并自动删除。
- `web` 下 `npm run build`：TypeScript 检查和 Vite 构建通过。
- 临时针对性测试：CRLF SSE 解析失败（期望 1 帧，实际 0 帧）；启动恢复未释放预留（期望 released，实际 held）。复现测试已移除，没有把预期失败的测试留在业务目录。
- 没有运行真实 OAuth、真实上游、生产部署或浏览器交互验收。Dockerfile 问题为静态确定缺陷，未完整构建镜像。

## 上线前必须修复（P1）

### 1. Docker 构建缺少 migrations

位置：`deploy/Dockerfile:7-10,23`。

build 阶段只复制 cmd、internal、rules，最终阶段却从 build 复制 `/src/migrations`。该路径未被创建，镜像构建会在 COPY 处失败。

修复：复制 migrations 到 build 阶段，或最终阶段直接从构建上下文复制。验收应覆盖完整镜像构建和空库启动迁移。

### 2. 生产准入与展示使用不同判断，测试价格可进入数据面

位置：`cmd/server/main.go:161-180`、`internal/audit/seed.go:96`、`internal/billing/billing.go:83`。

数据面 Ready 只检查审核 API Key 是否为空，后台 Ready 则始终 false。种子把 synthetic 价格激活，而取价只过滤 active。于是只要配置审核 Key，并配置好账号、路由和预算，数据面就可能使用测试价格调真实上游，后台却仍显示未就绪。文档承诺的已验证价格准入没有落地。

修复：统一 readiness 判断及明确的测试模式；生产模式拒绝 synthetic 价格，返回实际缺失条件。用零上游调用断言覆盖未验证价格路径。

### 3. 客户端断连后无法可靠结算或进入 unknown

位置：`internal/gateway/handler.go:367-398`。

结算、MarkUnknown、RetainUnknown 和状态更新均继续使用请求 ctx。客户端取消会取消该 ctx，数据库操作随之失败，多数错误被忽略；并发槽却由 defer 释放。可能出现预留仍占用、状态未收敛、账号继续接单的组合。

修复：用独立且有超时的收尾 context，显式处理持久化失败；覆盖审核中取消、预留后取消、流中取消和收到 usage 后取消。

### 4. 启动恢复只更新状态，没有完成资金和账号恢复

位置：`internal/gateway/state.go:93-126`、`cmd/server/main.go:108`。

预留完成但尚未 dispatch 的请求被改为 cancelled_before_dispatch，却没有释放 reservations 或扣减 budget_periods.reserved。针对性测试实际得到 reservation=held。已发送请求被改为 unknown，但关联账号没有进入 recovery_hold，也没有恢复内存并发占用。

此外，Settle 提交和 requests=completed 是两个操作；若二者之间崩溃，恢复会把已经记账的 settling 请求改为 unknown，而没有先核对账本。

修复：让恢复可重入，并按账本、预留、请求状态共同对账；未发送释放、已确认结算完成、未确认发送保留占用并隔离账号。测试必须检查余额和账号状态，不能只数请求状态。

### 5. 出口 fallback 可能重复执行已发送请求

位置：`internal/gateway/handler.go:404-416`、`internal/egress/egress.go:194-235`。

fallback 包裹整个 Dispatch + scanSSE。上游已经开始返回事件后，读流超时仍可被识别为 net.Error，触发重新 POST 同一 payload。两次生成共用一次预留、一个 attempt=0 结算，并可能向客户端拼接两条流。备用列表中的后续错误也没有逐次重做可重试判断。

修复：只对可以证明尚未发送的失败切换出口；发送结果不明必须进入 unknown，不能自动重放。若将来支持重试，需要明确上游幂等语义和逐 attempt 的资金记录。用“首帧后超时”验证上游只收到一次请求。

### 6. OAuth 回调未注册到外层路由，刷新实现未接入

位置：`internal/admin/routes.go:14`、`cmd/server/main.go:203`、`internal/gateway/handler.go:488`、`internal/accounts/oauth.go:211`。

子 mux 注册 `/api/oauth/callback`，外层却仅把 `/api/admin/` 交给该 mux，回调因此无法到达 handler。另有 Refresh 方法，但业务代码没有调用；取凭证不检查 ExpiresAt，过期 token 会继续发往上游。

修复：明确挂载回调；接入到期前刷新与并发刷新协调。测试应启动实际主路由，而不是仅验证 OAuth Manager。

### 7. SSE 解析会丢失 CRLF 流，并缺少帧大小上限

位置：`internal/gateway/handler.go:435-485`、`internal/gateway/upstream.go:63`。

分隔函数没有识别 `\r\n\r\n`。针对性测试返回 nil error，但实际处理帧数为 0，usage 也会丢失。多行 data 被直接拼接而非保留换行；一直不出现分隔符时 block 无界增长。

修复：完整处理换行、多行 data、分片边界和终止事件，增加单帧大小限制。对异常结束显式报告，避免仅以 EOF 视为成功读完。

### 8. 审核返回缺少必要字段也可能放行

位置：`internal/audit/moderation.go:144-173`。

Flagged 使用 bool 零值，当前只验证 results 非空。HTTP 200 且 body 为 `{"results":[{}]}` 就会产生 flagged=false 并 allow，违背异常响应应拒绝的约定。

修复：区分字段缺失与 false，校验审核响应结构；加入缺 flagged、错误类型和不符合预期结果数量的合成响应测试。不要把“合法 JSON”当成“有效审核决定”。

## 功能和运行可靠性问题（P2）

### 9. 人工 unknown 处置没有管理入口，函数本身也未形成闭环

位置：`internal/billing/reserve.go:231`、`internal/admin/routes.go`。

AdjustUnknown 未被管理端调用。其实现完成调整后把请求设成 settling，而不是终态，未恢复账号，未对超预留处理复用正常结算规则。后台提示人工核账，但没有可用的完整操作流程。

建议：增加带证据、权限、幂等保护的处置接口和页面；事务内收敛到明确终态，仅在账号所有 unknown 都解决后恢复账号。

### 10. 审核后的权限复查仍可能使用旧配置

位置：`internal/auth/auth.go:43-59`、`internal/gateway/handler.go:276-316`。

二次 LookupKey 可命中同一份五秒缓存；即使读到新配置，也只比对 ID，模型允许列表和并发限制仍使用第一次读取的 info。审核期间收紧模型权限或降低并发，当前请求不会按新限制重验。

建议：派发前读取关键配置的新快照并完整重验，管理写入后失效对应缓存；按所承诺的撤销时效写并发测试。

### 11. 调度忽略路由优先级，选中满载账号后也不会寻找其他可用账号

位置：`internal/scheduler/scheduler.go:165-209`、`internal/gateway/handler.go:316`。

Route.Priority 只决定数据库查询顺序，实际打分使用账号 Priority 和绝对 inflight，未保留路由层优先级。Pick 不排除已到并发上限的账号，后续 Acquire 失败即拒绝，即使另一账号仍有容量。Pick 和 Acquire 分离也存在竞争窗口。测试注入的 Slots 与调度读取的 DefaultSlots 不是同一实例。

建议：按路由优先级分层，在层内选择有容量账号，并将选择和占槽协调起来；依赖注入同一 Slots。覆盖低上限满载账号与高上限可用账号同时存在的情况。

### 12. 全局审核等待量和内存上限没有真正落实

位置：`internal/audit/queue.go:44`、`internal/config/config.go:25,67`、`internal/gateway/handler.go:150`。

global channel 限制的是执行中的数量；满后其他 Key 仍可各自加入等待，没有独立全局等待计数上限。请求体在排队前已完整读入并提取，AuditBodyMemoryLimit 只定义和加载，没有使用。多 Key 的大请求可显著超出配置的总内存预算。

建议：分别限制等待数、审核执行数和在途 body 字节；早期拒绝过载，排队总期限包含已耗时。用小容量参数测试，避免以真实大内存压测复现。

### 13. 管理界面缺少分页，若干流程仍不完整

位置：`web/src/components.tsx:66-72`、`web/src/App.tsx`、`internal/admin/routes.go`。

ResourcePage 总是请求默认第一页，没有翻页控件；使用分页的资源超过 20 条后，较旧记录无法在页面找到。一些其他列表完全不分页。预算、出口策略、账号组等未提供完整修改/删除流程，不能按“全部资源 CRUD”验收。

通用创建表单还只在显示时采用 default，提交的 createBody 不自动包含这些默认值；后端若采用不同默认值，界面所见会与保存结果不同。审核 reviews 返回 JSON 字符串，前端 stringify 再 parse 得到的仍是字符串，显示的是字符数而非复核条数。

建议：补分页与搜索，统一前后端 schema、初始化真实表单默认值；按“创建成员→Key→账号→组→路由→预算→调用→复核/调整”的操作闭环验收。

### 14. 单活锁连接失效后，旧进程没有停服机制

位置：`cmd/server/main.go:58-73`。

advisory lock 绑定独立连接，启动后没有持续检测该连接。该连接被断开时锁会释放，旧进程仍可能通过池里的其他连接服务，新进程可获得锁。此时内存并发、队列等单实例假设失效。

建议：锁连接丢失后立即取消数据面并停止服务；补连接被终止的故障注入测试。后续再按实际需求决定是否迁移到可协调的多实例设计。

## 优化顺序

1. **先恢复正确性**：Docker、统一生产准入、断连结算、启动对账、禁止不确定重放、SSE、OAuth、审核异常响应。
2. **再完成管理闭环**：unknown 调整、权限即时生效、路由/预算/账号组编辑、分页，统一状态和错误展示。
3. **然后优化稳定性和性能**：总 body 内存准入、真实全局等待限制、按出口配置版本复用 Transport、缓存容量和过期清理、优雅停机与锁失效停服。
4. **最后完成真实联调验收**：按项目已有 P0 清单验证上游 schema、usage、输出上界、审核容量和价格来源，再更新兼容矩阵。

其他建议：鉴权缓存会保存每个随机无效 Key 且无容量清理；StickyStore 只在同一键再次 Get 时清除过期项，应统一做容量/TTL 淘汰。实现实际日志保留任务；补请求终态、预留金额、unknown 年龄、队列等待、结算失败与出口错误的指标。前端和 Docker 构建改用 npm ci，并排除 node_modules、dist、bin 等构建上下文无关文件。

## 回归测试重点

现有 13 项测试可保留，但必须增加实际 HTTP 路由、CRLF/流中断、取消后的收尾、恢复资金、已记账未完成的崩溃窗口、fallback 零重发、异常审核字段、权限变更、多账号容量和完整镜像启动。集成测试在未配置数据库时应让 CI 明确显示跳过或失败，避免绿色结果被误认为已经验收。

交接文档中的“公平队列”“单活保证”“全部 CRUD”“生产价格准入”等描述需要随修复同步校正。目前这些声明有部分超过代码实际实现。
