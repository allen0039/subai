# 价格来源（P0-04）

状态：blocked（官方机器可读来源未经实测）。

## 设计（§8、§22）

- 默认来源：`https://developers.openai.com/api/docs/pricing`（`SUBAI_PRICE_SOURCE_URL` 可覆盖）。
- 默认每 24h 检查一次（`SUBAI_PRICE_SYNC_INTERVAL_H`）；成功验证后仅用于新请求；版本不可变。
- 来源失败 → 保留上次有效版本，界面显示过期时间；首次无有效价格不得调用（由 `ActivePriceVersion` + 种子机制保证）。

## 当前实现状态

1. `POST /api/admin/prices/sync` 已实现，但**明确返回 `status: not_verified` 且不激活任何版本**——官方页面解析器未经实测，禁止伪造同步（§23）。
2. 管理员手工价格：`POST /api/admin/prices/overrides` 创建 origin=manual 版本并激活（幂等替换 active）。
3. 种子价格：首启插入 origin=synthetic 版本并激活（D-008），数值为占位示例，界面明确标注；`production_ready` 要求已验证的 official 版本。

## 待实测清单

1. 官方页面是否存在稳定 JSON 数据块/可解析结构；记录源 URL、source_hash 与解析器代码。
2. 抓取频率与缓存礼貌性（ETag/If-Modified-Since）。
3. 新计费档位（长上下文、优先级、批量、图像/工具固定费）出现时的失败策略验证：未知档位严格拒绝，不猜测映射。

## 变更处理

- 价格版本行含 source_url、source_hash、fetched_at、activated_at、origin；审计可追溯每请求使用的版本（requests.price_version_id、usage_ledger.price_version_id）。
- 管理员手动覆盖后，自动同步不覆盖人工值（manual 版本仅在显式 override 时替换）。
