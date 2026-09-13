# 第三轮独立审查 · 2026-09-12

## 结论

第二轮的大部分直接缺陷已有修复，现有回归测试通过。但仍有 4 项经针对性测试确认的问题，其中 3 项影响隔离或计费正确性，1 项阻塞管理路由创建。当前不应认定全部验收通过。

本轮没有修改业务实现。目录仍无 .git，检查以当前源码、两轮审查报告和修复记录为依据。临时复现测试已经移除，未留预期失败测试在业务目录。

## 测试证据

- make lint、go test ./...、前端 TypeScript/Vite 构建通过。
- 完整 Docker 镜像构建通过；测试数据库容器已停止并自动删除。
- `go test -race ./internal/... -count=1` 未通过：TestQueueWaitBoundRejects 自身有数据竞争，详见 R3-05；不能把普通测试通过等同于 race 检查通过。
- 独立创建一次性 PostgreSQL 16，在隔离库运行全部现有集成测试：28 项顶层、含子测试共 31 条 PASS，约 9.1 秒。修复记录写的 29 项顶层与实际数量不符。
- 以下四项补充测试均复现缺陷，具体结果见各条。
- 本轮未调用真实 OAuth、真实审核或真实上游；管理页面的问题通过与页面相同的管理 API 路由复现，未作浏览器视觉验收。

## R3-01 [P1] 后续普通调整会解除先前超预留的隔离

位置：internal/admin/audit_handlers.go:549-555；internal/billing/reserve.go 的 AdjustUnknown。

**复现步骤**：同一账号下创建两个 unknown 请求 A、B。A 的实际用量超预留，先调整 A，账号正确进入 recovery_hold；随后调整未超预留的 B。B 是最后一个 unknown，接口将账号改为 active。

**实际测试结果**：`state=active after later normal adjustment; want recovery_hold`。

根因：本次新增的 `!result.OverReserve` 仅反映当前请求 B；数据库没有独立保留 A 尚未解除的超预留隔离原因。检查“没有 unknown”不能证明账号所有隔离原因都已解除。检查和恢复分成多次数据库操作，还保留了并发新 unknown 与恢复交错的窗口。

建议：持久化可区分的隔离原因（例如 unknown、over_reserve、管理员暂停），按原因消除；只有所有阻塞原因清空才恢复可调度性。检查和恢复在账号锁或一致的事务内完成。新增 A 超额、B 正常且 B 最后处置的测试。

与第二轮关系：R2-02 的单请求场景已修复，多请求场景仍未完成。

## R3-02 [P1] token 刷新会覆盖刷新期间设置的隔离

位置：internal/accounts/oauth.go:259-262。

**复现步骤**：开始 Refresh，让合成 token 服务器暂不返回；期间将账号设为 recovery_hold；放行 token 服务器返回成功结果。Refresh 最后执行 `state='active'`，覆盖隔离状态。

**实际测试结果**：`state=active; refresh overwrote hold`。

根因：更新条件只校验 credential_version。隔离或管理员暂停不会改变 credential_version，因此凭证的乐观锁保护不了账号可用状态。当前 gateway 已接入 Refresh，这条路径现在会在真实请求过程中触发。

建议：凭证刷新只更新凭证，不无条件改账号可调度状态；如需解除 reauth_required，应仅解除明确对应的原因，并保留刷新过程中新增的隔离。失败分支无条件 SetState(reauth_required) 也应遵循同一规则。测试应覆盖刷新成功/失败与管理员暂停、unknown 隔离交错。

## R3-03 [P1] 空 usage 会被当成已知零费用结算

位置：internal/gateway/upstream.go:63-84；internal/gateway/handler.go:448 起的 usage 收集和结算。

**复现输入**：合成上游发送 `response.completed`，内容为 `{"response":{"usage":{}}}`，随后结束连接。

**实际测试结果**：请求变为 completed，预期应为 unknown。空对象里的 input_tokens/output_tokens 使用 int64 零值，ExtractUsageFromEvent 返回 ok=true；因此以零 token 进入正常结算。

该函数还没有验证 token 非负、cached_tokens 不大于 input_tokens，也不区分事件是否提供最终 usage。数据库 token 列没有非负 CHECK，人工调整入口同样直接接受 int64。异常用量可能被写入账本，Cost 对负结果归零也不能代替有效性校验。

建议：区分字段缺失与明确的零；按支持的上游事件协议验证最终 usage 的完整性和范围，不满足条件则保留资金并进入 unknown。统一正常结算与人工调整的 usage 校验，补空对象、缺必需字段、负数、cached 大于总输入的测试。

与前轮关系：审核响应的缺 flagged 问题已修复，但计费响应仍有相同的“缺字段等同零值”缺陷。

## R3-04 [P2] 管理端无法通过正常 URL 创建 Key 路由

位置：internal/admin/routes.go:56-64,220-225；internal/admin/resources.go 的 keyRoutes。

**复现请求**：管理员会话下 POST `/api/admin/keys/<有效 Key UUID>/routes`，body 提交有效 target_type 和 target_id。

**实际测试结果**：409，数据库报 `invalid input syntax for type uuid: "<UUID>/routes"`。

根因：pathID 明确保留动作后缀；routes 分支直接把该返回值传给 keyRoutes，没有去掉 `/routes`，最终把拼接路径作为 api_key_id 使用。revoke 分支已经去后缀，但 routes 分支遗漏。

建议：使用显式路径参数或严格拆分动作和资源 ID；覆盖该路由的 POST/GET/DELETE，而不是只调用内部方法。GET 的遍历结束还应检查 rows.Err，避免同一数据库错误被显示成空列表。修复后从空配置走完“成员→Key→路由→预算→调用”的管理闭环。

## R3-05 [P2] 新增队列测试存在数据竞争

位置：internal/audit/queue_test.go:82,95-98。

race detector 报告 waiterRel 在子 goroutine 写入时，主 goroutine 已经读取它；`<-waiterReady` 放在读取之后，未建立正确的同步顺序。还可能在 waiterRel 尚未赋值时跳过释放。当前证据指向测试实现，不是生产 Queue 数据竞争。

建议：先等待 waiterReady，再读取并调用 waiterRel；也可以用 channel 直接传递释放函数。用显式同步取代固定 50ms sleep，并重新执行 -race。

## 上轮修复复核

| 第二轮项目 | 本轮结论 |
|---|---|
| 响应前断开导致重放 | 错误分类及逐备用出口判断已修复，对应集成测试通过 |
| 单次超预留调整立即解封 | 单次场景已修复，多请求仍有 R3-01 |
| 历史请求按现价调整 | 已使用请求冻结价格，对应测试通过 |
| 恢复不原子、遗留半状态 | 每请求事务与遗留状态清扫已落地，对应测试通过 |
| 上游耗尽收尾时间 | 执行与收尾 context 已分离，对应超时测试通过 |
| Compose 环境变量缺失 | 已增加 env_file 并更新示例，配置缺口已补 |
| 固定预算字段类型错误 | 创建 payload 已按类型转换/过滤，对应 API 测试通过 |
| 分页、队列计数 | 后端分页及执行时转移等待计数已实现，现有测试通过 |
| body 准入时点 | 已按块计量原始 body，额外副本不在计量内的限制已写明 |

## 建议的下一步

优先把账号隔离原因和凭证状态分开处理，一并修复 R3-01、R3-02；随后统一计费用量校验，修复 R3-03；补真实管理 URL 的路由测试修复 R3-04。新增测试应保留为长期回归，尤其是跨请求和刷新期间的状态交错。完成这些后再执行真实上游 P0 验收。
