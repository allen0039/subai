# 进度记录

## 2026-09-14

- 用户决定优先采用 Sub2API 风格计价方式。
- 已阅读 `planning-with-files` 技能并建立独立实施计划，未覆盖先前发布计划。
- 已确认现有套餐、订阅、Key、账号池动态路由和价格版本基础可复用。
- 完成阶段 0 基线测试和关键链路核对；确认可以复用订阅预算周期、usage ledger 与价格版本表。
- 已完成迁移、价格目录同步、请求后真实 usage 计量、套餐倍率冻结、管理 API 与界面调整。
- 已通过 `go test ./...`、前端构建和 PostgreSQL 集成测试的首轮验证。
- 收尾阶段开始处理零倍率免费套餐、metered 无限额套餐与手工价格覆盖的持久化语义。
- 已允许非负套餐倍率；倍率 0 时账本保留 base_cost、套餐实际消耗为 0。
- 已允许 metered 模式在无预算策略时调用并写账；strict_reservation 仍保持无预算拒绝。
- 已增加 `model_prices.is_manual_override`，手工版本保留完整目录，自动同步仅继承真实覆盖行。
- 文档已更新：README、PRICE_SOURCE、BILLING_BOUNDS、IMPLEMENTATION_STATUS。
- 最终验证通过：`gofmt -l cmd internal rules tests` 无输出、`go vet ./...`、`go test ./... -count=1`、`npm --prefix web run build`。
- 使用全新 PostgreSQL 16 执行修改后的全部迁移并运行 `make test-integration`，22.659s 通过；测试容器已执行 `make test-pg-down` 清理。
- 默认远程价格 JSON 与 SHA-256 sidecar 已实际下载并校验一致。
- 用户已授权提交、推送、发布 Docker Hub 并更新 Oracle3；将遵循 `make deploy`：先完成版本号与发布前验证，再推送、等待 CI 镜像、拉取更新并做健康检查。
- 发布前预检：GitHub CLI 已登录 `allen0039`；当前版本为 0.2.1；Oracle3 的 SSH 连接被远端关闭，尚在诊断，因此尚未开始任何发布写操作。
- Oracle3 SSH 复测已通过公钥认证与远程命令执行；首个连接关闭属于瞬态连接失败。版本已按功能发布规则从 0.2.1 升至 0.3.0。
- 发布前门禁通过：`make lint`、`make test`、`make build`；构建产物版本为 0.3.0（提交前因工作树正常显示 dirty）。
- 发布前全新 PostgreSQL 集成测试通过（25.757s）；测试容器已用 `make test-pg-down` 删除。`git diff --check` 通过，待创建发布提交。
