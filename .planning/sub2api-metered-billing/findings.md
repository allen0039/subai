# 实施发现

- Sub2API 当前价格目录默认远程同步，并支持本地 fallback/override；核心费用是原始分项费用乘套餐/用户倍率，再在请求后原子扣余额或累计订阅额度。
- 本项目已有不可变 `price_versions`、Decimal 费用计算、`usage_ledger` 幂等约束、用户订阅和套餐账号池绑定。
- 本项目当前普通请求在上游调用前按估算上界预留金额，响应后以真实 usage 结算；模型价格只包含非缓存输入、缓存输入和输出三项。
- 本项目已有官方 Codex 额度抓取，但额度快照尚未参与账号池调度。
- 基线 `go test ./...` 通过。
- 现有订阅限额已经映射成 `budget_policies` 的 day/week/month 固定额度；可在取消普通请求预留后继续复用这些周期行做请求后累计。
- 远程 `model-price-repo` 当前为模型名到 LiteLLM 字段的 JSON 对象，核心字段 `input_cost_per_token`、`cache_read_input_token_cost`、`output_cost_per_token` 可直接转换为每百万 token 单价。
- 当前 `account_quota_snapshots` 只由管理端刷新与展示，调度器未读取快照；这不属于本轮计价切换的首要闭环。
- 需要允许 `rate_multiplier=0` 表示免费套餐；数据库、管理 API、网关必须统一接受非负倍率。
- metered 模式下套餐额度全部留空应表示不限额：可以创建零个 reservation，仍照常写 usage ledger；strict 模式继续要求至少一条预算策略。
- 手工价格版本需要复制当前完整目录以免其他模型消失，但必须逐行标记真正的手工覆盖项，自动同步时只继承这些覆盖项，不能把复制来的旧目录全部覆盖到新目录。
- 默认目录的 JSON 与 SHA-256 sidecar 已于 2026-09-14 实际下载比对一致；自定义目录未配置自定义 hash 时不会误用默认仓库的 sidecar。
