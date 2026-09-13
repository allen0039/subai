# SubAI 独立网关

个人/受控成员 AI 网关：多 Codex 账号池、独立 API Key、内部美元预算、双层并发、每账号出口隔离、前置审核（本地规则 + 官方 Moderation）。

**当前状态**：第一版实现可用于开发和受控验证；上游 Codex、OAuth 与 Moderation 的真实凭证联调尚未完成，因此不应将本项目直接用于生产流量。详见 [已知限制](docs/KNOWN_LIMITATIONS.md) 与 [兼容矩阵](docs/COMPATIBILITY.md)。

## 架构

```
Codex/Hermes → 鉴权/限流/预算检查 → 内容提取 → 本地规则 → 审核缓存
  → 公平队列 → 官方 Moderation → 账号选择 → 原子预算预留 + 并发槽
  → 出口策略（代理/直连，故障默认停用）→ 上游 SSE → 幂等结算
```

单体 Go 服务 + PostgreSQL；管理界面 React + TS + Vite；Docker Compose 单实例部署（数据库单活锁，第二实例拒绝启动）。

## 快速开始

```bash
# 本地开发（需要 PostgreSQL 与主密钥）
export SUBAI_MASTER_KEY=$(openssl rand -hex 32)
make dev            # 默认 :8080；SUBAI_DEV_BOOTSTRAP_ADMIN=admin:pass 创建首个管理员
make dev-web        # 管理界面开发服务器 :5173（代理 API）

# 生产部署
cd deploy && cp .env.example .env && docker compose up -d --build
```

## 常用命令

| 命令 | 说明 |
|---|---|
| `make lint` | go vet + gofmt |
| `make test` | 单元测试（规则正反例、提取器、缓存键） |
| `make test-integration` | 集成测试（自动起 dockerized PostgreSQL；13 项含上游零调用断言） |
| `make build` | 构建服务器二进制 + 管理界面 |
| `make migrate` | 仅执行迁移与种子 |
| `make compose-up` | Compose 部署 |

## 目录

`cmd/server`、`internal/{auth,accounts,egress,gateway,audit,billing,scheduler,admin,storage,config}`、`migrations/`、`rules/defaults/`、`tests/{fixtures,integration}/`、`web/`、`deploy/`、`docs/`。

## 关键文档

- [docs/API.md](docs/API.md) — 数据面与管理面端点、错误映射
- [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) — 兼容矩阵（verified/mock_only/unsupported/pending）
- [docs/KNOWN_LIMITATIONS.md](docs/KNOWN_LIMITATIONS.md) — 已知限制
- [deploy/README.md](deploy/README.md) — 运行、备份恢复手册
