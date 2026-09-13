# 第六轮审查修复报告（2026-09-13）

本次按 `INDEPENDENT_REVIEW_ROUND6_2026-09-13.md` 修复了可在本地和合成服务中验证的问题。没有使用真实 Codex 账号、OAuth 凭据或 Moderation Platform Key。

## 已修复

- unknown 人工处置路由现在接受网关生成的 opaque request ID，并且只接受 `POST`。
- OAuth 会话可绑定已有账号；回调会更新同一账号的凭据和版本，清理 `reauth_required` hold，同时保留其他 hold 和人为暂停状态。
- 审核规则编辑以数据库行 UUID 定位，使用乐观锁创建新版本，并通过完整规则编译校验。管理界面已提供编辑字段。
- 审核例外已成为可执行的精确豁免：只对同一 API Key、内容 HMAC 和规则 ID 生效，要求该规则命中过原事件且未过期；secret 分类不能豁免。
- `response.failed` 在确认 usage 后仍会结算，但请求状态为 `failed_after_dispatch`，不会被写成 `completed`。
- 配置加载会拒绝负重试、无效并发/容量及相互矛盾的超时，避免审核客户端因错误配置进入 panic 路径。
- 固定费用仅接受显式支持的维度；未知维度、负数和非法结构会使模型价格不可用，避免低估预留。
- 管理路由补充了 mutation 的方法限制，修复了原测试中的 UUID、HTTP body、价格单位、reservation 状态和重复会话问题。
- 管理登录仅通过 HttpOnly cookie 保持会话；前端不再持久化 token 或设置 Bearer。服务端和 Caddy 增加 CSP、禁止嵌入、nosniff 与 referrer 策略。
- `PerAccountConcurrency` 现在作为账号自身并发限制的全局上限参与调度。

## 新增回归覆盖

- `TestR6OAuthReuseUpdatesOriginalAccount`
- `TestR6AuditExceptionIsExactAndApplied`
- `TestR6FailedTerminalSettlesWithoutCompleting`
- `TestR6AuditRulePatchUsesRowIDAndValidates`
- 固定费用未知维度与配置边界的单元测试

## 实际验证

隔离 PostgreSQL 16（容器已清理）执行结果：

| 命令 | 结果 |
|---|---|
| `TEST_DATABASE_URL=… go test ./tests/integration -count=1` | 通过 |
| `go test -race ./internal/... ./rules/...` | 通过 |
| `npm --prefix web run build` | 通过 |
| `make lint` | 通过 |
| `go build -o /tmp/subai-repair6-server ./cmd/server` | 通过 |
| `docker build -f deploy/Dockerfile -t subai-repair6 .` | 通过 |

## 仍需真实环境验证

官方 Codex OAuth/刷新、真实上游 SSE 协议、真实 Moderation 行为与官方价格页面解析仍需要对应凭据和受控生产验证。价格同步在解析器未经验证前继续明确返回 `not_verified`，不会伪造已同步或自动激活价格。
