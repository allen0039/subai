# Round 8 修复复核与清理

> 后续清理：用户明确要求清理后，已彻底删除上述仓库外归档及 `/tmp/subai-research` 参考源码副本，原归档不再可恢复。源码来源和固定版本仍保留在研究报告中。用途未确认的临时密钥/env 文件未删除；有效测试与项目依赖保留。

## 结论

当前空库迁移和现有集成测试已通过，但 ROUND8_FIXES_VERIFIED.md 的“全部验证、可以安全部署”结论不成立。此次清理未修改业务逻辑；仅规范两个 Go 文件格式、清理部署示例与无效产物，并在原报告顶部加更正。

## 当前仍需解决

1. **P1：过期会话阻止重新授权。** `internal/accounts/oauth.go` 的 StartSession 不会把过期 pending 转为 expired，0007 唯一索引又覆盖所有 pending。CompleteCallback 的过期 UPDATE 随错误返回被回滚。发起授权后等待超过十分钟、不完成授权，会持续占住账号，直到另行修改会话状态。应事务内释放过期会话并覆盖完整重试流程。
2. **P1：旧刷新失败仍可封住新授权。** Refresh 虽增加 advisory lock，回调没有共享这把锁，失败调用的 setReauthRequired 也没有 credential_version 条件。旧请求返回错误时仍可能给已重新授权的账号添加 hold；网络错误也被等同于凭据失效。成功和失败更新都应进行版本核验及错误分类。
3. **升级风险未完整验证。** bootstrap 已在查询 checksum 前补列，解决了原始缺列顺序问题；但 legacy/bootstrap 被永久跳过校验，迁移器没有迁移锁，0007 也没有处理旧数据中重复 pending。此次只运行空库迁移，不宣称真实旧版升级已通过。
4. **测试报告存在覆盖失实。** 被移出仓库的 oauth_concurrency_test.go 中 setupTestDB 无条件 t.Skip，账号和 OAuth mock 帮助函数也是占位。两个根目录 OAuth shell 脚本只是直接执行 SQL，并非调用真实应用回调，也不是完整并发测试。代码存在或编译通过不等于行为已验证。
5. **错误分类不准确。** StartSession 把取消/超时之外的所有 INSERT 错误都归为 ErrPendingSessionExists，应只将 pgx.ErrNoRows 对应的冲突映射为该错误，保留实际数据库故障。

这些问题来自当前源码路径复核；本次未新增完整故障注入测试，不将静态结论冒充动态复现。

## 已完成清理

- 从工作区移走 audit.test、server、subai、bin/subai-server 和 .DS_Store。
- 移走 test-r8-fixes：它将 macOS Mach-O 可执行文件挂入 Alpine Linux，且使用应用不读取的 SUBAI_DB_HOST 等变量。
- 移走两个硬编码旧容器的 OAuth SQL 脚本、verify_round4.sh（引用不存在的迁移路径）、scripts/verify_batch_b_fixes.sh（大量以字符串存在判断修复成功）。
- 移走永远跳过的 oauth_concurrency_test.go；保留其余 17 个真实 Go 测试文件及集成测试夹具。删除占位测试意味着明确暴露覆盖缺口，并不意味着并发行为已经验证。
- 移走已知的旧审查临时二进制和日志。共 31 个条目，约 154.5 MiB，保存在仓库外，可恢复；这是工作区整理，不是释放同等磁盘空间。
- 删除未被容器使用的项目测试镜像 subai-clean-deploy:test。
- deploy/.env.example 删除 SUBAI_ADMIN_ORIGIN、SUBAI_PRICE_SYNC_INTERVAL_H、SUBAI_LOG_RETENTION_DAYS 和重复 SUBAI_OUTPUT_BOUND，保留原有注释分组，不使用 sort -u 打乱文件。
- .gitignore/.dockerignore 增加测试二进制、根目录服务器产物及本地工具配置的排除规则。
- gofmt 规范 internal/accounts/oauth.go、internal/accounts/oauth_test.go。

可恢复归档：`/Users/allen/Downloads/subai-cleanup-20260913/`，权限 0700；`manifest.json` 逐项记录原路径与归档位置。原报告、设计文档、依赖锁文件和有效测试未删除。

## 本机配置检查边界

检查了项目本地 Claude 配置结构、用户 shell 配置中的项目引用、用户 LaunchAgents、相关运行进程、Docker 容器/卷/镜像。未发现需要移除的 subai 自启动项或正在运行的旧 subai 服务；未发现遗留 subai Docker 卷。无关的 mt-photos-ai 容器及其他应用配置未改动。

项目 .claude/settings.local.json 只有两条本地许可规则，未据此判定无效，保留原文件并排除版本/镜像。web/node_modules 和 web/dist 支持当前开发及静态页面运行，保留。/tmp/subai-research 是上一轮有来源的参考源码，保留；未打开或删除用途未确认的临时密钥/env 文件。此次不是对整台机器所有软件配置的无差别删除。

## 本次实际验证

| 项目 | 结果 |
|---|---|
| go test ./internal/... ./rules/... -count=1 | 通过；当时占位并发测试会跳过 |
| 隔离 PostgreSQL 16 + go test ./tests/integration/ -count=1 -timeout 300s | 通过，15.265 秒；8 个迁移记录 |
| 清理后 make lint | 通过；初次失败的格式问题已修正 |
| 清理后 go test -race ./internal/... ./rules/... -count=1 | 通过 |
| go build -o /tmp/subai-cleanup-build ./cmd/server | 通过 |
| npm --prefix web run build | 通过 |

测试 PostgreSQL 仅绑定 127.0.0.1:54348，无生产数据。检查后停止并自动删除容器。本次未跑真实 OAuth 提供方、浏览器端到端或旧版数据完整升级，不能据此批准生产部署。
