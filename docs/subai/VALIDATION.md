# 整体替换验证记录

日期：2026-09-14。代码位置：`codex/sub2api-rebase` worktree。旧库和线上服务未修改；未推送或发布。

## 已通过

| 检查 | 结果 |
|---|---|
| 固定上游源码完整性 | 原仓库 3919 个跟踪文件均存在；逐文件哈希及定制差异见 IMPORT_AUDIT.json |
| 未修改上游基线 | OpenAI pkg/service/repository/admin 四个包测试和前端生产构建通过 |
| Go 单元 | `go test -tags=unit ./...` 通过 |
| Go 集成 | `go test -tags=integration ./...` 通过，包含真实 Docker 数据库测试 |
| GPT 并发专项 | `go test -race ./internal/service ./internal/repository ./internal/handler/admin -run '^TestSubAI|^TestOpenAIOAuthService_ExchangeCode' -count=1` 通过 |
| 后端 lint | 上游指定 golangci-lint v2.13.0：0 issues |
| 依赖注入生成 | `go generate ./cmd/server` 成功；生成结果只变化两处 OAuth 依赖传递 |
| 前端静态检查 | `pnpm@9 run typecheck`、`pnpm@9 run lint:check` 通过 |
| 前端完整测试 | 276 个测试文件、2098 项测试全部通过 |
| 构建 | 上游 Dockerfile 构建自有 `subai-sub2api:local` 镜像成功，包含内嵌 Vue 前端；本机验证架构 linux/arm64 |
| Compose | 定制配置解析通过；独立 PostgreSQL、Redis、应用启动并健康；全新数据库初始化成功 |
| API 冒烟 | 登录后 accounts/users/groups/proxies/subscriptions 返回 200；旧 OAuth 会话创建/读取可用，错 state 返回 400；原生授权 URL 含原 SubAI 参数 |
| 浏览器 | 管理员登录、账号列表、OpenAI 添加向导可打开；完整回调 URL 提交到服务端，错误 state 被拒绝；无页面 JavaScript 异常 |
| Redis 重启恢复 | 创建真实 Redis OAuth 会话后重启应用，原会话仍为 pending |
| 自定义差异空白检查 | 对照上游逐个检查定制文件，无新增空白问题 |

## 专项覆盖

- 随机 state、PKCE S256、原 48 字节 verifier、10 分钟过期、原 scope/prompt 和可配置 OAuth 地址。
- 错误 host/path/state、缺失或重复参数、fragment、提供方拒绝、跨管理员访问、过期和重复回填。
- Redis 跨实例并发兑换仅成功一次，服务重启保留会话，Redis 失败时不回退本地会话。
- 不确定的 token 兑换失败不能重复兑换同一会话；需要重新发起授权。
- 原兼容接口创建账号、重新授权、查询状态、公开回调；复用上游账号模型且不在兼容完成响应中返回 token。
- 刷新响应缺少 refresh_token/id_token/账号标识/email 时保留既有必要凭证。
- 前端保留完整回调，不从缓存 state 拼接回调，不接受裸 code。
- 标准 HTTP token 请求、不附加上游原有 originator 指纹、无额外 refresh scope、错误响应不携带提供方原始 body。

## 上游原有测试问题

全量前端初次运行有两项失败，均在未修改上游源码中复现：

1. `ChannelMonitorView.grok.spec.ts` 固定断言 8 个供应商，但当前目录已有 10 个。改为核对目录长度及所有供应商按钮存在，保留后续 Grok 默认值断言。
2. `GroupsView.codexManifest.spec.ts` 未初始化 Pinia，页面新增 auth store 后夹具无法挂载。补充每次测试的 Pinia 初始化，原交互断言保留。

仅修正测试夹具，没有因此更改两个页面的产品逻辑。上游存在大量原始尾随空格，整树导入相对旧 SubAI 的 `git diff --check` 会报告这些既有问题；为保留上游完整内容，未大范围格式化。自定义补丁单独核对通过。

## 尚未验证 / 未执行

- 未使用真实 GPT 账号完成 OpenAI 登录、实际 token 轮换及刷新后的真实模型调用。模拟提供方与页面验证不替代真实联调。
- 未验证其他供应商、支付、邮件和外部插件的真实服务配置。功能源码和接口完整保留。
- 未执行旧库迁移，用户、API Key、额度、订阅与历史数据的保留范围仍需确定。测试实例为空库和虚构管理员。
- 未发布或切换线上流量，未在正式环境执行备份/回滚演练；linux/amd64 由提供的多架构发布工作流后续构建验证。

浏览器截图与本地 API 检查结果保存在本 worktree 的 `.planning/qa/`。测试账号口令及会话仅存于临时文件，不进 Git 或镜像。

本地测试实例及其专用数据库/Redis 卷已清理，临时管理员口令和浏览器会话文件已删除；构建好的本地镜像保留。最终镜像 ID：`sha256:f1bbb831e8996e0baaf7532aa6631d690984f1930db9ede20fdc5fa277d9b90b`。
