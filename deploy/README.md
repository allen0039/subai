# 运行手册（部署、运维、备份恢复）

## 首次部署

```bash
cd deploy
cp .env.example .env
# 编辑 .env：SUBAI_MASTER_KEY（openssl rand -hex 32）、SUBAI_DB_PASSWORD、
#            SUBAI_MODERATION_API_KEY、OAuth 三元组（P0 验证后）
docker compose up -d --build
docker compose logs -f server
```

- 迁移与默认规则种子在启动时自动执行；`--migrate-only` 可单独执行。
- 首个管理员：临时设置 `SUBAI_DEV_BOOTSTRAP_ADMIN=admin:强密码` 启动一次，登录后移除该变量并重启。无出厂密码（§22）。
- 数据库与管理 API 默认不暴露公网；HTTPS 由 Caddy 反代提供（SSE 已关闭缓冲、长超时）。
- `production_ready=false` 时数据面对有效 Key 返回 503（明确提示未就绪），管理面可用。

## 日常运维

- 状态页 `#/status`：进行中/unknown 请求计数、账号状态分布；unknown>0 必须人工核对（§18.3）。
- 账号 `recovery_hold` / `reauth_required` 需管理员处理：前者核对账本后手动恢复 active，后者重新 OAuth。
- 日志只含路径级请求记录；正文与 Authorization 永不入日志（§9）。
- 审计事件默认保留 14 天（SUBAI_LOG_RETENTION_DAYS）；账本不跟随审计日志删除。

## 备份

```bash
# 数据库逻辑备份（含全部账本与配置；凭证列为密文，单独依赖主密钥）
docker compose exec db pg_dump -U subai subai | gzip > backup-$(date -u +%Y%m%dT%H%M%SZ).sql.gz
```

**主密钥（SUBAI_MASTER_KEY）必须与数据库备份分开保管**（§22）：丢失主密钥 = 所有账号/代理凭证不可解密，需重新录入。建议：密钥存密码管理器，备份存对象存储。

## 恢复

```bash
docker compose down server
docker compose exec db psql -U subai -c "DROP DATABASE subai;"
docker compose exec db psql -U subai -c "CREATE DATABASE subai;"
gunzip -c backup-xxx.sql.gz | docker compose exec -T db psql -U subai subai
SUBAI_MASTER_KEY=<原主密钥> docker compose up -d server
```

恢复后核对（§10 P6 门槛）：

1. `SELECT state, count(*) FROM requests GROUP BY state;` — 无未预期 unknown 增长。
2. `SELECT sum(spent+reserved) FROM budget_periods;` 与账本 `sum(cost)` 对照。
3. 启动日志出现 startup recovery 行时，按 docs/KNOWN_LIMITATIONS 处理 unknown 请求。

## 升级

```bash
git pull && docker compose up -d --build server
```

迁移只前进不回滚（D-005）；回滚版本前先恢复备份。

## 单活约束（D-007）

第二实例启动会因 `pg_try_advisory_lock` 失败退出——这是预期行为。扩容先升级单实例资源配置；多实例协调属后续阶段。

## 故障速查

| 症状 | 处置 |
|---|---|
| 数据面全部 503 audit_unavailable（提示 not ready） | 配置 SUBAI_MODERATION_API_KEY 并重启 |
| 503 no_healthy_account | 账号状态页检查；代理探测 POST /api/admin/proxies/{id}/test |
| 429 budget_exceeded 频繁 | 核对预算策略/周期快照；额度调整下周期生效（§18.2） |
| 审核队列 429 | 提高队列上限前先确认账户 RPM；吞吐受审核 API 限制 |
