# 计费模式与费用边界

SubAI 支持两种计费模式，由 `SUBAI_BILLING_MODE` 选择。默认是更接近 Sub2API 的 `metered`；原有 `strict_reservation` 作为需要请求前硬上界的兼容模式保留。

## metered（默认）

请求前只检查已有周期额度是否已经耗尽，不按估算 token 冻结金额。上游返回终端 usage 后，系统按真实 token 计算费用，通过 `(request_id, attempt_id, entry_type)` 幂等写账，并原子累加所有适用的日/周/月预算周期。

套餐的日、周、月额度都留空表示不限额：请求仍会写 usage ledger，但不会创建预算 reservation。`rate_multiplier=0` 表示免费套餐，基础成本仍保存在账本中，用户额度消耗为 0。

这种模式允许多个并发请求在结算时小幅超过周期额度；已经达到或超过额度后，后续请求会被拒绝。这是请求后真实计量换来的明确取舍。

## strict_reservation（兼容）

请求前按输入估算和输出上限计算候选费用，在所有适用预算周期中原子预留；没有预算策略时拒绝请求。上游返回 usage 后按真实费用结算，实际费用超过预留时如实记账，并将账号置为 recovery hold 等待核查。

- 输出上界：注入 `max_output_tokens = min(客户端值, SUBAI_OUTPUT_BOUND)`，默认 4096。
- 输入上界：`ceil(utf8_bytes/3) × 1.5`，不运行 tokenizer。
- 金额使用 `NUMERIC(30,12)` 和 Decimal 运算。

## 费用公式

```text
base_cost = (
  uncached_input × input_rate
  + cached_input × cached_rate
  + output × output_rate
) / 1,000,000 + supported_fixed_fees

subscription_cost = base_cost × plan_rate_multiplier
```

`uncached_input = input_total − cached_input`，仅在 usage 字段完整、非负且 `cached_input <= input_total` 时成立。模型价格版本与套餐倍率都在请求上冻结，价格同步或套餐改版不会改变进行中请求。

## 保守失败语义

- 未知模型或缺少价格：请求前拒绝，不按 0 价处理。
- usage 缺失或不一致：请求置 `unknown`，账号进入 `unknown_pending` hold，不自动退款，也不记作 0 token。
- 未映射费用维度：拒绝计价；管理员可依据证据走 `billing.AdjustUnknown` 补账。
- 图片 token、cache write、service tier 和长上下文阶梯尚未接入本轮闭环。
