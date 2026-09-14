# 兼容矩阵（P0-01）

状态定义（§17.1）：`verified`（真实客户端实测）、`mock_only`（仅合成上游验证）、`unsupported`（明确不支持）、`pending`（待真实凭证验证）。
本表是 P0 交付框架；没有真实凭证前，所有真实联调行为 `pending` 或 `mock_only`，不得宣称 verified。

## 客户端与调用形式

| 能力 | 状态 | 证据 | 备注 |
|---|---|---|---|
| POST /v1/responses（非流式字段解析） | mock_only | tests/integration TestHappyPathSettlesLedger | 合成上游 SSE |
| /v1/responses SSE 流式透传 | mock_only | 同上（event passthrough 断言） | 真实上游事件 schema 待 P0-01 |
| /v1/responses usage 捕获与结算 | mock_only | 同上（账本 0.000682 精确断言） | 真实 usage 字段名待 P0-01 |
| CPA / Sub2API 等 OpenAI-compatible Responses 中转 | mock_only | internal/gateway TestDispatchOpenAICompatibleRelay | 可按账号设置 `/v1` 端点与独立 Bearer Key；真实中转联调待补录 |
| previous_response_id | unsupported（严格拒绝） | internal/audit TestExtractResponsesUnsupportedHistory | §6.1；网关不可见上下文 |
| item_reference（服务端历史） | unsupported | extract.go | 同上 |
| 工具定义/调用参数/工具输出提取 | mock_only | TestExtractResponsesFullCoverage | 受限深度 JSON 字符串提取 |
| 图片输入（input_image） | pending | — | Moderation 图片能力未实测；当前一律 unsupported，不按零分放行（§5） |
| POST /v1/chat/completions | unsupported | e2e curl 断言 422 | D-010；Hermes 需求确认后再实现 |
| WebSocket | unsupported | — | D-010 |
| 客户端取消 | pending | — | 语义已实现（pre-dispatch 释放/post-dispatch unknown），真实客户端断开待测 |
| 幂等键（X-Client-Request-Id） | pending | — | 存储已建列；重放语义待 §19 全量实现评估 |
| Codex 桌面端直连行为 | pending | — | 不经过网关，无法审计（§2.2） |

## 已验证客户端版本

无。首批三台电脑 + 两个 Hermes 的软件版本需在真实联调后补录（§11 客户端用例）。

## 真实联调开关

全部测试默认只使用合成上游/合成审核（tests/integration）。真实联调需要：

1. `SUBAI_UPSTREAM_BASE_URL` 指向经 P0-01 验证的上游端点；
2. `SUBAI_MODERATION_API_KEY` 配置真实 Platform Key；
3. OAuth 端点三元组（authorize/token/client_id/redirect）经验证；
4. 更新本表状态并附测试 ID 与脱敏样本。
