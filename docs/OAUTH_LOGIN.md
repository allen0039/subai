# Codex OAuth 登录

上游账号页点击「OAuth 接入」→「打开授权页」，在 OpenAI 页面登录。
浏览器跳转到 `http://localhost:1455/auth/callback?...` 后，如果显示无法连接，
复制地址栏的完整 URL，回到 SubAI 粘贴并点击「完成授权」，然后刷新账号列表。
回调 URL 含一次性授权码，只应提交到自己的 SubAI 管理界面，不要分享或写入日志。

远程部署采用 CPA 支持的手工回调方式，不需要服务器开放 1455 端口。
本次没有实现设备码登录。

## 已有部署更新

更新代码后重新构建 server 镜像。旧 `.env` 会覆盖代码默认值，必须检查并更新：

```dotenv
SUBAI_OAUTH_AUTHORIZE_URL=https://auth.openai.com/oauth/authorize
SUBAI_OAUTH_TOKEN_URL=https://auth.openai.com/oauth/token
SUBAI_OAUTH_CLIENT_ID=app_EMoamEEZ73f0CkXaXp7hrann
SUBAI_OAUTH_REDIRECT_URI=http://localhost:1455/auth/callback
```

在实际使用的 Compose 目录执行 `docker compose up -d --build server`。
更新后重新点击「OAuth 接入」，不要复用更新前生成的授权链接。
上述客户端 ID 是公开标识；回调地址必须与该客户端的登记信息匹配。

## 验证范围

自动测试使用隔离 PostgreSQL 和模拟 token 服务，覆盖授权参数、JWT 身份提取、
会话匹配、回调重放拒绝、账号关联、刷新保留身份以及过期会话重试。
真实 OpenAI 登录仍需管理员在浏览器中完成验收。
