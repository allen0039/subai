# 费用上界（P0-02）

规格：§2.6、§18.1、§18.3。严格预算要求每个可计费调用形式都有可证明的费用上界。

## 第一版实现的上界机制

1. **输出上界**：dispatch 前注入 `max_output_tokens = min(客户端值, SUBAI_OUTPUT_BOUND，默认 4096)`（D-001）。
   - 待验证：上游是否在所有工具调用形式下尊重该参数（P0-02 blocked）。验证前该行为标记 pending。
2. **输入上界**：`ceil(utf8_bytes/3) × 1.5` 保守估算（D-002），不做 tokenizer。
3. **预留**：`RoundUpReservation(input_ub×input_rate + output_ub×output_rate)`，按激活价格版本冻结计算（§18.1）。
4. **结算**：上游 usage 真实值，幂等键 (request_id, attempt_id)。`actual > reservation` 时如实记账（不截断）、置账号 recovery_hold、写 admin_events 告警（§18.3）。

## 计费公式（§18.1）

```
cost = (uncached_input × input_rate + cached_input × cached_rate + output × output_rate) / 1,000,000 + supported_fixed_fees
```

- `uncached_input = input_total − cached` 仅当 usage 明确包含缓存时成立；字段缺失/负数/不一致不接受为 0。
- 固定费用只在 `fixed_fees.supported` 显式映射时计入；出现未知费用维度 → 该模型严格模式拒绝（`audit_unsupported: model has no price mapping` 或 FORMAT-003 路径）。
- 金额 NUMERIC(30,12)，shopspring decimal，无浮点累计。

## 逐模型/模式上界状态

| 调用形式 | 上界可证明？ | 说明 |
|---|---|---|
| /v1/responses 纯文本+工具（max_output_tokens 尊重） | 设计上可证明 | 待 P0-02 实测注入有效性 |
| 上游忽略 max_output_tokens 的形式 | 不可证明 | 不进入严格预算支持清单（§2.6）；实测发现即告警+暂停 |
| 图片输入 | 待定 | 价格档位未映射，当前被提取器/审核拒绝 |
| 推理 token | 待定 | 若 output 已含推理 token 则不重复计费；字段语义待 P0-01 |

## 缺失用量处理（§23 禁止条款）

usage 缺失 → 请求置 `unknown`，预留保留、账号 recovery_hold；不自动退款、不当 0 处理。人工证据核对通过 `billing.AdjustUnknown` 追加 adjustment 账本行。

## 实测记录

无（blocked：无真实账号）。取得凭证后按模型逐项实测 max_output_tokens 有效性并回填本文件。
