# 发布流程（GitHub + Docker Hub → Oracle3）

每次对外发布新版本之前，按本文档的顺序执行。整套流程的自动化已就绪：
push 到 `main` 触发 GitHub Actions 构建镜像并发布到 Docker Hub，
服务器只从 Docker Hub 拉镜像更新。

## 0. 前置条件（只需检查一次）

```bash
gh auth status          # GitHub CLI 已登录 allen0039
ssh oracle3 hostname    # 免密 ssh 可用（~/.ssh/config 有 oracle3 条目）
make --version          # make 可用
```

GitHub Actions secrets 必须存在（已配置好，勿删）：
`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`。token 失效时去 Docker Hub →
Account Settings → Security 重新生成 Access Token，然后
`gh secret set DOCKERHUB_TOKEN --repo allen0039/subai` 更新。

## 1. 发布前检查（每次必做）

```bash
git status                        # 工作区必须干净，改动全部提交
make lint                         # go vet + gofmt
make test                         # 单元测试
cd web && npm run build && cd ..  # 前端能构建（CI 里会再跑一次）
```

## 2. 更新版本号（有功能变更时）

版本号来自仓库根目录的 `VERSION` 文件，CI 会把它注入镜像并作为 Docker tag：

```bash
echo "0.2.0" > VERSION
```

约定：修 bug 升第三位，加功能升第二位，破坏性变更升第一位。
纯内部小改动可以不升版本号（仍会有 `latest` 和 `sha-*` tag）。

## 3. 提交并推送

```bash
git add -A
git commit -m "release: vX.Y.Z <一句话说明>"
```

**不要手动 `git push`**，下一步的 `make deploy` 会推。

## 4. 一条龙发布（推荐）

```bash
make deploy
```

它依次自动执行：

1. 校验工作区干净；
2. `git push origin main`；
3. 监听最新一次 Actions 构建，构建失败则中止；
4. ssh 到 Oracle3，`docker compose pull server && docker compose up -d`；
5. 打印容器运行状态。

成功标志：输出里出现 `docker.io/allen0039/subai-server:latest Up ... (healthy)`。

## 5. 发布后验证

```bash
# 服务器健康
ssh oracle3 'curl -s http://127.0.0.1:18080/healthz'        # 应输出 ok
ssh oracle3 'tail -20 /opt/1panel/docker/compose/subai/logs/server.log'

# 镜像 tag 已就位（应包含刚推的 sha-<hash> 和新 VERSION）
curl -s "https://hub.docker.com/v2/repositories/allen0039/subai-server/tags?page_size=10" \
  | python3 -m json.tool | grep '"name"'
```

数据库迁移由服务启动时自动执行，发布后扫一眼日志确认无 migration 报错。

## 6. 回滚

每个提交都有固定 tag（`sha-<commit hash>` 和版本号 tag），回滚 = 指定旧 tag：

```bash
# 在服务器上临时改 compose 的 image tag 后 up -d，回滚完记得改回 latest
ssh oracle3 'cd /opt/1panel/docker/compose/subai \
  && sed -i "s|subai-server:.*|subai-server:sha-<旧commit>|" docker-compose.yml \
  && docker compose up -d server'
```

数据库迁移不可自动回滚，回滚前确认旧版本能兼容当前 schema（历史上迁移都是增量加表/列，一般安全）。

## 7. 备用与例外

- **只想刷新服务器**（CI 已出镜像、或想拉取别人的更新）：
  ```bash
  make pull-update
  ```
- **CI 挂了 / GitHub 不可用**：服务器上留有同步过的源码，可本地构建兜底：
  ```bash
  ssh oracle3 'cd /opt/1panel/docker/compose/subai \
    && docker compose build server && docker compose up -d'
  ```
- **手动触发 CI**（不推代码重出镜像）：`gh workflow run docker --repo allen0039/subai`

## 8. 硬性规则

- `.env`、`data/`、`secrets/`、`logs/` **永远不进 git、不进镜像**。
- 发布期间不要在本地大改文件（rsync/构建会读到中间状态）。
- push 后 CI 完成前不要 `pull-update`，拉到的会是旧镜像；
  `make deploy` 已经内置了等待，无此风险。
- `DEPLOYED_COMMIT` 文件记录服务器当前运行的提交，仅由发布流程更新，勿手改。
