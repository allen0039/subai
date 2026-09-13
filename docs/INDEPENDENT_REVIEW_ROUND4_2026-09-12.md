# 第四轮全面审查 · 2026-09-12

## 结论

本版不能作为验收通过版本。前三轮部分修复已落地，但新增迁移和测试不符合项目现有结构；扩大检查范围后，还复现了百分比预算、审核动作、用量终态、人工调整、路由轮转和审核复核的问题。

**优先阻止带有现有隔离数据的数据库直接执行当前升级迁移。** 当前加载器会执行降级脚本，存在删除 account_holds 及其数据的路径。此问题在隔离数据库中复现，不代表已经对用户实际数据库造成影响。

本轮只审查，没有修改业务代码、依赖或迁移。临时复现文件已移除。目录没有 .git，不能做提交级差异审查。没有读取真实凭证或调用真实上游。

## 检查范围与结果

| 范围 | 方法与结果 |
|---|---|
| 构建与依赖 | make lint 和完整集成测试入口失败，首先提示 go.mod 需要更新；新增测试还有不存在的包、类型和表字段 |
| 单元与并发检查 | go test -race ./internal/... 失败：队列测试 5 秒后必然超时；gateway 包通过 |
| 旧集成测试 | 显式选取原有三个测试文件，排除新增不可编译文件，28 项顶层测试通过，约 10.4 秒。不能把这称为全部测试通过 |
| 新增针对性验证 | 7 个业务/迁移场景均确认缺陷，详见 R4-01、04 至 09 |
| 服务与容器 | go build ./cmd/server 对应二进制构建通过；完整 Docker 镜像构建通过 |
| 前端 | TypeScript/Vite 构建通过；管理功能通过与前端相同的 API 请求、路径及源码检查验证，未做浏览器视觉验收 |
| 迁移与恢复 | 核对迁移发现/排序/版本记录、隔离原因迁移、启动恢复和人工调整 |
| 数据面 | 核对鉴权、权限复查、内容提取、本地规则、审核调用、队列、预算预留、调度、出口、SSE、结算 |
| 管理与运维 | 核对资源接口、前后端路径、分页、会话、配置、Compose、备份恢复说明 |

测试数据库使用新建的一次性 PostgreSQL 16 容器，与实际数据库隔离；审查结束后已停止并自动删除。

## 必须修复的发现

### R4-01 [P0，阻止升级] 升级加载器执行 .down.sql，可能删除隔离表

位置：internal/storage/storage.go:80-117；migrations/000011_account_holds.down.sql；migrations/000011_account_holds.up.sql；migrations/0002_hold_reasons.sql。

加载器接受所有 .sql，按文件名字典序执行，只用数字 version 判断是否应用。新增的 000011_account_holds.down.sql 排在同版本 up.sql 前，其内容是 DROP TABLE IF EXISTS account_holds。执行后记录 version=11，up.sql 因版本已存在而跳过。数字长度不同还会导致 000011 排在 0001、0002 前，并非按数值顺序。

**复现条件**：数据库已经应用版本 1、2，account_holds 中存在 over_reserve 记录，尚未登记版本 11。运行当前 Migrate 后，查询 account_holds 报 relation does not exist。

空库测试可能掩盖这个问题：先执行 down（当时无表），随后 0002 又创建表，最终看似正常。必须同时验收空库与已有数据的升级。

修复：统一迁移格式；升级只选前进脚本，按数字排序并拒绝重复版本；合并两个不一致的 account_holds 定义。已有部署需要基于真实 schema_migrations 状态制定修正迁移，不能直接随意重排已应用版本。当前两份表定义的原因枚举也不一致：admin_action 与 admin_pause/reauth_required 并存。

### R4-02 [P1] 新增集成测试不属于当前项目的数据模型，完整检查不可用

位置：tests/integration/round3_fixes_test.go:8-10 及全文件；go.mod。

新增文件引用不存在的 subai/internal/db，使用不存在的 testNow、ModelPrice.InputPerMillion/OutputPerMillion。SQL 使用 accounts.name、oauth_providers、budget_policies.account_id/entity/window 等当前 schema 不存在的表或列；testify 也没有正确纳入模块依赖。

实际 make lint 与 go test ./tests/integration 首先报 go.mod 需要更新。仅执行 go mod tidy 无法修复不存在的内部包、类型和 schema。

修复：基于现有 newTestEnv、storage.DB、实际迁移及合成 OAuth Manager 编写测试。不要用直接更新虚构 oauth_providers 表代替 Refresh 实际调用；空 usage 的测试目前 t.Skip，也不能算覆盖。完整测试能编译运行后才可声明新增回归已验证。

### R4-03 [P2] 队列测试修复了竞争，却引入确定的相互等待

位置：internal/audit/queue_test.go:73-95。

两个执行槽已经被占满；主 goroutine 在释放槽之前等待 waiterReady；子 goroutine 必须先拿到执行槽才发送 waiterReady。最终等待者 5 秒超时并关闭 channel，主 goroutine 看到 Waiting=0，测试失败。

实际 -race 输出：audit queue wait timeout；waiting=0, want 1。此处是测试顺序错误，不能据此认定生产队列死锁。

修复：先同步确认第三个请求进入等待，再断言第四个被拒绝；释放原有槽后才等待获取第三个释放函数，最后调用释放。将“已排队”和“已取得执行槽”设计为不同信号。

### R4-04 [P1] 百分比预算查询复用忙碌事务连接

位置：internal/billing/billing.go:144-185，尤其 177 行。

applicablePolicies 在遍历 tx.Query 的 rows 时，遇到 percent 分支又用同一个 tx.QueryRow 查询基础策略。pgx 的事务使用同一连接，前一个结果集尚未读完，返回 conn busy。

**复现**：创建有效 fixed 基础预算和引用它的 50% key 预算，再调用 Reserve。实际报 percent base policy ...: conn busy。用户会看到预算调用被拒绝，而不是正常的百分比额度。

修复：先完整读取并关闭策略结果集，再查询基础策略；或用 JOIN/一次性读取完成。验收 fixed、percent、多个共同约束作用域，不能只测试“创建策略成功”。

### R4-05 [P1] 本地 review/reject/unsupported 动作没有对应执行语义

位置：internal/audit/pipeline.go:72-96；internal/audit/rules.go:validateRule、RunLocal。

规则 schema 接受 flag/review/block/reject/unsupported，但流水线仅特殊处理 secret 的 block/review 与其他 block。非 secret 的 review、reject、unsupported 命中后仍继续外部审核，官方 allow 即转发上游。

**复现**：安装 content 类 regex 规则，action=review，输入明确命中。实际仍调用上游 1 次。

修复：对所有可配置 action 做显式、穷尽的分支处理；未支持动作不允许写入配置。以“命中规则后审核服务/上游调用次数”断言验证，不能只检查 RunLocal 是否返回 hit。

### R4-06 [P1] 有效但非最终的 usage 被当成最终结算证据

位置：internal/gateway/upstream.go:68-108；internal/gateway/handler.go:446 及结算分支。

新的字段存在性和非负校验修复了空 usage，但 ExtractUsageFromEvent 仍不检查事件类型或终态。handler 将任意事件中的合法 usage 标记为 usageKnown；流随后普通 EOF，就会结算 completed。

**复现**：只发 response.created，附 input_tokens=0、output_tokens=0，然后关闭流，没有 completed 事件。实际 requests=completed，预期应为 unknown。收到终态前的临时统计还可能在后续终态 usage 无效时被沿用。

修复：为支持的上游协议定义可信终态和最终 usage 的关联；仅在有效终态证据齐全时结算。覆盖 created+usage 后 EOF、临时 usage 后异常终态、正常完成、failed/incomplete 等协议支持的终态，不要把“字段有效”替代“统计最终确定”。

### R4-07 [P1] 人工调整仍接受负 token，按零费用完成

位置：internal/admin/audit_handlers.go:506-545；internal/billing/reserve.go:257 起；internal/billing/billing.go:Cost；migrations/0001_init.sql 的 usage_ledger。

上游解析增加了校验，但人工入口和 billing 层没有共用校验；token 列没有非负约束。

**复现**：对合法 unknown 请求提交 input_tokens=-1000000，其他为 0。接口返回 ok=true、cost=0，并完成调整。负费用被 Cost 截为零不能代替拒绝异常用量，会导致释放资金和错误账本。

修复：在结算服务层统一检查 usage 范围；管理入口将缺字段与显式零区分，拒绝负数并返回 400。数据库加入合理的非负约束作为最后保障。应说明人工输入的 input_tokens 是总输入还是不含缓存输入，避免重复计费。

### R4-08 [P2] 审核事件复核 URL 仍把动作后缀传给 UUID

位置：internal/admin/routes.go:190-192；internal/admin/audit_handlers.go:458 起；web/src/App.tsx 的复核按钮。

Key 的 /routes 已修复，但审核事件 /review 存在同类问题：pathID 返回 <UUID>/review，未剥离动作就传入数据库。

**复现**：管理员 POST /api/admin/audit/events/<有效 UUID>/review，outcome=false_positive。返回 500，数据库报 invalid input syntax for type uuid: <UUID>/review。

修复：统一资源 ID 与子动作的路径解析，补所有真实 URL 的契约测试，包括 review、revoke、resolve-unknown、groups/accounts 等，而非按单一已发现路径修补。

### R4-09 [P2] 相同优先级的独立路由不会同层调度

位置：internal/scheduler/scheduler.go:179-193、216-251。

for tier, r := range routes 使用切片下标作为 tier；每条路由成为独立层，即使它们的 priority 相同。第一条有空位时就返回，无法在相同优先级路由之间比较负载或轮转。

**复现**：给两个同状态、同优先级账号创建同 priority 的直接路由，依次获取并释放六次，实际只选到一个账号。

修复：以 Route.Priority 的值划分层，在同层汇总账号后选取。组内 ties 也应同时比较 inflight 和账号 Priority，并固定一次轮转起点再遍历，避免在尝试每个候选时重复改变轮转偏移。

## 隔离原因系统尚未完成的整合（代码确认）

新增 account_holds 确实改善了正常超预留与后续普通调整的区分；OAuth 成功刷新也不再写 active。但仍存在多套不一致的写路径：

- gateway/state.go 的启动恢复和 RetainUnknown 只写 accounts.state，不写 account_holds。
- patchAccount 允许直接设 active，不校验或清除 hold；调度和 loadCredentials 只检查 state，不检查 holds。因此状态和隔离表可能相互矛盾。
- 未见可供管理员按原因解除 hold 的管理接口；holds.go 的 AddHold/RemoveHold 未被现有业务调用，页面和运维手册仍以直接修改 state 为主要恢复方式。
- resolveUnknown 的“查无 unknown→删 hold→恢复账号”分散在多条独立 SQL；它与新的 MarkUnknownWithAccount 仍未通过共同的账号锁或事务协调。
- 0002 将历史 recovery_hold 全部迁为 unknown_pending，可能丢掉旧版超预留的隔离含义；不能据此自动解封历史账号。

建议：统一隔离原因的事务写入与可调度性判定，规定管理员主动解除的证据、权限及日志；迁移历史状态时保守保留无法确定的原因。该部分未做完整并发故障注入，不能宣称所有竞争窗口已验证。

## 其他优化与契约缺口

1. **严格计费**：fixedFeeTotal 遇到未映射费用维度返回零而不是错误，和“未知费用应拒绝”的注释不符。真实工具费用接入前应改成显式不可计价，并验证预留与实际结算的一致性。
2. **界面完整性**：Key 路由列表仍不支持 offset，而 ResourcePage 统一使用分页；超过 20 条仍有重复页风险。会话过期只清 localStorage，未更新全局认证状态，部分页面可持续停留在错误或加载状态。应补实际浏览器的完整管理员操作流程。
3. **出口配置即时生效**：管理变更重载 Key/路由/规则，但 Egress Resolver 缓存 30 秒没有对应失效。应明确停用出口的生效时效并测试；HTTP Transport 每次新建也浪费连接复用机会。
4. **运行恢复**：日志保留清理、请求历史增长控制和优雅停机仍需补齐；故障对账必须考虑失败时数据库短暂不可用，而不仅是启动后补偿。
5. **配置校验**：输出上限、重试次数、超时、内存和队列额度应在启动时验证有效范围，避免负值或零值导致不可解释的拒绝或运行时错误。

## 修复文档不能作为验收证据

ROUND3_COMPLETION_REPORT.md、ROUND3_FIXES_SUMMARY.md 引用了不存在的 internal/upstream/anthropic.go、internal/admin/server.go、oauth_providers 等路径或对象；还将第三轮“Key 路由 UUID 后缀问题”写成不同的权限问题，将队列测试竞争写成 testNow 问题。报告的“所有问题已修复”和“总体风险低”与本轮结果不符。

应按真实代码与执行日志重写完成报告，并将“实现完成”“静态检查通过”“测试通过”“真实联调通过”分别标注。不能保留占位式验证代码作为已执行证据。

## 修复次序和验收门槛

1. 先修迁移发现规则与重复版本，验证空库、版本 1 库、已有版本 2 和 hold 数据的升级、重复启动；所有旧 hold 必须保留。
2. 修复新增测试与队列测试，使 make lint、普通单元、-race、完整集成测试恢复可执行。
3. 修 percent 查询、规则动作和最终 usage/人工用量校验，保留本轮 7 个失败场景为回归测试。
4. 完成隔离原因整合、真实管理 URL 和调度层级，再做浏览器完整流程及故障注入。
5. 最后执行项目 P0 的真实 OAuth、审核、上游 SSE/usage 和价格验证。在此之前不建议进入生产验收。
