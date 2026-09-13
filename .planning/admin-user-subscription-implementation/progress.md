# 进度记录

## 2026-09-13

- 用户批准按已交付方案开始实施。
- 已重新阅读 `planning-with-files` 和 `frontend-design` 指引。
- 已建立独立实施计划，不覆盖方案编制记录。
- 基线：`go test ./...` 通过；`npm --prefix web run build` 通过。
- 新增 `migrations/0009_user_subscriptions.sql`，建立套餐、版本、订阅、账号池绑定、用户额外授权及订阅账本基础。
- 将认证服务扩展为通用成员会话身份，并添加密码变更后的全量/其他会话撤销能力。
- 实现管理员套餐草稿/发布/版本、用户订阅分配与额外账号/账号池授权接口。
- 实现普通用户的“我的订阅”“我的 Key”“修改密码”接口，创建 Key 强制绑定有效订阅并校验额度边界。
- 调度器改为动态解析套餐池与用户授权，旧 Key 仍回退至 `key_routes`。
- 新增订阅级并发槽、订阅账本归属和月度预算窗口；`go test ./...` 通过。
- 完成管理端“用户管理、套餐管理、订阅分配、上游账号、账号池”导航；移除客户端入口。用户端只保留概览、订阅、Key 与账户安全。
- 用户管理页可直接打开权益页，为指定用户追加/撤销单个上游账号或账号池；订阅仍是用户可选创建 Key 的主授权来源。
- 新增订阅动态路由集成测试，覆盖套餐账号池路由、请求订阅归属及订阅暂停后立即拒绝请求。
- 最终验证：`go test ./...`、`npm --prefix web run build`、以及带 PostgreSQL 的 `TEST_DATABASE_URL=… go test ./tests/integration -count=1` 全部通过。
- 账号池策略已接入调度：普通池按轮询，权重池按实时负载/成员权重分摊，优先级池在高优先级成员不可用或已满时自动回退。

## 2026-09-13 — Oracle3 再次更新

- 用户授权直接更新，不创建备份。
- 已确认 GitHub `main` 最新提交为 `7809b510dcd8ae28eedc075d0b4c247692502fcb`；升级前 Oracle3 服务健康，数据库迁移数为 9。
- 已审查本次变更：新增远程 OAuth 回调功能，无数据库迁移；Oracle3 旧 `.env` 包含已不兼容的授权 URL、空客户端 ID 与旧回调 URL，需在部署时替换为新提交文档指定的公共 Codex CLI OAuth 参数。
- 验证通过：`go test ./...`、`npm --prefix web ci --no-audit --no-fund && npm run build`。
- 已同步代码、更新 Oracle3 的 OAuth 授权 URL、公共客户端 ID 与 localhost 回调地址，并成功构建、重建 server 容器。
- 最终部署提交：`7809b510dcd8ae28eedc075d0b4c247692502fcb`；服务和 PostgreSQL 均 healthy，`/healthz` 返回 `ok`，数据库迁移数为 9。
- 初始管理员密码文件登录返回 401；只读核对确认当前有效管理员为 `allen0529`，应使用其现有密码登录，未重置凭据。

## 2026-09-13 — GitHub 与 Docker Hub 发布

- 用户指定后续发布一律按 `docs/RELEASE.md` 执行，并要求本次推送本地仓库、发布 Docker 镜像。
- 本次只有额度界面图标中文化的 UI 修复，按发布规范将版本从 `0.1.0` 升至 `0.1.1`。
- GitHub CLI 已以 `allen0039` 登录；待完成 lint、单元测试及前端构建后，提交并执行 `make deploy`。
