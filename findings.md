# 调研依据与决策

日期：2026-09-12。以下来自本次对话前序源码检查与官方文档阅读，不是对第三方部署的实测结论。

- CPA 的 Codex OAuth 客户端提供 PKCE、凭证刷新、代理相关逻辑，可作为参考；独立实现不保证改变上游识别或账号限制。
- Sub2API 包含 content_moderation 与 securityaudit 模块，具备前置阻断、关键词、分类阈值、审计日志及可配置审核服务。
- LiteLLM 提供 pre_call/during_call/post_call 审核；阻止内容进入上游应选择 pre_call。
- NeMo 将输入、检索资料、工具执行和输出控制分层；内容分类不能替代工具权限。
- OpenAI Moderation 的接口与模型分别是 /v1/moderations 和 omni-moderation-latest；免费但受账户 API 限额约束。
- FlyAPI 普通用户页面没有显示审核配置，无法确认其服务端具体实现。
- 用户选择独立开发、Pro OAuth 上游、五个设备/Agent Key、固定美元内部预算与百分比分配、账号/Key 双层并发、每账号出口、可配置出口故障策略。
- 官方桌面端直接登录的消耗及操作不经过本项目；网关无法对此分设备记账或审核。
- 用户选择内置可编辑本地规则并接官方审核，不要求第一版自部署审核模型。

来源链接与具体应用见主计划末尾。

## v0.2 用户补充

- 初期三台电脑和两个Hermes不是上限，后续可能增加客户端。
- 用户将先让其他工具实施，之后再交回独立审查；需要详细、可执行、可验收的规格与交接说明。
- 本次更新只细化设计，没有新增外部能力实测，P0事项仍待验证。

## 2026-09-14 WebUI 重构发现

- 原方案的“控制塔”、双环额度与分组导航方向成立，但趋势图、全局搜索、链路阶段健康度和事件 tail 均缺少现有后端数据契约。
- 当前正式前端应继续使用哈希路由：Go 服务仅通过 `http.FileServer` 托管 `web/dist`，BrowserRouter 直接访问子路径会失效。
- 已将正式入口恢复为既有身份/权限/资源页，并将状态页仅接入已有 `/api/admin/status` 的真实快照。
- 未接入的原型目录和“完成总结”包含 mock 数据及与实际依赖不符的描述；P0 必须收敛，不能随发布镜像作为正式功能交付。
- 当前选择是 CSS token 方案，不引入未配置的 Tailwind/Router 依赖；后续如要改变选型，必须先提供完整构建配置和迁移验收。

## 2026-09-14 P3 数据层发现

- 现有 `requests`、`usage_ledger`、`accounts`、`audit_events` 与 `admin_events` 表已提供构建仪表盘快照和有界时间聚合所需的数据；现有索引覆盖 `requests(api_key_id, created_at)` 和事件时间，但仪表盘按时间范围聚合应补请求与账本时间索引。
- 现有管理 API 采用 `pageParams`，列表接口已有受管理员身份保护的统一路由；新 dashboard/search/overview 应复用 `RequireAdmin` 或 `RequireSession`，不能由前端拼接全部分页数据。
- 原型目录已在 P0 删除；正式入口再次只使用已验证的 `App.tsx`、`components.tsx` 和 `styles.css`。

## 2026-09-14 WebUI 实施与验收发现

- `account_quota_snapshots` 是 0010 迁移创建的表，但原集成测试的清理列表遗漏它；在同一 PostgreSQL 容器中运行第二轮测试会导致迁移重复建表。本次已将其纳入清理列表，真实 PostgreSQL 全套测试恢复通过。
- 新增仪表盘数据全部在服务端按 24 小时或 7 天进行有界聚合；搜索仅允许 2–80 字符前缀匹配，每类最多 5 条，DTO 不包含密文、token 或原始请求。
- 个人概览仅按当前会话成员汇总订阅、Key 与账本成本；不查询或返回账号、代理、出口等管理员资源信息。

## 2026-09-14 主题专项发现

- 截图证实浅色模式只替换部分背景 token：`styles.css` 仍有按钮、输入、Badge、Hero、配额弹窗和代码块等固定深色值，造成浅色 canvas 上的深色控件混搭。
- 现有应用已有基于 `localStorage` 的登录后主题状态，但登录页没有主题切换控件；主题 token 在样式表前后重复定义，后段 token 无法覆盖前段的硬编码颜色。
- 主题重构应以语义 token 迁移为主，不能靠为浅色模式继续追加局部覆盖规则。

## 2026-09-14 主题实施发现

- 主题初始状态若先使用固定默认值、再在 effect 中读取本地偏好，会在刷新时产生短暂错误主题，且可能覆写用户选择。现已改为 `useState(preferredTheme)` 同步读取，再由 effect 统一写入 DOM 与本地存储。
- 登录页在浅色、深色实际浏览器渲染均通过；浅色模式的输入、次按钮和卡片现均使用浅色 surface token。
- 关键颜色对比度已测量：浅色次文本 4.54:1 为最低抽查值，仍达到 WCAG AA；主按钮和正文在两主题下均显著高于阈值。

## 2026-09-14 重置卡额度发现

- 当前 `POST /api/admin/accounts/{id}/quota/refresh` 已能通过账号既有出口刷新官方额度，但列表行没有直接操作，详情弹窗刷新成功后会关闭。
- 当前快照只解析 `/wham/usage` 的套餐与额度窗口；JSONB 存储可直接兼容新增重置卡字段，无需数据库迁移。
- Sub2API 除解析 `/wham/usage.rate_limit_reset_credits` 外，还以只读方式请求 `/wham/rate-limit-reset-credits` 获取每张卡的过期时间；对外 DTO 仅保留 `available_count` 与 `expires_at`，不暴露卡 ID 或令牌。
- 重置卡详情是附加信息：其请求失败不能让五小时/每周额度刷新整体失败；字段缺失与明确返回 0 张必须在 UI 中分别展示。
- 通用资源列表刷新时会暂时卸载表格，因此额度弹窗内刷新不能立即触发整表 reload；现改为先原地更新弹窗，关闭弹窗时再刷新列表。
- 本机未安装 Python/Node Playwright；通过 Codex 内置浏览器检查了浅色、深色、列表直接刷新和弹窗刷新保持行为，浏览器控制台无错误。

## 2026-09-14 模型能力与精细计费发现

- 现有模型准入已形成“激活价格目录 → 套餐/订阅模型上限 → API Key 收窄 → /v1/models 返回”的链路；请求在审核前后均复核 Key 模型权限。管理端仍使用逗号分隔字符串，缺少目录驱动的选择器。
- 现有价格版本支持全局按模型覆盖 input/cached-input/output 单价，并在自动目录同步时保留手工覆盖；套餐版本支持一个全局 `rate_multiplier`。账本记录 base_cost、rate_multiplier、cost 和 price_version_id。
- 缺口是账号/账号池可用模型、请求模型到上游模型的映射，以及套餐/用户范围内的模型专属价格或倍率；现有全局手工价不能表达“同一模型对不同套餐不同售价”。
- Sub2API 将资源组的模型白名单同时用于模型列表和请求准入，并在资源组内保留模型映射与多维 model_pricing；其管理端可从 LiteLLM 目录同步模型名、查询默认价格。它还将最终向用户展示的价与官方参考价分离。
- 本项目应复用现有不可变 plan_versions：模型级规则必须属于 plan_version，而非直接覆写 plan，才能使已有订阅、预留和账本继续可追溯。目录价格版本继续作为采购基准；plan-version 规则是售价层。

## 2026-09-14 sub2api 整体替换调研

- 旧代码基线：1b6d901f75da6046c096fa5909d8a2cab0357c79；调研开始工作区干净。
- GPT OAuth 使用 PKCE、10 分钟会话、localhost:1455 回调 URL 手动提交，账号凭证写入旧 PostgreSQL 加密字段。
- OAuth Manager 依赖旧 accounts、oauth_sessions、account_holds；迁移需替换持久层和会话鉴权适配，不能携带整个旧网关。
- 发布文档表明 main 推送触发镜像发布；方案阶段不触发发布。

- 上游快照 bdb42e22f81fcb633ff0a060961211dd2bcb515b 已拉取并核对 Vue/Go/PostgreSQL/Redis、OpenAI 授权服务与统一刷新接口。
- 更正初步判断：旧代码注释称管理员会话绑定，但已查看路径未见 owner 检查，应作为适配验收要求而非现成功能。
- 方案：完整导入上游，GPT 授权单实现，旧库隔离，数据迁移独立于运行时；不可同时刷新同一账号。


## 2026-09-14 应用到 main
按用户要求，将已验证的隔离工作区源码应用到 main：4105 个变更路径逐项内容及权限核对一致。额外迁入 docs/subai 文档并修正忽略规则，保留 GPT 原登录方案；未提交、推送、部署或迁移旧数据库。此前 main 保留旧运行时代码的记录为历史状态。


## 2026-09-15 Console simplification
Scope: remove onboarding and administrator compliance gate; remove login agreement, model plaza and all platform social login settings/entry points. Keep password login and original GPT upstream OAuth. Plan: implement frontend/backend changes, run targeted tests/build and browser verification, publish image and update Oracle3 on port 18080 with existing new database retained.

Console simplification: removed runtime compliance guards, tour mounting/replay UI, public plaza/legal routes, social auth route registrations (preserving WeChat payment OAuth). Removed settings cards and social defaults entries. Public settings force removed features off. Initial targeted frontend 48 passed; full suite 2095 passed with one obsolete WeChat route assertion, now updated. Typecheck passed before final HomeView cleanup. Backend lint 0 issues.


## 2026-09-15 Confirm-only primary email replacement
Implement explicit confirmation for authenticated users changing an already-bound email, with no email code/current-password prompt. Preserve password hash, alias collision checks, transaction and session invalidation. First-time binding retains its separate password setup flow. Test, publish and deploy without changing the actual administrator email.
