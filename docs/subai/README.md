# SubAI / sub2api 整体替换

本分支完整采用 sub2api `bdb42e22f81fcb633ff0a060961211dd2bcb515b`。旧项目基线是 `1b6d901f75da6046c096fa5909d8a2cab0357c79`，可通过 Git 历史取回；没有运行旧 React 页面、旧网关或旧计费引擎。

## 唯一定制：GPT 上游登录

- 采用旧 SubAI 的 48 字节 base64url PKCE verifier、32 字节 state、10 分钟会话和 `prompt=login` 等授权参数。
- 保留标准 Go HTTP 表单请求、20 秒超时、原 OAuth 配置变量；通过 sub2api 的代理配置连接提供方。
- 管理台保留完整 localhost 回调 URL，由服务器验证地址、code 和实际回调中的 state。localhost 页面不需要成功打开。
- 生产会话使用 Redis，重启/多实例可继续；绑定发起管理员；兑换前原子消费，网络结果不确定时必须重新发起授权，避免重复兑换。
- 原 `/api/admin/accounts/oauth/sessions` 的创建、查询、`/{id}/callback` 提交接口保留，采用 sub2api 管理员 JWT/API Key 鉴权与原数据字段；重新授权的 `reuse_account_id` 是新系统数值 ID 的字符串，不再是旧 UUID。
- 上游 `/api/v1/admin/openai/*` 授权入口委托同一个授权服务。Vue 页面沿用上游账号创建/编辑契约；受管理员保护的原生兑换接口仍返回用于账号保存的凭证，不向普通用户暴露。旧兼容完成接口由服务器保存账号，仅返回 ID 和状态。
- 不再启动旧 OAuth Manager、旧后台刷新任务或旧 account_holds 状态机。账号存储、缓存、额度、调度、刷新任务和其他平台功能属于 sub2api。
- 默认使用完整回调 URL 回填；旧 `/api/oauth/callback` 自动回调入口也保留，通过随机、限时 state 查找原始会话，与手动回填共用同一消费和落库逻辑。只有原兼容会话可通过此入口完成；提供方无需携带管理员 JWT。

配置变量保持：`SUBAI_OAUTH_AUTHORIZE_URL`、`SUBAI_OAUTH_TOKEN_URL`、`SUBAI_OAUTH_CLIENT_ID`、`SUBAI_OAUTH_REDIRECT_URI`。不要在前端或日志中放置 access/refresh token。

## 本地启动与构建

前端使用上游指定的 pnpm 9，避免其他主版本误判锁文件：

```sh
npx --yes pnpm@9 --dir frontend install --frozen-lockfile
npx --yes pnpm@9 --dir frontend run build
cd backend
go build -tags embed -o bin/server ./cmd/server
```

使用带本项目补丁的自有镜像，独立新库和卷：

```sh
cp deploy/.env.subai.example deploy/.env.subai
# 编辑 deploy/.env.subai 的数据库、Redis、管理员密码和 JWT/TOTP 密钥
# 默认只绑定 127.0.0.1:18081

docker compose --env-file deploy/.env.subai -f deploy/docker-compose.subai.yml up -d --build
```

`docker-compose.subai.yml` 保留上游全部服务环境配置，使用独立 Compose 项目 `subai-v2`、独立卷和自动容器名。不得把 DATABASE_* 指向旧 SubAI 数据库，也不要复用旧数据库卷。生产镜像使用固定 SHA 标签，构建入口是根目录 Dockerfile；`.github/workflows/subai-image.yml` 可手动发布自有 Docker Hub 镜像，不会自动更新服务器。

上游提供的其他 Compose 示例原样保留作为参考；其中官方镜像不包含 GPT 补丁。内置二进制升级来源改为本项目 `allen0039/subai`，避免升级时覆盖补丁；发布版本时保持上游二进制命名和校验和格式。Docker 部署应始终通过自有镜像升级。

## 数据与发布边界

当前代码工作不接触旧数据库或线上服务。旧用户、Key、余额、订阅与历史账单不能通过执行上游 SQL 直接升级。数据保留范围尚待确定；新库初始化不等于旧数据迁移完成。真实 GPT 登录/刷新、其他供应商、支付和邮件联调需要对应账号与配置，模拟测试不替代这些验证。

上线前停止旧实例接单及账号刷新，核对在途请求和最终账单，再迁入凭证/配置并切流。回滚时若新系统已经刷新凭证或产生消费，必须同步最新凭证并核对新增账单，不能只换镜像或恢复旧快照。

## 完整性和维护

- `UPSTREAM.json`：上下游基线。
- `UPSTREAM_FILES.json`：全部上游跟踪文件 SHA256。
- `IMPORT_AUDIT.json`：缺失、变更清单和页面/路由/服务/迁移清单。
- `VALIDATION.md`：本轮检查结果与未验证项目。

```sh
python3 tools/subai_upstream_audit.py > docs/subai/IMPORT_AUDIT.json
```

升级上游时重新固定 SHA、导入完整文件，再重放 GPT 授权及部署适配补丁。所有原始 LICENSE、版权和来源保留；分发按原许可履行要求。

## SubAI 控制台精简（2026-09-15）

按部署方要求移除首次引导、运营合规确认门槛、登录条款、模型广场以及 LinuxDo、微信、钉钉、OIDC、GitHub、Google 平台第三方登录入口和路由。保留邮箱密码登录及安全验证、原有 GPT 上游 OAuth、独立的微信支付授权。相关公开功能标记固定关闭，旧设置不会重新启用入口。上游许可和版权文件仍保留。
