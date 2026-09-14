# 模型价格目录

SubAI 默认使用与 Sub2API/LiteLLM 字段兼容的远程 JSON 价格目录：

`https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json`

服务启动时同步一次，此后按 `SUBAI_PRICE_SYNC_INTERVAL_MIN`（默认 60 分钟）检查。也可以通过 `POST /api/admin/prices/sync` 手动触发。下载或解析失败时保留最后一个有效版本；如果从未有可用价格版本，数据面保持不可用。

## 版本与校验

- `SUBAI_PRICE_SOURCE_URL`：价格目录地址，必须是 HTTP(S)。
- `SUBAI_PRICE_HASH_URL`：可选 SHA-256 sidecar；非空时摘要不一致会拒绝导入。
- 每次内容 hash 变化都会创建不可变 `origin=catalog` 版本，并在同一数据库事务中激活。
- 请求会冻结 `price_version_id`，因此同步只影响新请求；账本可追溯当时使用的版本。
- 目录的 `*_cost_per_token` 会精确转换成数据库中的每百万 token 价格，不使用浮点累计。

## 自动发现新模型

只要上游目录新增了包含可支持价格字段的模型，下一次同步就会导入并激活，无需发布 SubAI。这里的“自动”依赖远程目录先更新，并不代表从 OpenAI 接口实时查询价格。

当前闭环支持：

- `input_cost_per_token`
- `cache_read_input_token_cost`
- `output_cost_per_token`

长上下文阶梯、service tier、图片 token、cache write 和其他固定费用尚未映射；不能可靠计价的模型或维度仍严格拒绝，绝不默认为免费。

## 手工覆盖

`POST /api/admin/prices/overrides` 会复制当前完整目录、更新管理员提交的模型并激活新的不可变 `origin=manual` 版本。每行保存是否为真正的手工覆盖：后续自动同步只继承这些被修改的模型，其他模型继续采用最新目录价格，新模型也能正常加入。
