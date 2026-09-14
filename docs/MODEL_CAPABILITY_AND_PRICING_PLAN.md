# 模型能力与精细计费方案

日期：2026-09-14  
状态：已实施，待部署验证  
目标：借鉴 Sub2API 的资源组模型白名单、模型映射和模型级定价能力，在不破坏 SubAI 已有订阅版本、价格版本、预留与账本审计语义的前提下，完成精细模型运营。

## 1. 结论与范围

现有实现已经具备三项基础能力：

1. 激活价格目录中的模型可从 `GET /v1/models` 返回；
2. 套餐、订阅和 API Key 可用 `allowed_models` 逐层收窄；
3. 价格目录可按模型手动覆盖单价，套餐版本可统一设置 `rate_multiplier`，账本记录基础成本、倍率和最终成本。

但现有实现不能表达以下运营规则：

- 一个账号或账号池实际只支持部分模型；
- 客户端模型名和 CPA/Sub2API 侧模型名不同；
- 同一目录模型对不同套餐有不同售价或不同倍率；
- 管理员和用户查看“此 Key 在此时究竟可用哪些模型、走哪个池、按什么价格计费”。

本次已覆盖文本 Responses API 的模型能力和 token 计价。图片、音频、视频、Web Search、固定调用费和长上下文阶梯保留为第二阶段能力；在支持之前，现有严格拒绝/不计价策略不变。

## 2. 设计原则

### 2.1 三层分离

| 层 | 责任 | 现有对象 | 新增对象 |
|---|---|---|---|
| 采购基准 | 模型原始 token 单价、缓存价、来源 | `price_versions`、`model_prices` | 保持现有全局手工覆盖 |
| 资源能力 | 某模型是否可被某账号/池提供、上游应使用什么模型名 | `accounts`、`account_groups` | 模型能力规则、模型映射 |
| 销售规则 | 某套餐版本对某模型收取何种价格或倍率 | `plan_versions` | `plan_model_pricing` |

不得把用户售价直接写回 `model_prices`：它是全局采购基准，任何套餐特价都不应影响其他订阅。

### 2.2 冻结与审计

每个请求固定以下内容：

```text
客户端模型名
  → 生效资源规则版本 / 目标上游模型名
  → 冻结的 price_version（采购基准）
  → 冻结的 plan_version（销售规则）
  → 输入、缓存输入、输出 token 与最终费用
```

`plan_model_pricing` 只挂在不可变的 `plan_versions` 上。变更套餐模型价格时创建新的套餐版本，并仅让新订阅或明确升级后的订阅采用它；不得修改历史版本。这样能与当前 `price_version_id`、预留、结算和未知结果恢复机制一致。

### 2.3 规则仅可收紧

模型能否调用取所有约束的交集：

```text
目录有可计价模型
∩ 套餐/订阅允许模型
∩ API Key 允许模型
∩ 路由可达且 active 的账号池允许模型
∩ 被选中账号允许模型
= 最终可调用模型
```

任一层未配置白名单代表“不额外限制”，不是“允许不存在于目录的模型”。白名单不支持模糊匹配；模型映射仅允许精确匹配，避免 `gpt-*` 规则意外扩大权限。

## 3. 目标数据模型

### 3.1 资源能力规则

新增 `account_model_capabilities`：

| 字段 | 说明 |
|---|---|
| `account_id` | 上游账号；删除账号时级联删除 |
| `public_model` | SubAI 对客户端公开的精确模型名 |
| `upstream_model` | 发给该账号/CPA/Sub2API 的模型名；默认等于 `public_model` |
| `status` | `active` / `disabled` |
| `priority` | 同一账号多条别名时排序，仅用于展示 |
| `created_at`, `updated_at`, `version` | 审计与乐观锁 |

唯一键：`(account_id, public_model)`。一个账号没有任何 capability 行时表示“能力未知”：为兼容存量直连 Codex 账号，暂时按全目录候选；新建 OpenAI-compatible 账号在生产模式必须显式配置至少一条能力后才能进入池调度。

新增 `account_group_model_rules`：

| 字段 | 说明 |
|---|---|
| `group_id` | 账号池 |
| `public_model` | 对此池可见的模型 |
| `status` | `active` / `disabled` |
| `fallback_group_id` | 可选，当前池没有健康模型资源时才允许按该模型转入的备用池 |
| `version` | 乐观锁 |

账号池没有规则时不额外限制；有规则时只允许 active 规则中的模型。池规则与账号 capability 相交后，调度器才能选择账号。`​fallback_group_id` 只在请求尚未送达上游前使用，绝不重放已发送请求。

### 3.2 模型销售规则

新增 `plan_model_pricing`，主键 `(plan_version_id, model)`：

| 字段 | 说明 |
|---|---|
| `input_per_mtok`, `cached_input_per_mtok`, `output_per_mtok` | 可空；为空时继承冻结的目录价格 |
| `rate_multiplier` | 可空；为空时继承 `plan_versions.rate_multiplier` |
| `pricing_mode` | `inherit`、`unit_override`、`multiplier_override`、`unit_and_multiplier` |
| `notes` | 运营说明 |
| `created_at` | 不可变版本的创建时间 |

语义：先将目录价按字段覆盖为该模型的销售基准，再应用模型倍率；模型倍率存在时替代套餐默认倍率，不与默认倍率相乘。采用“替代”而不是“叠加”可避免套餐倍率改动意外把已有模型特价二次放大。

价格计算公式：

```text
catalog_cost = (uncached_input × catalog_input_price
              + cached_input × catalog_cached_input_price
              + output × catalog_output_price) / 1,000,000

pricing_base_cost = (uncached_input × effective_input_price
                    + cached_input × effective_cached_input_price
                    + output × effective_output_price) / 1,000,000

effective_multiplier = model.rate_multiplier ?? plan.rate_multiplier
charged_cost = pricing_base_cost × effective_multiplier
```

`usage_ledger.base_cost` 继续保存采购目录成本（`catalog_cost`），新增不可变的 `pricing_base_cost` 保存模型规则覆盖后的销售基准成本；`rate_multiplier` 保存最终倍率，`cost` 保存实收。`requests` 新增 `upstream_model`、`plan_version_id`、`model_pricing_source`；现有 `requests.model` 继续保存客户端公开模型名。这样能区分采购成本、售价规则和实收，不会让套餐特价抹掉原始成本证据。

### 3.3 目录与发现缓存

新增 `upstream_model_catalog_snapshots`（可选第二小阶段）：记录管理员手动同步或中转 `/v1/models` 拉取的模型名、账号、时间、错误和原始清洗后摘要。它只帮助配置 UI，不自动授予模型能力，也不会覆盖管理员的 capability 规则。

## 4. 决策链与调度改造

### 4.1 请求前模型解析

在当前模型白名单检查之后、预留之前新增 `ResolveModelRoute`：

1. 标准化客户端 `model`（trim、精确大小写策略；建议目录中统一小写）；
2. 检查当前 API Key 的有效模型交集；
3. 读取所有有权路由的 active 账号池及直接账号；
4. 按池规则、账号 capability 和健康状态筛选候选；
5. 按既有路由优先级、池策略、账号负载选择一个账号；
6. 返回公开模型、上游模型、账号、池、规则版本/来源。

调度 API 从：

```go
AcquireAccountWithSubscription(ctx, keyID, keyLimit, subscriptionID, subscriptionLimit, stickyID)
```

调整为：

```go
AcquireModelRoute(ctx, keyID, keyLimit, subscriptionID, subscriptionLimit, publicModel, stickyID)
```

将模型筛选置于占用并发槽之前，防止不支持该模型的账号被选中后才失败。保持既有“未发送可 failover、已发送不可重放”的出口语义。

### 4.2 上游请求改写

仅改写 JSON payload 顶层 `model` 为 `upstream_model`。原始客户端模型名继续写入 `requests.public_model`/审计事件；审计提取和套餐权限始终基于公开模型名。改写失败或不支持的 capability 配置在发送前返回 `model_route_unavailable`，不创建上游调用。

### 4.3 `/v1/models` 的真值

`GET /v1/models` 不再只是“价格表 ∩ Key 白名单”，而应返回：

```text
目录可精确计价
∩ 当前 Key 权限
∩ 当前有效订阅
∩ 至少一条可达且健康的模型路由
```

接口不应泄露账号、池、上游模型名或售价。可在 admin API 返回受保护的诊断字段。

## 5. 管理 API

### 5.1 目录与预览

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/admin/models` | 激活价格目录模型、基准价格、是否有手工覆盖、搜索/分页 |
| GET | `/api/admin/models/{model}/availability` | 按模型解释可用池、健康账号数量、映射和阻断原因 |
| GET | `/api/admin/models/{model}/price-preview?plan_version_id=` | 返回目录价、套餐默认价、模型规则、最终输入/缓存/输出单价与倍率 |
| POST | `/api/admin/accounts/{id}/models/sync` | 可选；从该兼容中转的 `/v1/models` 拉取建议模型名，不自动启用 |

### 5.2 资源能力

| 方法 | 路径 | 用途 |
|---|---|---|
| GET/POST | `/api/admin/accounts/{id}/model-capabilities` | 管理账号公开模型 → 上游模型映射 |
| PATCH/DELETE | `/api/admin/accounts/{id}/model-capabilities/{public_model}` | 乐观锁更新、禁用或删除 |
| GET/POST | `/api/admin/account-pools/{id}/model-rules` | 管理池白名单与模型级备用池 |
| PATCH/DELETE | `/api/admin/account-pools/{id}/model-rules/{public_model}` | 乐观锁更新、禁用或删除 |

### 5.3 销售规则

模型价格只能随着套餐版本创建或复制：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/api/admin/plans/{id}/versions` | 创建下一不可变版本，同时提交 `model_pricing[]` |
| GET | `/api/admin/plans/{id}/versions/{version}/model-pricing` | 查看该版本模型规则 |
| POST | `/api/admin/plans/{id}/versions/{version}/clone` | 基于当前版本复制并编辑，产生下一版本 |
| POST | `/api/admin/subscriptions/{id}/migrate-plan-version` | 显式把订阅迁到新版本；需要确认并记审计事件 |

禁止直接 PATCH 已发布套餐版本中的模型规则。这样不会在月中改变已有用户的费用定义。

## 6. 管理台体验

### 6.1 模型目录页

新增“模型目录”页，表格列为模型、基准输入/缓存/输出价、目录来源、全局手工覆盖标记、可用池数、可用账号数。支持搜索与“仅显示可路由/仅显示未配置价格”筛选。

模型详情抽屉展示：

```text
公开模型 gpt-5-codex
├─ 基准价格（冻结目录）
├─ 资源可用性
│  ├─ CPA 池 → cpa-gpt-5-codex（3/4 健康账号）
│  └─ Codex OAuth 池 → gpt-5-codex（2/2 健康账号）
└─ 套餐售价预览
   ├─ Pro：目录价 × 1.20
   └─ Team：自定义输入/输出价，× 1.00
```

### 6.2 账号与池编辑器

账号编辑页使用目录检索/多选而不是逗号输入；每一行可配置公开模型、上游模型别名和状态。池编辑页只展示其成员 capability 的并集供选择，并警告“池白名单包含但没有任何健康账号支持”的死规则。

### 6.3 套餐版本编辑器

在创建/复制套餐版本时提供模型规则表：继承目录价、覆盖三项单价、覆盖倍率、恢复继承。每次输入即时调用价格预览，明确显示：

```text
目录输入价 → 销售输入价 → 最终倍率 → 每百万 token 实收
```

任何没有相应模型路由的套餐规则显示警告，但允许保存为草稿；发布时必须阻止或要求明确的“暂不开放该模型”状态。

## 7. 迁移与兼容策略

### Phase A：只读与配置基础

1. 添加 capability、池规则和 plan-version 模型规则表；不修改现有 `accounts`、`price_versions` 行为。
2. 添加 admin 模型目录、可用性和价格预览 API。
3. 所有既有账号没有 capability 时视为 legacy-unrestricted；行为不变。
4. 前端先以只读目录和 dry-run 可用性页上线，管理员可核对 CPA/Sub2API 的真实模型名。

### Phase B：路由强制

1. OpenAI-compatible 新账号要求配置 capability，或显式设置 `legacy_unrestricted=true` 并记录管理员审计事件。
2. 账号池一旦开启模型规则，`/v1/models` 和请求准入同时生效。
3. 提供 `model_route_unavailable` 错误，包含 request_id 和 retryable=false；不暴露内部池名。
4. 先以观测模式记录“若强制会拒绝的请求”，连续 7 天无异常后才允许池级强制开关。

### Phase C：模型级套餐定价

1. 创建新的 `plan_versions` 时允许 model_pricing；已有版本保持默认继承。
2. 所有新请求持久化 `plan_version_id`、公开/上游模型和价格规则来源。
3. 在预留、结算、未知结果人工调整三处复用同一 `ResolveEffectivePrice`；不得出现三套计算实现。
4. 管理台提供套餐版本复制与订阅显式迁移；默认不自动迁移活跃订阅。

### Phase D：复杂价目与公开展示

1. 仅在上游 usage 能可靠提供所需维度后支持长上下文阶梯和固定费用。
2. 添加只读的用户“可用模型/预估价格”页；不显示采购价、账号、池或上游映射。
3. 如需高峰倍率，作为独立、可冻结的 plan-version time-pricing 规则，不能直接复用服务器本地时区。

## 8. 不变量与安全约束

- 所有金额使用 decimal 和 NUMERIC(30,12)，禁止 float。
- 目录缺价、模型规则缺价、usage 不完整时维持 fail-closed：不把未知成本当作零。
- 账号 capability 与池白名单的每次变更调用调度缓存失效；在审核期间仍执行现有 Key fresh re-check，并新增模型路由 fresh re-check。
- 账号/池删除或禁用时 capability 级联清理；计划版本不可删，只能 archived。
- 上游 `/v1/models` 同步视为不可信输入：大小限制、超时、HTTPS/配置允许的私网策略、模型名长度/字符集校验；绝不从上游自动创建价格、映射或权限。
- 管理事件只记录模型名、规则 ID、版本和操作者，绝不记录中转 API Key、OAuth token 或完整上游响应。
- 任何已发送请求仍禁止因模型 fallback 在另一个账号/池重放。

## 9. 测试与验收

### 单元测试

- 模型交集：目录、套餐、订阅、Key、池和账号约束任一拒绝均不可调用。
- 映射：公开模型权限校验通过后才改写为上游模型；未知映射不发送请求。
- 价格优先级：目录继承、三项单价覆盖、模型倍率覆盖套餐倍率、无规则回退。
- decimal 边界：零价允许、负价拒绝、缓存 token 不超过输入 token。
- 新旧账号兼容：legacy-unrestricted 保持当前模型行为；显式 capability 账号严格筛选。

### 集成测试

- `/v1/models` 不返回无可用健康路由模型。
- 同一公开模型在两个池有不同上游映射时，按现有池优先级选择正确 payload `model`。
- 不支持模型在预留和上游前拒绝，账本/上游调用数均为零。
- 不同 plan_version 对同一模型结算出不同 `pricing_base_cost`/`rate_multiplier`/`cost`，同时保留各自的 `base_cost` 采购目录证据；历史账本不因后续规则版本改变。
- 已发送后流断开，模型级备用池不得被使用；资金和账号仍按 unknown 收敛。
- plan version 迁移必须产生管理审计事件，且迁移前后的请求使用不同冻结版本。

### 发布门槛

- `go test ./...`、真实 PostgreSQL 集成测试、`npm run build`、`git diff --check` 全部通过。
- 至少一个 CPA 和一个 Sub2API 中转在观测模式下完成模型清单校对、映射与 Responses SSE 结算验证。
- 对比 7 天观测日志后，确认没有因 capability 规则误排除原可服务请求。

## 10. 建议实施顺序与工作量

| 迭代 | 内容 | 风险 | 估计 |
|---|---|---|---|
| M1 | 模型目录 API、账号 capability、池白名单、可用性 dry-run | 中 | 3–4 天 |
| M2 | 调度按模型筛选、payload 模型映射、`/v1/models` 真值化 | 高 | 3–5 天 |
| M3 | plan-version 模型售价、统一价格解析器、账本快照 | 高 | 4–6 天 |
| M4 | 管理台编辑器、价格预览、订阅版本迁移 | 中 | 3–5 天 |
| M5 | 观测上线、CPA/Sub2API 联调、强制开关与回滚演练 | 中 | 2–3 天 |

总计约 15–23 个工程日；M1/M2 可以优先解决“不能将不支持的模型发给错误中转”的可靠性问题，M3/M4 才是差异化定价能力。

## 11. 明确不在本轮复制的 Sub2API 能力

- 多平台协议转换（Claude/Gemini/Anthropic）；本项目当前仅支持 Responses API。
- 图片、音频、视频、搜索等非 token 维度计价；必须先有可靠 usage 证据。
- 依据价格倒推利润率的自动控制；需先确定本项目“采购成本”和“用户售价”的业务模型。
- 自动从上游模型目录授予模型权限；这是高风险权限扩大，方案只允许同步为管理员建议。

## 12. 参考

- [Sub2API channel model-pricing API](https://github.com/Wei-Shaw/sub2api/blob/main/frontend/src/api/admin/channels.ts)：模型默认价格查询与目录模型同步。
- [Sub2API group service](https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/service/admin_group.go)：资源组倍率、模型定价与白名单归一化/校验。
- [Sub2API model plaza handler](https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/handler/model_plaza_handler.go)：将官方参考价、实收价、长上下文与分时倍率分离展示的方式。
