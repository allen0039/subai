# sub2api 整体替换实施状态

2026-09-14：已将隔离 worktree 中完成本地验证的源码整体替换应用到 main 工作区，已完成文件完整性核对；未迁移旧库或上线。

- 分支：`main`
- 代码目录：`/Users/allen/Downloads/Agent_Worker/subai`
- [启动与维护说明](subai/README.md)
- [验证结果与未执行事项](subai/VALIDATION.md)
- [上游完整性清单](subai/IMPORT_AUDIT.json)

固定导入 sub2api bdb42e22 的全部 3919 个跟踪文件，缺失 0。GPT 登录采用现有 SubAI 方案；其余功能采用上游实现。原手动会话接口和公开 OAuth 回调保留，均复用同一个授权服务及上游账号模型。

Go 单元/集成测试、GPT race 专项、后端 lint、前端 2098 项测试、Docker 构建、独立空库启动与浏览器/API 冒烟通过。真实 GPT 账号联调和数据迁移未完成，不能视为已上线验收。

当前 main 工作区已包含替换后的完整源码，提交与推送按用户授权在 main 执行。后续可在 main 工作区继续审查和修改。迁移按文件清单执行，未复制依赖缓存或修改本地配置。
