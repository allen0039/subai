# 第二轮独立审查 · 2026-09-12

结论：上一轮多项基础缺陷已修复，但不能认定全部修复完成。仍有重复调用、错误解除账号隔离、历史计费价格错误、恢复不可重入等问题；新增管理界面改动也存在回归。

本轮未修改业务实现。目录仍没有 .git，因此以当前源码和第一轮报告逐项核对，不能提供提交级 diff。临时复现测试执行后已移除。

## 验证

- make lint、Go 普通测试、前端 TypeScript/Vite 构建通过。
- 完整 Docker 镜像构建通过，迁移目录缺失已修复。
- 使用新建的一次性 PostgreSQL 16 数据库执行现有集成测试。当前是 21 项顶层测试（原有 13 + 新增 8），不是修复记录所写的 20 项。
- 首次完整运行：TestClientDisconnectStillSettles 失败，其余通过。随后单独重复该测试 3 次全部通过。该测试固定延迟 80ms 取消，未同步等待上游首帧；负载变化可使取消发生在审核或 DB 阶段，不能作为稳定的“流中断连”证据。
- 移除临时复现文件后再次运行完整 21 项集成测试：全部通过，约 8.1 秒；make lint 再次通过。测试容器已停止删除。
- 三项针对性复现均确认缺陷：已接收 POST 被再次发送（实际 2 次）；超预留人工调整后账号变为 active；固定预算表单请求返回 400 invalid body。
- 真实上游、真实 OAuth 和浏览器交互仍未验收。界面问题通过代码与对应 API 请求验证。

## P1：必须继续修复

### R2-01 出口 fallback 仍会重放已接收的 POST（已复现）

位置：internal/egress/egress.go:197-214；internal/gateway/handler.go:505 起。

sent 仅在收到完整 SSE 帧后设置。上游已经接收并执行 POST，但在返回响应头前断开时，client.Do 返回 url.Error，sent 仍为 false，fallback 再发一次。复现服务器读完第一份请求体后断开，实际收到 2 个 POST。

修复应区分“请求尚未发送”与“未收到响应”。后者必须视为结果不确定，不能自动重放。备用出口的错误也需要逐次分类，目前 fallback 循环遇到 HTTP 状态错误仍可继续下一个备用。

现有 TestFallbackNoReplayAfterFirstFrame 只覆盖收到首帧后的路径；其断流还可能表现为普通 EOF + usage_missing，没有覆盖本问题。

### R2-02 人工结算的超预留隔离会被接口立即撤销（已复现）

位置：internal/admin/audit_handlers.go:533-541；internal/billing/reserve.go:309 起。

AdjustUnknown 检测超预留后将账号设为 recovery_hold，但接口随后只检查是否还有 unknown 预留，没有则立即改为 active。复现：将输出用量设置为明显超出预留的数值，调整成功后账号实际 active，预期应继续 recovery_hold。

修复：事务返回明确的超预留结果和隔离原因；账号恢复不能仅由 unknown 数量决定，也不能覆盖其他隔离原因。检查和恢复应与账号状态协调，避免并发新 unknown 出现时误解封。

### R2-03 unknown 调整使用当前价格，而非请求冻结的价格

位置：internal/admin/audit_handlers.go:508-528。

resolveUnknown 调用 ActivePriceVersion，再按当前版本计算历史请求。若请求发生后价格更新，同一份历史 usage 会按新价格计费；如果该模型从当前价格表移除，旧请求甚至无法调整。

修复：读取 requests.price_version_id，并使用冻结版本和模型。缺失历史价格应进入明确的证据补全流程，不应悄悄改用现价。回归测试应在请求后切换价格，再验证调整成本和账本版本仍使用原版本。

### R2-04 启动恢复中途失败后，后续启动不会补齐 unknown

位置：internal/gateway/state.go:132-175。

恢复先把请求改成 unknown，然后分别更新预留和账号，三步不在同一事务。若第一步后崩溃或后续 SQL 失败，下次启动只扫描 dispatching/streaming/settling，忽略已经 unknown 的请求。结果可能是 requests=unknown、reservations=held、accounts=active，且永久不再修复。

修复：将请求、预留和账号的收敛放在事务中，并扫描已有 unknown 的一致性。还应处理上一版留下的 cancelled_before_dispatch + held 历史记录；当前恢复也不会释放这些记录。

### R2-05 上游执行与收尾共用同一截止时间，超时后仍无法收尾

位置：internal/gateway/handler.go:398-401,432,453-470,489-497。

finishCtx 虽然脱离客户端取消，但同时用于整个上游调用与最终数据库操作。上游运行达到 5 分钟时，该 context 已过期，随后的 MarkUnknown、RetainUnknown 和状态更新都会使用失效 context；并发槽却释放。与第一轮断连问题相同的未收敛状态仍可由超时触发。

此外，Settle 返回错误的分支没有调用 RetainUnknown，数据库恢复后账号可能继续接单。

修复：分离上游执行 context 和在执行结束时新建的收尾 context，统一失败收敛函数；派发前预留成功但状态更新失败时也要释放或进入可恢复路径。用短超时注入验证，不需要等待真实 5 分钟。

### R2-06 Compose 没有传入新生产开关，标准部署无法启用数据面

位置：deploy/docker-compose.yml:26 起；internal/config/config.go:64；deploy/.env.example。

ProductionReady 默认 false，但 Compose environment 未传 SUBAI_PRODUCTION_READY，也没有 env_file。把变量放进 deploy/.env 不会自动注入容器，因此按现有 Compose 流程启动，运营开关始终关闭，数据面返回 503。OAuth 的 CLIENT_ID、REDIRECT_URI 等虽写在示例 .env，同样没有传入容器。

修复：显式传入实际支持的配置、同步示例与操作手册；对 compose 渲染结果和容器内配置做验收，不能只以镜像构建成功代表可部署运行。

## P2：功能回归与未完成项

### R2-07 创建表单把可选数值初始化为空字符串（已复现）

位置：web/src/components.tsx:83-88；internal/admin/audit_handlers.go:64-69。

openCreate 对所有未设 default 的字段赋值空字符串。创建 fixed 预算时，不相关的 percent_bps 也被提交为字符串；后端类型是 *int，JSON 解码立即失败。使用与表单一致的请求复现，返回 400 invalid body。

修复：按字段类型初始化，仅提交适用字段；可选数字使用省略或 null，不使用空字符串。增加真正通过页面/表单数据提交 fixed 与 percent 策略的测试。

### R2-08 分页控件与多个后端列表不匹配

位置：web/src/components.tsx:65-75；internal/admin/resources.go:15,153,211,331；internal/admin/audit_handlers.go:20,141。

前端统一发送 offset 并截取前 20 条，但代理、出口策略、账号、组、预算策略等列表没有使用 offset。记录超过 20 条时，“下一页”重复第一页内容，却展示新的条目范围。预算周期固定 LIMIT 200 也没有 offset。

修复：所有使用 ResourcePage 的接口统一分页契约，或明确由前端进行本地分页。用 25 条记录验证两页 ID 不重复、没有遗漏。

### R2-09 总 body 内存准入仍发生在分配之后

位置：internal/gateway/handler.go:223-234。

先 io.ReadAll 读完整 body，再 TryReserve。多个慢速上传或并发上传的 body 在读取期间完全不计入 gate，因此仍可绕过总内存上限。已准入请求还有提取文本等额外副本，该 gate 只能称为原始 body 计量，不能代表进程总内存限制。

修复：读取前预留安全额度，或分块读取并逐块申请；超限及时停止读取并释放已分配额度。测试检查读取期间 Used，而不仅是读完后的 429。

### R2-10 队列 waiting 包含执行中的请求，三类界限尚未分离

位置：internal/audit/queue.go:58-105。

成功获得执行槽后没有减少 waiting，而是到 release 才减。因此 globalWaiting 实际限制“等待 + 执行”，与配置和 Waiting 指标含义不符。例如 waitMax=1、maxInFlight=8 时，第一个执行中请求会阻止第二个进入，即使还有七个执行槽。

修复：入执行槽时转移计数，释放时只释放执行额度；明确 perKey 约束的是等待还是总在途。release 重复调用还会继续读取 channel，应使用 sync.Once 保证整个释放操作幂等。

## 已确认改善与证据边界

- Docker migrations 复制与 npm ci 已落地，并通过完整镜像构建。
- 数据面与管理面已共享 Readiness，默认拒绝 synthetic；但 Compose 配置闭环仍缺失。
- CRLF、多行 data、帧大小限制已有实现和通过的单元测试。
- 缺失 flagged 的审核响应现在拒绝，相关集成测试通过。
- OAuth 回调挂载到真实主路由，合成回调测试通过；刷新已接入，但仍需刷新到期、并发刷新与出口路径测试。
- 审核后 Fresh Key 复查及模型重验、调度跳过满载账号已落地。
- 未发送请求资金释放和正常恢复路径测试通过，但不覆盖恢复流程自身崩溃的窗口。

修复记录存在两处证据不符：TestMain 在无数据库变量时仍 os.Exit(0)，并没有改为逐测试 t.Skip；members_keys.go 的写操作没有调用 Reloader，因此“所有管理写入立即失效缓存”也不能按文档验收。Fresh Key 复查已经改善实际派发权限，应与缓存即时失效分别描述。

单活监控已加入，但数据库查询未设置超时；锁连接网络黑洞时不能保证 2 秒内停服。建议给监控探测明确截止时间，并测试连接被终止与仅该连接网络失联两种情况。

## 下一轮验收顺序

1. 先修 R2-01 至 R2-06，验证资金、重复执行和部署可用性。
2. 修表单与分页，跑实际管理员操作闭环。
3. 用同步事件代替固定 sleep 编写断连测试；补首响应前断开、恢复中断、超预留调整、历史价格切换和执行超时的回归测试。
4. 再验证资源准入、单活监控与真实上游 P0 项。当前仍不建议作为生产验收通过版本。
