# 第五轮独立审查（2026-09-12）

结论：仍有需要修复的问题，不能依据现有完成报告认定全部通过。本轮未修改业务代码；数据库验证使用独立 PostgreSQL 16 容器和合成数据，没有调用真实上游。当前目录没有 Git 元数据，审查基于工作区快照。

## 已确认的问题

### R5-01 [P1] 人工补账的缓存 token 语义与计费模型不一致

位置：internal/admin/audit_handlers.go:522、545；internal/billing/billing.go:31；internal/gateway/upstream.go:107。

管理入口要求 cached_input_tokens <= input_tokens，按总输入进行校验，却直接将 input_tokens 传给定义为 uncached 的 billing.Usage。正常上游路径会先减去缓存量，人工路径没有减。

实际通过管理 HTTP 接口补账：总输入 100、缓存 80、输出 0；价格为输入 $2/百万、缓存 $0.2/百万。应为 (20×2+80×0.2)/百万 = 0.000056，实际返回 cost=0.000216。如果接口本意是输入非缓存 token，那么 20 非缓存、80 缓存又会被新校验拒绝。

修复：明确 API 与页面中的总输入/非缓存输入契约，并统一归一化；补充真实 HTTP 调用及账本金额断言。当前负 token 测试仅复制校验表达式，不能验证此链路。

### R5-02 [P1] 管理状态更新可以绕过尚未解除的隔离原因

位置：internal/admin/resources.go:298-320；internal/scheduler/scheduler.go:171 起；internal/admin/audit_handlers.go:559-574。

实际创建 over_reserve hold，设置账号 recovery_hold，然后 PATCH 账号为 active，返回 200。保留 hold 的情况下调用 AcquireAccount，仍成功选择该账号。账号状态与 account_holds 出现矛盾，隔离不再约束请求。

建议统一按原因解除隔离的管理入口，在同一事务中协调账号状态、未决请求与 holds，并在选择账号时保证不可绕过隔离。resolveUnknown 当前检查未决请求、删除 hold、恢复状态使用独立 SQL，仍有并发窗口；本轮没有进行该窗口的并发故障注入。gateway/state.go:260、336 的恢复路径仍只更新状态，没有同步记录原因。

另外，migrations/0002_hold_reasons.sql:8 允许 admin_action，而 internal/accounts/holds.go 定义 admin_pause、reauth_required，后两者会违反数据库约束。相关帮助函数尚未接入完整业务链路，应一并统一。

### R5-03 [P1] 集成测试清理遗漏 account_holds，连续运行确定失败

位置：tests/integration/integration_test.go:155-163；migrations/0002_hold_reasons.sql:6。

在全新隔离库运行原有三个测试文件：28 个顶层测试中首个通过，其余 27 个失败，均阻塞于迁移，错误为 relation account_holds already exists。

原因：清理列表不含 account_holds；DROP accounts CASCADE 删除依赖约束，并不会删除引用表本身；同时 schema_migrations 被清除，下一次又执行 CREATE TABLE account_holds。此前错误执行 down 迁移可能掩盖了清理缺口。

修复测试隔离机制，优先在专用测试库重建 schema，或完整维护表清理列表；不要为掩盖测试残留而简单弱化生产迁移。此结果是测试基础设施失败，不能解读为其余 27 条业务断言失败。

### R5-04 [P1] 新数据库测试绕过测试连接配置且使用不存在的 schema

位置：tests/integration/round4_fixes_test.go:337-390。

testPool 硬编码 postgres://localhost/subai_test?sslmode=disable，忽略 Makefile 设置的 TEST_DATABASE_URL，也没有 requireDB 或迁移初始化。插入 accounts.name、keys 表、members.email，均不符合当前迁移。因此标准集成入口无法正确执行这些测试，且可能访问本机另一数据库。本轮没有运行这两个硬编码数据库测试。

此外 TestR4_04_PercentBudgetQueryNoBusy 只执行独立 SQL，没有调用 Reserve/applicablePolicies；TestR4_07_NegativeTokenValidation 只测试自身复制的布尔表达式。即使显示通过，也不能证明目标实现没有回归。

修复：复用真实 newTestEnv，调用实际业务入口并验证金额、状态和外部调用次数。

### R5-05 [P2] 审核事件复核 URL 仍返回 500

位置：internal/admin/routes.go:190-192。

POST /api/admin/audit/events/<UUID>/review 后，pathID 保留 /review 后缀并传给 UUID 参数。实际返回 invalid input syntax for type uuid，HTTP 500。本轮使用格式合法 UUID 即可复现解析错误，不依赖记录是否存在。前端使用的也是该子动作 URL。

修复：剥离动作后缀并校验 ID，增加真实前端 URL 的契约测试。

### R5-06 [P2] 相同优先级路由仍被拆成不同调度层

位置：internal/scheduler/scheduler.go:179-193、216 起。

代码以 for 循环下标作为 tier，而不是 Route.Priority。两个相同优先级的直接账号路由，连续六次获取并释放，实际选择计数为一个账号 6 次，另一个 0 次。第一条有空位就返回，跨路由同优先级的轮转与负载比较没有发生。

修复：按优先级值聚合候选账号，再进行同层选择；覆盖直接路由、账号组路由及重叠候选。

## 验证结果与修复进展

- make lint：通过。
- go test -race ./internal/... -count=1：通过，之前队列测试问题未再出现。
- go build -o /tmp/subai-review5-server ./cmd/server：通过。
- npm --prefix web run build：通过。
- go test ./tests/integration -run 'TestR4_0[567]' -count=1：通过，仅包含三个不连接数据库的测试。
- 原有三个集成测试文件：1 个顶层测试通过、27 个失败，原因见 R5-03；没有宣称完整集成通过。
- 临时隔离探针：记录了 R5-01、02、05、06 的实际 HTTP/调度结果。探针属于现状观察，PASS 仅代表观察执行完成，不代表业务行为正确；文件已移除。

代码检查确认：迁移加载器已过滤 .down.sql 并按数字排序；本地审核动作已有显式分支；usage 提取已限制 completed/failed 事件；人工负数校验已加入；百分比预算已改为读完结果集再查询基础策略。除上述已有测试覆盖外，本轮没有逐项完成历史升级、百分比预算和真实上游协议的动态验收，不应扩大表述为全部验证通过。

## 后续优化及验收边界

1. 优先修复补账与隔离一致性，再修测试环境，恢复完整回归能力。
2. 明确上游 failed/incomplete 终态及 usage 契约；当前仅接受 completed/failed，真实协议联调仍需验证。此项是待验收风险，不列为本轮已复现缺陷。
3. 将迁移验收拆为空库、已有版本、保留历史 hold 数据、重复启动，不用空库成功代替升级成功。
4. 修复报告应与代码和日志对应。docs/ROUND4_VERIFICATION_REPORT.md 引用不存在的 migrations/014_create_account_holds.sql、017/018/019 和 cmd/migrate/main.go，并宣称 7/7 修复，不能作为发布验收证据。

本轮未进行浏览器端到端、真实 OAuth/审核/上游 SSE 联调及完整并发故障注入，也未重新构建 Docker 镜像。仍需在上述问题修复后完成这些适用的验收。
