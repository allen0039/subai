# SubAI 独立网关与安全审计实施计划

版本：v0.2 · 日期：2026-09-12 · 状态：供开发工具执行的实施前规格

本计划以安全审计模块为重点，并定义它与账号、额度、代理及并发管理的衔接。项目独立开发，可参考开源实现，不以改变客户端特征或避免上游识别为验收目标。

## 1. 目标与范围

在 VPS 上提供可扩展的个人／受控成员 AI 网关，供数量可增长的电脑、CLI、Hermes 实例及其他兼容客户端使用。通过独立 API Key 管理各使用主体，经受控网关调用多个 Pro 账号的 Codex 能力。每个 Key 可绑定账号或账号组、设置内部美元预算与并发上限。请求先通过本地规则与官方审核，再进入上游。

三台电脑和两个 Hermes 只是初期使用场景与首批试运行样本，不是数量上限。账号、成员、客户端和 Key 必须通过数据库及管理界面动态增删，不得硬编码为五个。可扩展是指数据模型和配置不限定数量，不承诺单台 VPS 无限容量；实际接入规模受算力、连接数、审核吞吐和上游可用量约束。

| 已确认需求 | 第一版实现目标 |
|---|---|
| 独立项目 | Go 服务、PostgreSQL、Web 管理界面、Docker Compose 单实例部署 |
| 多账号授权 | 使用上游支持的授权流程，保存并刷新凭证 |
| 可增长的使用主体 | 管理员可创建成员、设备／Agent与多个Key，可撤销、暂停和查询用量 |
| 预算 | 固定美元预算，按日/周限制；支持账号预算与账号组预算的百分比分配 |
| 价格 | 默认以官方 API 标价内部记账，自动检查更新，保留版本 |
| 并发 | Key 与上游账号双层限制 |
| 出口 | 每账号 HTTP/SOCKS5 或 VPS 直连，故障默认停用，允许显式配置备用出口 |
| 审计 | 可编辑本地规则包、官方 Moderation 前置检查、队列、脱敏日志与误判复核 |

第一版不做公开注册、充值、支付、商业分销、多机集群或本地审核模型部署。Hermes 本地工具权限作为独立集成项，不声称网关能控制所有本地动作。

## 2. 必须保留的边界

1. Pro 订阅额度不等于美元 API 余额。本项目的美元是按价格表折算的内部预算；百分比基于管理员设置的预算，不是官方周额度的精确切片。
2. 官方 Codex 桌面端直接登录的请求不受网关预算、审计和并发控制，但会消耗同一账号的真实上游额度。
3. OAuth 独立实现不产生新额度，不保证减少识别，不改变上游支持的客户端与授权规则。
4. Moderation 是内容分类服务，不是所有违法行为、网络攻击、提示词注入、数据外传和危险工具操作的通用判定器。
5. 全文分段审核仍可能遗漏跨段意图。任何模式都不承诺零误判、零漏判或账号永不受限。
6. 严格美元上限依赖可验证的请求费用上界。若上游不能可靠限制输出或某项用量不可界定，该调用形式不进入严格预算支持清单。

## 3. 技术结构与处理顺序

建议采用单体模块化服务，先减少部署和状态一致性复杂度。PostgreSQL 存放配置、预算账本、请求状态、价格版本及审计事件；第一版不强制 Redis。

```text
Codex / Hermes
      ↓
鉴权、请求大小检查、入口限流、检查 Key 与预算是否可用
      ↓
解析与内容分类 → 本地规则
      ↓
审核缓存 → 公平队列 → OpenAI Moderation
      ↓ 通过
选择有权限且健康的账号 → 原子预留预算与取得并发名额
      ↓
指定代理或直连 → Codex 上游
      ↓
流式返回、用量结算、审计记录
```

审核不得与上游推理并行启动。审核排队不占用上游并发名额。审核后需重新检查 Key、策略版本和预算，防止等待期间配置已变化。

模块：接入协议适配、账号授权、出口管理、预算账本、价格同步、调度器、本地规则引擎、审核适配器、审计日志、管理 API 与界面。

## 4. 本地默认规则包

规则内置为版本化 YAML/JSON 资源，初始化导入数据库。后台允许启停、编辑、恢复默认；更新默认包不得覆盖管理员改动，先显示差异。规则只能配置声明式匹配，不允许上传任意可执行脚本。

| 规则组 | 首批内容 | 默认动作 |
|---|---|---|
| SECRET | 完整私钥块、明确结构的访问密钥、含密码的数据库连接串 | 高确定性暂停；疑似匹配提示复核 |
| CONTENT | 风险词与意图组合，如要求伤害、窃取或外传的组合线索 | 标记，继续交官方审核；不把普通词直接判违规 |
| INJECTION | 外部资料要求忽略指令、读取凭证、执行无关命令 | 标记；涉及凭证或明确危险工具参数时暂停 |
| EXFIL | 可识别的“读取敏感位置＋向外发送”工具参数组合 | 暂停可识别的调用；其余记录风险 |
| FORMAT | 超大请求、无效 JSON、不支持的附件、无法解析的载荷 | 拒绝或返回审核不可完成 |

实现要求：

- 每条规则保存 id、版本、类别、说明、匹配范围、动作、严重度、例外条件、正反例和启用状态。
- 匹配范围区分用户消息、系统指令、代码、工具定义、工具结果及外部引用。来自客户端的角色标签不是可信豁免依据。
- 对规范化副本检查大小写、Unicode 和零宽字符，保留原始请求不变。
- 使用有运行时间保障的正则实现；限制规则长度、请求大小及解析深度。
- 不默认封禁“攻击、破解、成人、杀死进程”等单词；不使用过宽的白名单跳过整条请求。
- 敏感凭证检查在外部审核前运行；命中后不向审核 API 发送原文，也不把匹配到的秘密写入日志。
- 每条启用规则至少提供 2 个应命中及 2 个不应命中的测试用例。示例只用合成凭证与无害占位内容。

本地通过不代表审核通过，仍进入官方审核。提示词注入与外传的模糊结果不能仅靠 Moderation 的未命中自动消除。

## 5. 官方审核适配

默认端点为 https://api.openai.com/v1/moderations，默认模型为 omni-moderation-latest，使用单独的 Platform API Key。审核密钥与 Pro OAuth 凭证分开管理。

适配器输出统一结果：allow、block、unavailable、unsupported；保留官方返回的模型标识、分类标记及分数。第一版采用官方 flagged 判定，不拍脑袋设置所有类别统一阈值。管理员调整阈值后必须记录策略版本，并用样本验证。

审核请求可独立指定代理。密钥加密保存，管理界面仅显示掩码。审核出口固定为配置的官方端点，不能受下游用户提供的 URL 或 Header 控制。

图片不支持的分类不能据零分认定安全；不可获取或不支持的内容返回 unsupported。第一阶段验证文本与图片实际格式和大小限制，不写死未经验证的上限。远程图片下载若实现，需限制协议、大小、重定向并阻止访问内网地址。

## 6. 长上下文、缓存与限流

### 6.1 默认完整输入覆盖

对当前请求中可访问且可解析的内容检查，不仅检查最后一句。超长内容按审核服务支持范围分段，包含交叠上下文；任何段失败则整条请求不能继续。不截断后标记全文通过。

previous_response_id、压缩历史、加密推理内容等可能导致网关看不到完整上下文：P0 必须验证客户端实际行为。无法重建必要上下文时，严格模式明确拒绝或声明该调用形式不受支持，不伪装为完整审计。

### 6.2 缓存

第一版仅缓存完全相同的规范化审核输入。缓存键包含 Key/策略作用域、内容 HMAC、规则版本、审核模型配置与策略版本；图片变化也必须使缓存失效。短期缓存建议 10 分钟；latest 别名可能升级，短 TTL 只降低陈旧风险，不保证感知所有模型变更。

不采用仅按单段通过结论放行新组合的优化。后续增量模式可检查新增内容与相关上下文，但需独立标为覆盖范围有限，默认关闭。

### 6.3 队列与故障

| 参数 | 初始建议（可配置，非官方限制） |
|---|---|
| 审核吞吐预算 | 实际 API 账户 RPM/TPM 的 80% |
| 公平性 | 按 Key 调度，设置每 Key 等待数限制 |
| 最大等待队列 | 全局 50、单 Key 10 |
| 排队期限 | 30 秒 |
| 单次外部调用超时 | 10 秒 |
| 审核阶段总期限 | 60 秒，含多段审核与重试 |
| 瞬时错误重试 | 最多 2 次，受总期限约束，遵守 Retry-After |
| 审核不可用 | 暂停请求，不调用上游 |

上述期限需要用真实长上下文负载校准。超过期限的请求返回 unavailable；不要把未审核与审核违规混为一谈。API Key 增加不等于同组织限额增加。

客户端取消后移除等待任务；已进行的调用尽力取消。网关重启不恢复执行旧请求，客户端重新发起并重新检查。多个设备只等待自己的请求，不能被一个大任务无限阻塞。

## 7. 输出与工具的支持范围

默认第一版只承诺输入前置阻断，保留正常 SSE 流式响应。不得把此模式显示为“输入输出全审计”。

后续可选完整输出缓冲：收齐响应、审核后再发送，会增加首字延迟与内存需求。事后输出审核可以记录事件，但不能收回已发送内容。无论输出是否被拦截，上游已发生的用量必须结算。

工具调用参数只有完整组装后才可判断；若要网关阻断某个可见工具调用，必须在向客户端交付该调用前完成检查。通用 shell 语义无法靠正则可靠穷举。Hermes/Codex 本地执行权限、目录限制和上传目标控制作为后续客户端集成，不计为网关第一版已实现能力。

## 8. 预算、价格与账号衔接

金额使用定点数或整数微美元，禁止浮点累计。按非缓存输入、缓存输入、输出及适用计费档位折算。每请求保存价格版本。

示例：账号 A 内部周预算 $100，设备甲分配 20%，则该设备对 A 的预算为 $20。组预算 $300 的 20% 为 $60。单账号、组和 Key 总限额同时存在时均需满足，不能叠加获得额外额度。

默认预算周期建议 Asia/Shanghai 周一 00:00 重置，可配置；不假装与上游周窗口同步。百分比在周期开始时生成额度快照，周期中增减账号不自动放大既有额度；人工变更留痕。上游余额与内部预算分别显示。

请求前原子预留可证明的最高费用，完成后幂等结算，释放差额。用量不明则保留待核对预留，不自动退款。并发名额与资金预留需处理取消、异常、服务重启及重复回调，不能因租约到期而允许仍在上游运行的请求被忽略。

价格同步默认每日检查官方来源，成功验证后仅用于新请求。官方机器可读来源和可解析性在 P0 验证：来源不可用则保留上次有效价格；未知模型、无法识别的新计费档位在严格模式拒绝。首次无有效价格不得调用。管理员可手动覆盖，自动同步不覆盖人工值。

账号刷新采用每账号互斥，避免并发刷新损坏凭证。账号出口应用于服务端换取凭证、刷新及推理调用；用户浏览器登录的出口不由 VPS 代理配置自动控制。代理故障默认暂停，只有显式允许才切备用或直连。审核拒绝不触发账号切换。

## 9. 数据与管理界面

主要实体：accounts、proxy_profiles、account_groups、device_keys、budget_policies、budget_periods、reservations、usage_ledger、price_versions、audit_rules、audit_policies、audit_events、request_states、admin_events。

审核事件最少字段：request_id、Key 标识、规则/模型/策略版本、决定、分类、耗时、缓存状态、覆盖范围、错误类型、脱敏摘要。默认保留 14 天，可配置；不保存完整正文、Authorization、OAuth token 或代理密码。日志失败时，要求可审计的请求在上游开始前停止；上游已开始则进入补记/告警流程，避免重复调用。

管理界面包括：账号与出口、账号组、设备 Key、预算与价格版本、审核状态、规则编辑、事件列表和误判复核。复核不自动重放请求；任何例外只针对精确范围且有有效期，不因一次误判关闭整组审核。默认不因单次命中永久封禁设备 Key。

业务错误区分 blocked、unavailable、unsupported、budget_exceeded、concurrency_exceeded、upstream_unavailable，按客户端协议包装；SSE/WebSocket 需使用协议内错误，不输出伪造成功响应。

## 10. 实施阶段与交付门槛

以下是执行顺序，不是完成时间承诺。所有阶段当前均未开始。

| 阶段 | 工作与交付物 | 完成条件 |
|---|---|---|
| P0 可行性验证 | 单账号授权、真实客户端协议、SSE/WS需求、计量上界、官方审核容量、价格源验证报告 | 明确支持与拒绝的调用形式；严格预算和必要上下文可行才进入完整实现 |
| P1 基础网关 | Go骨架、数据库迁移、Key鉴权、账号/代理适配、合成上游测试服务 | 单客户端可受控调用；直连/代理故障行为符合配置 |
| P2 本地规则 | 默认规则包、版本化配置、规范化与解析器、正反例测试 | 默认规则可编辑恢复；凭证命中不发送外部且日志无原文 |
| P3 官方审核 | Moderation适配、全输入提取、分段、缓存、公平队列、取消与故障处理 | 拒绝/不可用/不支持时上游调用为零；多客户端排队可控 |
| P4 预算和调度 | 价格版本、预留结算、账号组与百分比、双层并发 | 并发无超卖，重复结算无重复扣费；未知费用拒绝 |
| P5 管理界面 | 配置、用量、审核日志、规则差异、复核与状态页面 | 无需改数据库即可完成日常管理；敏感值不回显 |
| P6 部署验收 | Compose、HTTPS、备份恢复、首批客户端与扩容试运行、运行手册 | 核心故障测试通过，实际吞吐和延迟有记录，恢复后账本一致 |

P0 未通过的项目必须明确缩小支持范围或提出替代设计，不能继续以“后续修复”掩盖核心不可行点。先使用合成上游和合成测试数据；真实账号与凭证通过环境或管理界面配置，不写入仓库或对话。

## 11. 验收用例

- 审核：正常代码、中文讨论、授权安全分析、医疗与引用语境的误判统计；合成风险输入的拦截统计，逐类报告，不声称真实世界100%。
- 覆盖：长文本末尾风险、跨段组合、工具返回、图片、历史引用、未知载荷；不支持情况必须显式报错。
- 凭证：合成秘密命中后审核服务与模型上游收到请求数均为零，日志无完整匹配值。
- 故障：审核429/超时/无效JSON、断网、数据库故障、取消、进程重启；未通过审核时上游请求数为零。
- 预算：并发预留、价格更新、跨周、账号组变化、流断用量缺失；账本无重复扣款、无预算超卖。
- 网络：代理断开时默认无直连流量；允许备用时只有配置出口被使用。
- 客户端：以初期设备为首批样本，分别测试鉴权、流式响应、工具调用、取消、错误提示；再新增第六个及更多逻辑客户端，验证不改代码即可接入。具体软件版本写入验证报告。
- 容量：记录审核token量、排队P50/P95、审核P50/P95、429比例及缓存命中率。延迟目标待P0基于真实账户限额确定，不预先承诺任意设备规模的全量实时审核。

## 12. 开始开发前需要的环境信息

不阻碍计划交付，进入相关阶段再提供：VPS操作系统、CPU/内存、部署域名、代理连通方式、Codex与Hermes具体版本、Pro账号数量、Platform审核API实际限额、各Key预算与并发值。凭证通过安全配置入口输入。

## 13. 参考资料

以下支撑设计方向，不能证明第三方部署的实际配置；实现时固定所参考源码提交并检查许可证。

- [CPA Codex授权实现](https://github.com/router-for-me/CLIProxyAPI/blob/main/internal/auth/codex/openai_auth.go)：授权、刷新与代理参考。
- [Sub2API 内容审核实现](https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/service/content_moderation.go)：规则、阈值、阻断及日志参考。
- [Sub2API 独立审计配置](https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/securityaudit/prompt_config.go)：配置版本与审核服务适配参考。
- [OpenAI Moderation指南](https://developers.openai.com/api/docs/guides/moderation)：请求格式、返回分类及能力边界。
- [官方审核模型及限额](https://developers.openai.com/api/docs/models/omni-moderation-latest)：模型免费、速率限制按API账户等级变化。
- [LiteLLM OpenAI Moderation](https://docs.litellm.ai/docs/proxy/guardrails/openai_moderation)：调用前审核和完整输出缓冲模式参考。
- [NeMo Guardrail Types](https://docs.nvidia.com/nemo/guardrails/about-nemo-guardrails-library/rail-types)：输入、工具执行与输出分层参考。
- [Codex认证文档](https://learn.chatgpt.com/docs/auth)：订阅授权与API访问方式区分。
- [官方API价格](https://developers.openai.com/api/docs/pricing)：内部美元折算候选来源，自动解析能力待P0验证。

本文件是实施计划。尚未生成可执行规则包、实现网关、调用真实审核接口或部署服务。

## 14. 开发执行约定与文档优先级

本节及后续章节补充可直接实施的契约。第1—13节说明目标与背景，第14节以后提供字段、行为与交付要求。遇到无法同时满足的条目，开发者须记录冲突和实测证据，不可自行取消严格预算、审核前置或代理禁止直连等用户要求。

“必须”表示验收约束；“默认”表示可由管理员配置；“建议”表示实现可替换但需说明；“待验证”表示没有实测证据，不得把它写成已支持功能。示例数值不是账号实际额度，不能直接初始化为用户可用余额。

开发开始先阅读本文件与 DEVELOPMENT_HANDOFF.md，建立需求到实现、测试的映射。单独维护 docs/DECISIONS.md 和 docs/KNOWN_LIMITATIONS.md。没有真实凭证时，可完成模拟链路、数据层、规则、管理界面及故障测试，真实联调项标记待验证，不得伪造响应或把模拟成功当成真实兼容。

第一版为单活后端。多副本部署必须显式禁止或用数据库全局锁选主；本地并发计数、队列和缓存不能在启动第二实例时悄悄失效。扩容首选可配置资源上限及升级单实例容量，多实例协调属于后续阶段。

## 15. 仓库组织与建议技术栈

后端使用 Go、PostgreSQL；前端建议 TypeScript + React + Vite，使用锁文件固定依赖。具体受支持版本在启动实现时确认并写入README，不在本计划猜测未来版本。数据库使用编号迁移，日期以UTC存储，周期按策略指定时区计算。

```text
cmd/server/                    后端入口
internal/auth/                 管理员会话、成员与Key鉴权
internal/accounts/             OAuth、凭证刷新、账号状态
internal/egress/               代理与出口隔离
internal/gateway/              协议适配与生命周期
internal/audit/                提取、规则、官方审核、缓存和队列
internal/billing/              价格、预算、预留与账本
internal/scheduler/            账号选择、黏性及并发
internal/admin/                管理接口
internal/storage/              数据访问和事务
migrations/                    数据库迁移
rules/defaults/                版本化默认规则包
tests/fixtures/                合成请求、响应及规则样本
tests/integration/             模拟审核、模拟上游及代理
web/                           管理界面
deploy/                        Compose、环境变量样例、部署说明
docs/                          API、决策、验证、运维和限制
```

第一版至少交付 Makefile 或等效任务入口：lint、test、test-integration、build、migrate、dev。开发者根据实际工具链提供可执行命令，不保留只有标题的脚本或伪测试。

## 16. 主体、权限与核心数据契约

### 16.1 访问主体

- member：管理员创建的使用主体；初始可只有一个所有者，之后可增加成员。第一版无需公开注册。
- client：成员名下设备或Agent的逻辑记录，type取computer、hermes、cli、other，数量不限于初期样本。
- api_key：归属一个member，可选关联一个client。一个client可拥有多个Key，轮换不要求改动客户端实体。
- role：admin与member。第一版可只提供管理员操作界面，但数据和API访问必须按所有者隔离，不把所有使用者默认当管理员。

### 16.2 表结构最低要求

所有可变配置实体包含id、created_at、updated_at、version；修改采用乐观锁。金额字段在第18节统一定义。秘密字段只存密文或不可逆校验值，响应DTO不直接序列化数据库对象。

| 表 | 必备业务字段及约束 |
|---|---|
| members | name、role、status；禁用立即影响其所有新请求 |
| clients | member_id、name、type、notes、status |
| api_keys（取代前文device_keys命名） | member_id、client_id、name、public_prefix、key_hash、status、expires_at、concurrency_limit、allowed_models、audit_policy_id；密钥仅创建时显示一次 |
| proxy_profiles | name、kind(direct/http/socks5)、endpoint、credentials_ciphertext、status；direct不可带代理地址 |
| accounts | provider、label、credentials_ciphertext、credential_version、expires_at、state、concurrency_limit、priority、egress_policy_id；不把access token当公开字段 |
| egress_policies | primary_proxy_id、failure_mode(stop/fallback)、ordered_fallback_proxy_ids；直连必须以显式direct项出现 |
| account_groups / account_group_members | 组名、状态、成员关系、顺序/权重；关系唯一，停用账号不参与选择 |
| key_routes | api_key_id、target_type(account/group)、target_id、priority；默认无路由即禁止调用 |
| budget_policies | owner_type(member/key/account/group/key_account/key_group)、owner_refs、period(day/week)、timezone、mode(fixed/percent)、amount或percent_bps、base_policy_id、version；禁止循环引用 |
| budget_periods | policy_id、period_start/end、limit_snapshot、spent、reserved；(policy_id,period_start)唯一 |
| requests | request_id、api_key_id、client_request_id、payload_hmac、state、coverage、account_id、group_id、price_version_id、时间戳、error_code；不持久化完整body |
| reservations | request_id、budget_period_id、amount、state；(request_id,budget_period_id)唯一 |
| usage_ledger | request_id、attempt_id、计量字段、cost、price_version_id、entry_type；结算事件唯一，修正用追加记录 |
| price_versions / model_prices | source_url、source_hash、fetched_at、activated_at、origin、model、计费维度及档位；版本不可变 |
| audit_rules / audit_policies | 规则与策略版本、内容、scope、enabled、modified_by；保留默认包来源 |
| audit_events | request_id、decision、coverage、规则命中、类别、脱敏摘要、策略/模型版本、耗时、错误；可多条事件但最终决定唯一 |
| admin_events | actor、action、target、变更字段、脱敏前后值、request_id、时间；禁止写秘密 |

数据库外键与唯一约束必须实现，不能只依赖前端校验。已参与账本的账号、Key、组采用停用/软删除，不级联删除账目。分页列表默认20条、最大100条；排序字段白名单。

## 17. 接口契约与客户端兼容

### 17.1 数据面

首要目标为POST /v1/responses和GET /v1/models。/v1/chat/completions仅在Hermes实际需要时实现；不能仅因返回HTTP 200就宣称兼容。WebSocket单独列能力项，需覆盖每一轮消息的审核、预算和并发，不能只审握手或首帧。

认证使用Authorization: Bearer <gateway-key>；此Key仅对本网关有效，不是OpenAI官方Key。入站Authorization不能透传给上游；上游凭证只由账号适配器注入。过滤Host、Forwarded、代理配置等可改变路由的客户端字段。用户提供的模型名必须通过允许模型列表和价格映射双重检查。

模型、端点、流式、工具、图片、历史引用、压缩项和取消行为分别建立兼容矩阵。每行标记verified、mock_only、unsupported或pending，附客户端版本及测试ID。管理界面只把verified能力显示为已验证。

建议错误映射：

| HTTP（流开始前） | code | 含义 |
|---|---|---|
| 401 | invalid_api_key | 无效、过期或撤销Key |
| 403 | access_denied | 成员、模型或路由无权限 |
| 400 | audit_blocked | 本地规则/模型拒绝，非可自动重试错误 |
| 422 | audit_unsupported | 审核所需输入不可访问或格式不支持 |
| 429 | audit_queue_full / concurrency_exceeded | 队列/并发不足，返回可用的Retry-After |
| 429 | budget_exceeded | 周期预算不足，返回reset_at；不频繁自动重试 |
| 503 | audit_unavailable / no_healthy_account | 服务不可用或无可用账号 |
| 502 | upstream_error | 上游异常，是否重试取决于是否已发送 |

统一错误体包含error.code、error.message、request_id、retryable，可选retry_after_seconds、reset_at。不得把上游原始错误直接回显，以免包含凭证。实际SSE/WS错误事件名称与结构依P0确认的协议实现。

### 17.2 管理面

路径以/api/admin为前缀，必须管理员鉴权；下表是功能契约，开发者交付完整OpenAPI并明确请求/响应schema。

| 资源 | 最低接口 |
|---|---|
| 会话 | POST /session、DELETE /session、GET /session；登录限流 |
| 成员/客户端 | GET/POST /members、PATCH /members/{id}；/clients同类CRUD |
| Key | GET/POST /keys、PATCH /keys/{id}、POST /keys/{id}/revoke；绝不提供明文列表 |
| OAuth | POST /accounts/oauth/sessions、GET /accounts/oauth/sessions/{id}；回调处理按验证方案；支持取消与过期 |
| 账号/组/路由 | /accounts、/groups、/keys/{id}/routes；暂停与恢复分别有动作记录 |
| 出口 | /proxies、/egress-policies、POST /proxies/{id}/test；探测使用服务端固定允许目标 |
| 预算 | /budget-policies、GET /budget-periods、GET /ledger；不能直接编辑账本余额 |
| 价格 | GET /prices/versions、POST /prices/sync、POST /prices/overrides；同步结果可查看 |
| 审核 | /audit/policies、/audit/rules、POST /audit/rules/validate、GET /audit/events |
| 复核 | POST /audit/events/{id}/review；只记录结论或创建精确例外，不自动重发 |
| 系统 | GET /status、GET /admin-events；配置导出默认不含秘密 |

写接口提交version，冲突返回409。规则测试默认仅运行本地；发送测试文本到外部审核必须在界面明确提示目标。不得把任意URL测试器变成内网探测代理。

## 18. 美元预算的精确定义

### 18.1 计价

推荐金额以NUMERIC(30,12)美元存储，Go使用十进制定点库；也可用等价整数实现，但必须说明舍入策略。价格每百万token计价：

```text
cost = (uncached_input × input_rate
      + cached_input × cached_rate
      + output × output_rate) / 1,000,000
      + supported_fixed_fees
```

只有适配器确认input_total包含缓存时才取uncached_input=input_total-cached_input。计量字段不一致、负数或缺失不能当0处理。推理token若已包含在output中不重复收费。价格档位、长上下文、快速模式、图像和工具固定费用必须明确映射，否则严格模式拒绝相关能力。

同一请求金额只生成一次费用事件，同时应用到相关预算作用域；界面汇总不把多个作用域扣账相加形成重复消费。预留金额向上舍入，实际结算按同一版本规则计算。价格在预留时冻结。

### 18.2 周期与百分比

百分比用整数基点percent_bps：2000代表20%，范围0—10000。mode=fixed必须填写amount；mode=percent必须引用同周期基础预算，不接受模糊“整个Pro实际余额”。金额0表示禁止；无配置含义在初始化时明确，默认没有预算不允许生产调用。

账号组基础预算第一版采用管理员明确设置的固定金额，不自动把重叠组的账号额度求和。成员可属于多个组，但单次请求记录唯一计费路由组，不得在原组预算不足后悄悄改用别组规避原约束。

示例：组G周预算$300、账号A周预算$100、Key K周预算$50、K对G为20%（$60）、K对A为20%（$20）。请求选中A时，所有适用预算共同限制，K经A最多$20，K跨账号合计最多$50，并受组与账号总额度约束。

百分比默认是使用上限，不是保证容量的专属预留。多Key上限合计超过100%可配置，但界面警告“共享容量，不保证全部用满”；父预算始终限制总支出。若未来需要独享额度，另行引入容量保留，不偷换现有语义。

周期快照唯一且不可覆盖。周期内改额度默认下周期生效；立即生效需显式操作且新额度不得小于spent+reserved。跨周期请求始终结算到预留所属周期；新周期不清理旧预留。日和周限额同时存在时同时锁定并扣减。

### 18.3 原子预留与结算

按预算周期ID固定顺序锁行，事务内检查每个spent+reserved+candidate<=limit，再写预留及请求dispatching状态；并发槽与调度状态必须同一单活协调器处理，失败释放全部资源。不得先调用上游再预留。

结算使用request_id+attempt_id作为幂等键。重复结算不得重复扣费；异常修正写追加账本。客户端断开不表示上游免费或立即停止，不能直接退款。

若已发送请求无法确认结束，状态进入unknown，保留资金及保守并发占用，账号可置recovery_hold避免突破并发。后台提供证据驱动的人工核对，不允许超时自动假定无消耗。发现actual>reservation时暂停相关能力、如实记账并告警，不能把金额截成预算值掩盖错误；此情况意味着严格预算验收失败。

## 19. 请求与账号状态机

```text
received → validated → local_checked → audit_queued → auditing
         → audit_passed → reserved → dispatching → streaming
         → settling → completed
```

分支终态：rejected（格式/权限/规则）、audit_failed（不可用/不支持）、cancelled_before_dispatch、failed_before_dispatch。dispatching之后的网络不确定性进入unknown或settling，不退回“未发送”。

每次迁移记录时间及原因。审计决定必须持久化后才允许dispatching。崩溃恢复将未发送任务结束为cancelled_before_dispatch；已可能发送任务置unknown，不自动重放。账本恢复优先于接受新流量。

账号状态区分active、paused、refreshing、reauth_required、quota_exhausted、proxy_unavailable、recovery_hold。健康检测不消耗推理预算；不能没有可靠接口就伪造“剩余官方美元余额”。账号耗尽不等于Key内部预算耗尽。

组内默认选择低并发、健康且预算满足的账号，优先级相同时轮转。会话需要上游状态时保持账号黏性；绑定键包括api_key_id和客户端会话标识，不能跨Key共享对话状态。绑定账号不可用时，只有证实可完整重建上下文的请求才能切账号，否则返回明确错误。

自动重试只限能够证明未到达上游的错误；请求可能已执行时不自动重试，避免重复花费。若客户端支持幂等键，可用于识别重复请求：同Key同幂等键不同body返回409；不支持重放流时返回已有请求状态，而不是再次执行。

## 20. 默认规则包的开发明细

以下为最低规则目录，开发者需产出真实可运行的匹配器、配置和样本，不得仅复制名称作为空实现。

| ID | 规则 | 默认决定 | 实现要求 |
|---|---|---|---|
| SECRET-001 | 完整PEM私钥块 | block | 匹配BEGIN/END同类边界及非占位有效载荷；公开证书/公钥不命中 |
| SECRET-002 | 私钥/凭证文件内容被附带提交 | block或review | 结合结构与敏感字段；仅提到文件名不拦截 |
| SECRET-003 | 带非占位密码的连接串 | review | 解析已支持URI格式；模板${PASSWORD}等不是实际秘密，但仍正常送审 |
| SECRET-004 | 高可信访问密钥格式 | review | 每种格式单独规则与来源说明；不靠sk-前缀就直接定性 |
| CONTENT-001 | 风险词＋行为意图组合 | flag | 支持all/any组合和限定距离；独立词不硬阻断 |
| INJECTION-001 | 外部内容要求覆盖原任务 | flag | 标记来源，不执行其中指令，也不把模型未命中当成注入安全证明 |
| INJECTION-002 | 覆盖指令同时要求取凭证/外传 | review | 组合匹配并返回定位区间，不打印内容 |
| EXFIL-001 | 明确读取敏感内容并发送外部 | review | 只对支持的结构化参数判断；不可解析脚本不得宣称安全 |
| FORMAT-001 | body超过配置上限 | reject | 解压后也限制；读取过程限制，不能全量读完再检查 |
| FORMAT-002 | 无效或过深结构 | reject | JSON深度和字段数量上限，避免解析资源耗尽 |
| FORMAT-003 | 严格模式不支持的输入 | unsupported | 返回具体不支持的内容类型 |

动作语义：flag为记录风险且必须继续官方审核；review为当前请求暂停并返回审核拒绝类错误，等待人工复核后由用户重新发起；block为当前请求拒绝；unsupported为无法完成审核而非违规。Secret高确定性与review均在任何外部内容请求之前终止。普通flag默认不改变官方判定，但日志保留“注入检测不完整”等覆盖限制。

配置样例（结构契约，实际pattern须实现并通过测试）：

```yaml
id: SECRET-001
version: 1
category: secret
enabled: true
scope: [user_text, instructions, tool_definition, tool_result, code, quoted_text]
matcher:
  type: pem_private_key
action: block
severity: high
message: 请求疑似包含私钥，请移除后重试。
fixture_set: secret_001
```

每类匹配器使用有限枚举，复杂正则由Go regexp等受限引擎执行；不使用eval。规则测试结果包括命中位置、规则ID和遮蔽后的示例，不返回完整秘密。恢复默认建立新版本而不是覆盖历史。

## 21. 审核输入提取的实现要求

建立AuditDocument中间结构：segments[{role,source,type,text/image_ref,position}]、coverage、missing_parts、content_hmac。覆盖级别为full_visible、partial、unsupported，不使用无条件的full_conversation；full_visible仅表示当前可见内容完整提取。

Responses适配器逐项处理input字符串、message内容、instructions、工具描述、function_call参数、function_call_output及图片；具体字段和新增类型以P0实测schema固定。Chat Completions和WebSocket各自有提取器。不能仅搜索字段名content然后跳过其他文本。工具输出中的JSON需在受限深度内提取文本，不解析执行代码。

编码内容仅解码协议明确定义的格式；不无限尝试base64解码。未知类型严格模式拒绝。压缩历史和不可见服务端会话在兼容矩阵中单列，不为维持可用性默认绕过。

审核输入不是聊天模型的可信指令：向Moderation发送待分类数据，不把用户内容嵌入可执行模板。保留角色标识有助于定位，但不承诺接口理解全部多轮语义。

缓存仅存HMAC与审核结果，不存原文；只在内存中保留等待/审核期间的body，设置内存总量上限（不只限制队列条数）。规则更新、策略变化、Key权限撤销后不能沿用过期通过结果。

## 22. 配置默认值与部署说明

下列为建议实现默认值，首次启动UI必须显示未完成项，不自动开启真实上游。

| 配置 | 默认 | 说明 |
|---|---|---|
| production_ready | false | OAuth、价格、审核Key、路由、预算配置和P0支持矩阵通过后才启用 |
| public_registration | false | 管理员创建成员 |
| per_key_concurrency | 1 | 可修改；不等于上游允许值 |
| per_account_concurrency | 1 | 上游实际约束另行遵守 |
| request_body_limit | 8MiB | 管理员可调；图片/大上下文需容量验证 |
| audit_body_memory_limit | 128MiB | 排队原文总量；超过返回queue_full |
| audit_mode | pre_call | 不默认并行调用上游 |
| audit_coverage | full_visible_required | 不支持的历史依赖明确拒绝 |
| audit_failure_mode | stop | 不静默放行 |
| audit_cache_ttl | 600s | 版本变化失效 |
| audit_provider_limits | unset | 首次必须填实际账户值或经可信响应确认，不猜等级 |
| output_audit | disabled | UI明确输入审核范围 |
| log_retention | 14天 | 可调，账本不跟随短期审计日志删除 |
| price_sync_interval | 24h | 失败保留有效旧版本并显示过期时间 |
| proxy_failure_mode | stop | 回退需显式配置 |

部署交付.env.example，只包含变量名与占位值；数据库与管理接口不默认暴露公网。反向代理提供HTTPS，SSE关闭不适当的缓冲并配置长连接超时，WS如支持需转发升级头。后台Cookie会话使用HttpOnly、Secure和CSRF防护，管理员密码使用成熟密码哈希库，无出厂通用密码。

OAuth会话使用随机state、PKCE、短有效期、单次消费并绑定管理员会话；回调URI必须为上游认可且实际验证的地址，不臆造任意域名可用。凭证加密主密钥不和数据库备份保存在同一公开目录；交付备份与密钥恢复说明。

账号出站连接池按账号/代理配置版本隔离，改变出口后不能复用旧出口连接。禁用系统环境代理的隐式继承，SOCKS5 DNS方式写入说明；备用列表耗尽返回失败，不自动直连。浏览器登录出口和服务端出口分开展示。

## 23. 实施任务拆分与证据交付

每项完成需在docs/IMPLEMENTATION_STATUS.md记录文件路径、测试ID、结果、限制。禁止用“页面已做”“接口返回成功”代替端到端证据。

| 任务ID | 阶段 | 交付证据 |
|---|---|---|
| P0-01 | 协议验证 | docs/COMPATIBILITY.md含真实客户端版本、能力状态、脱敏样本 |
| P0-02 | 费用上界 | docs/BILLING_BOUNDS.md逐模型/模式解释最高费用，输出限制实际有效证据 |
| P0-03 | 审核容量 | docs/AUDIT_CAPACITY.md含账户实际限额、短/长输入耗时、429和队列结果 |
| P0-04 | 官方价格源 | docs/PRICE_SOURCE.md含源URL、解析器、档位映射、失败及变更处理 |
| P1-01 | 基础服务 | 可启动服务、迁移、Key鉴权、模拟上游与账号出口测试 |
| P1-02 | OAuth | state/PKCE/单次回调、刷新互斥、过期重登测试 |
| P2-01 | 默认规则 | rules/defaults、正反例fixture、版本合并与恢复测试 |
| P3-01 | 官方审核 | 文本/图片/工具提取覆盖测试、外部失败模拟、被拒请求上游零调用断言 |
| P3-02 | 排队缓存 | 公平性、并发取消、权限变化、内存上限和缓存隔离测试 |
| P4-01 | 预算 | 百分比/多层限制/跨周期/并发/未知用量测试 |
| P4-02 | 调度 | 账号组权限、黏性、代理断线、无隐式直连及重启恢复测试 |
| P5-01 | 管理界面 | 每个写接口有真实持久化与刷新回读证据；空/错/加载态可用 |
| P6-01 | 部署恢复 | 新数据库部署、升级、备份恢复、单活锁、重启后账本核对 |
| P6-02 | 扩展接入 | 新增第六个及更多逻辑客户端无需改代码；可配置压测报告 |

自动测试主要通过模拟服务确定性执行；真实联调单独开关，默认test不得消耗真实预算。每个关键故障测试附上游请求计数，不靠日志文字判断是否被拦截。测试报告区分passed、failed、not_run和blocked，不允许把未运行计入通过率。

## 24. 完工交接与后续审查标准

其他开发工具初步完成后，应交付：源码与依赖锁、迁移、默认规则包、OpenAPI、Compose、环境变量样例、启动运维手册、兼容矩阵、费用上界报告、价格来源说明、审核容量报告、测试命令和真实结果、已知限制、相对本计划的偏离清单。

审查重点依次为：

1. 审核未通过是否仍可能调用上游；不可见内容是否被伪装为已审核。
2. 预算是否先预留、是否并发超卖、实际用量缺失是否错误退款。
3. Key与账号组权限隔离，是否可通过请求字段选择未授权账号或绕过审核。
4. 代理故障是否隐式直连，刷新与推理出口是否一致。
5. 密钥是否出现在日志、前端响应、错误信息或备份样例。
6. SSE/WS、工具与取消是否真正在目标客户端验证。
7. 设备与成员数量是否动态配置，是否存在固定五个的代码假设。
8. 管理页是否连接真实接口，统计是否来自真实账本而非静态数据。

初步完工后由后续审查确认是否可用于真实账号。开发者不得自行把未验证核心能力描述为“已生产就绪”。本次仅交付规格，真实部署与账号操作留在后续实施任务中进行。
