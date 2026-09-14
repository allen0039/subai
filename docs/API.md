# SubAI API 参考

完整请求/响应 schema 以代码为准（internal/admin、internal/gateway）。本文列出全部端点、方法与关键约束。

## 数据面（§17.1）

认证：`Authorization: Bearer <gateway-key>`。该 Key 仅对本网关有效，不是 OpenAI 官方 Key；入站 Authorization 不透传上游。

| 端点 | 方法 | 说明 |
|---|---|---|
| /v1/responses | POST | 首要数据面端点。SSE 流式转发；完整生命周期见计划 §19 状态机 |
| /v1/models | GET | 返回当前激活价格版本内、该 Key 有权使用的模型 |
| /v1/chat/completions | POST | 第一版不支持（422 unsupported_transport，D-010） |
| /v1/*（Upgrade: websocket） | GET | 第一版不支持（422 unsupported_transport，D-010） |

### /v1/responses 生命周期

鉴权 → 请求大小限制（默认 8MiB，读取过程强制）→ 路由检查（无路由 403）→ JSON 深度/字段检查 → AuditDocument 提取（§21）→ 本地规则 → 审核缓存 → 公平队列 → 官方 Moderation → 重查 Key → 账号选择 → 原子预算预留 → 双层并发槽 → 出口策略 dispatch → SSE 转发（注入 `max_output_tokens` 上界，D-001）→ 幂等结算。

请求头：`X-Client-Request-Id`（幂等/追踪用，可选）、`X-Session-Id`（会话黏性，可选）。Host/Forwarded 等可改变路由的头被过滤，不透传。

### 错误映射

统一错误体：`{"error":{"code","message","request_id","retryable","retry_after_seconds?","reset_at?"}}`。

| HTTP | code | 场景 |
|---|---|---|
| 401 | invalid_api_key | 无效/过期/撤销 Key；成员被禁用 |
| 403 | access_denied | 无路由、模型不在允许列表 |
| 400 | audit_blocked | 本地规则 block/review 命中或官方 flagged |
| 413 | request_too_large | 超过请求体上限 |
| 422 | audit_unsupported | 审核无法访问或格式不支持（含 previous_response_id 严格拒绝） |
| 422 | unsupported_transport | chat/completions、WebSocket（D-010） |
| 429 | audit_queue_full | 审核队列满/等待超时（带 Retry-After） |
| 429 | concurrency_exceeded | Key 或账号并发已满（带 Retry-After） |
| 429 | budget_exceeded | 周期预算不足（带 reset_at 提示） |
| 503 | audit_unavailable | 审核不可用（fail-closed，不调用上游）；含网关未就绪 |
| 503 | no_healthy_account | 无可用上游账号 |
| 502 | upstream_error | 上游异常（流开始前） |

流开始后错误以 SSE `event: error` 传递，不伪造成功响应。

## 管理面（§17.2，前缀 /api/admin）

会话：cookie（HttpOnly/Secure/SameSite=Strict）或 `Authorization: Bearer <session>`。登录限流：15 分钟内 10 次失败锁定。

| 资源 | 端点 |
|---|---|
| 会话 | POST/DELETE/GET /session |
| 成员 | GET/POST /members；PATCH /members/{id} |
| 客户端 | GET/POST /clients；PATCH /clients/{id} |
| Key | GET/POST /keys；PATCH /keys/{id}；POST /keys/{id}/revoke；GET/POST/DELETE /keys/{id}/routes |
| OAuth | POST /accounts/oauth/sessions；GET /accounts/oauth/sessions/{id}；公开回调 GET /api/oauth/callback |
| 账号 | GET/POST /accounts；PATCH /accounts/{id}（状态机：active/paused/reauth_required/quota_exhausted/recovery_hold）；GET/POST/DELETE /accounts/{id}/model-capabilities |
| 账号组 | GET/POST /groups；POST /groups/{id}（body: account_id, weight）；GET/POST/DELETE /account-pools/{id}/model-rules |
| 代理 | GET/POST /proxies；PATCH /proxies/{id}；POST /proxies/{id}/test（服务端固定探测目标） |
| 出口策略 | GET/POST /egress-policies |
| 预算 | GET/POST /budget-policies；GET /budget-periods；GET /ledger |
| 价格 | GET /prices/versions；GET /prices/active/models；POST /prices/sync；POST /prices/overrides |
| 审核 | GET /audit/rules；PATCH /audit/rules/{rule_id}（生成新版本）；POST /audit/rules/validate（仅本地匹配）；GET /audit/events；POST /audit/events/{id}/review |
| 系统 | GET /status；GET /dashboard?range=24h\|7d；GET /events?since=<RFC3339>；GET /search?q=<2–80 字符>&types=user,key,account,pool；GET /admin-events |
| 个人概览 | GET /me/overview（当前成员的订阅、Key、最近使用时间和近 7 日已记账成本；不返回账号、代理或出口信息） |

通用约束：

- 写接口提交 `version`（乐观锁），冲突返回 409。
- 分页默认 limit=20、最大 100。
- 秘密只在创建响应出现一次；列表与详情永不回显；admin_events 中秘密字段脱敏为 [redacted]。
- 预算 percent 模式校验 base_policy_id 必须为同周期 fixed 策略（防循环）。
- 审核规则修改生成新版本行，历史保留；恢复默认=由种子逻辑重新补入。
- 复核接口只记录结论或创建带 TTL 的精确例外，从不重放请求。
- 仪表盘和事件流均为有界聚合/增量读取，不能替代账本明细；全局搜索仅支持前缀匹配，并且不会返回任何凭据字段。

### 模型能力与精细计价

- `POST /accounts/{id}/model-capabilities`：`public_model` 是对客户暴露的模型名，`upstream_model` 是实际转发名；`status` 可为 `active|disabled`。一个账号一旦配置了任意能力行，就只会处理明确启用的模型。`DELETE` 使用 `?model=<public_model>`。
- `POST /account-pools/{id}/model-rules`：为账号池逐模型启停；未配置规则的池保持兼容模式。`DELETE` 使用同样的 `?model=`。
- 创建套餐或套餐版本时可带 `model_pricing` 数组。每一项至少给出 `input_per_mtok`、`cached_input_per_mtok`、`output_per_mtok` 或 `rate_multiplier` 之一；未给出的字段继承冻结的价格目录。模型规则的倍率会替代套餐默认倍率。
- 请求会冻结实际使用的上游模型、套餐版本和价格来源。账本同时保存目录基础成本 `base_cost` 与套餐模型规则后的 `pricing_base_cost`，因此后续目录或套餐变更不会重写历史结算。

### 已反代上游（CPA / Sub2API 等）

创建上游账号时可将 `provider` 设为 `openai_compatible`，并同时提供：

- `upstream_base_url`：中转的 `/v1` 基地址或完整 `/responses` 地址，例如 `http://cpa.internal:8317/v1`；
- `access_token`：该中转的 Bearer API Key（加密保存，不在列表或审计事件中回显）。

此类账号和 Codex OAuth 账号一样可加入账号池、设定权重/优先级和出口策略。请求会发送到该账号专属端点的 `/responses`，只携带该账号的 Bearer Key；不会把 ChatGPT OAuth 专用的 `chatgpt-account-id` 头发送给中转。兼容中转须支持 OpenAI Responses 的 SSE 事件，并在终态 `response.completed` 或 `response.failed` 中返回标准 `usage`，否则本项目会按未知结果保护性冻结该账号与对应结算。
