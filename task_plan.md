# 实施计划编制进度

本次任务：整理独立 Codex 网关实施计划，重点明确本地规则与官方 Moderation 审核方案；不开始产品开发或部署。

- [x] 汇总已确认需求与既有调研。
- [x] 编写实施计划、阶段依赖和验收条件。
- [x] 区分已知能力、设计建议与待验证事项。
- [x] 检查文档完整性并交付。

主文档：IMPLEMENTATION_PLAN.md。

## v0.2 扩展

- [x] 设备数改为初期样本，成员、设备和Key动态扩展。
- [x] 补全数据、接口、预算、状态机、规则与部署契约。
- [x] 新增DEVELOPMENT_HANDOFF.md供其他实施工具使用。
- [x] 检查24节结构、交接引用及固定设备限制残留。

产品开发尚未开始；文档编制完成不代表网关实现完成。

错误记录：无。

## 2026-09-14 WebUI 修复与优化计划

- [x] 审查当前 WebUI 方案和未提交实现，确认原型存在构建、路由、API 契约及功能回归风险。
- [x] 建立可构建基线：恢复哈希路由、身份分流与既有资源操作；完成 Shell 与真实状态快照的首轮改造。
- [x] 输出 `docs/WEBUI_REFACTOR_REPAIR_PLAN.md`，定义 P0–P6、数据契约、验收和发布门槛。
- [x] P0：删除未接入原型、mock 页面及失实“完成”文档，正式入口只保留已验证的哈希路由和业务页面。
- [x] P1/P2：实现控制塔壳层、分组导航、主题记忆、筛选、取消陈旧请求、空态及键盘关闭/焦点恢复。
- [x] P3/P4：新增受权限保护的 dashboard、搜索、事件流和个人概览接口；接入真实聚合、额度快照与事件数据。
- [x] P5：个人台明确订阅 → Key → 资源访问边界，并展示本人近 7 日已记账成本。
- [ ] P6：完成静态检查、真实 PostgreSQL 集成测试、镜像构建和部署前发布检查。

当前阶段：P6（进行中）。

## 2026-09-14 双主题色彩统一计划

- [x] 基于登录页和用户管理页截图，以及现有样式表完成问题定位。
- [x] 输出 `docs/WEBUI_THEME_PLAN.md`：定义 token、组件迁移、切换体验、可访问性与验收门槛。
- [x] 实施 T1–T4：建立双主题 token、迁移通用组件及业务特例、补充登录页切换和主题持久化、完成对比度检查。
- [x] 完成本地 T5 验收：浏览器深浅主题检查、构建、单元/集成测试均通过。
- [ ] 发布主题改造待用户单独要求；本轮未推送、未部署 Oracle3。

## 2026-09-14 上游额度刷新与重置卡

- [x] 核对现有额度刷新 API、快照模型与 Sub2API 的官方接口实现。
- [x] 扩展后端额度快照：采集重置卡可用张数与安全的过期时间，详情失败时保留 `/wham/usage` 基础额度。
- [x] 扩展上游账号列表：增加逐行刷新状态，并在摘要中显示重置卡张数。
- [x] 扩展额度详情：刷新后原地更新，显示重置卡张数、最近及全部过期时间。
- [x] 完成 Go/React/集成测试、静态检查和深浅主题视觉验收。

当前阶段：实现与本地验收完成；未提交、推送或部署。

## 2026-09-14 模型能力与精细计费方案

- [x] 盘点现有模型目录、套餐/订阅/Key 白名单、价格版本与全局倍率链路。
- [x] 研究 Sub2API 的资源组模型白名单、模型映射、模型定价目录与模型广场职责。
- [x] 定义不破坏现有订阅版本、账本冻结和未知结果收敛的目标架构。
- [x] 输出数据模型、优先级、迁移、接口、管理台、分阶段发布和验收方案。

当前阶段：M1/M2 已实施；已添加模型能力迁移、调度前模型筛选、上游模型映射、套餐模型级价格覆盖与管理 API，待人工迁移配置后启用细粒度规则。

错误记录：首次编译发现 `scheduler` 缺少 `pgx.ErrNoRows` 的包导入；已定点补充，不重复该失败路径。

## 2026-09-14 sub2api 整体替换方案

范围：仅编制实施方案。完整采用 sub2api，唯一产品定制为现有 GPT 登录/授权方案。
- [x] 核对旧项目 OAuth 与部署边界。
- [x] 核对上游基线、模块与接入点。
- [x] 输出实施、数据处理、验收与回滚方案。
错误记录：zsh 未匹配 internal/admin/*oauth*；后续使用 rg 文件发现，避免未匹配 glob。

## sub2api 实施（已获用户授权）
- [x] 隔离 worktree，固定上游 bdb42e22 和旧项目 1b6d901。
- [x] 完整导入上游跟踪文件及许可证，记录逐文件 SHA256。
- [x] 验证基线，适配 GPT OAuth 并测试。
- [x] 完成自有镜像/独立数据部署配置与交付文档。
- [x] 完成可执行检查，标记真实账号与数据迁移边界。

当前状态：代码整体替换及本地验证已完成；真实账号联调、旧数据迁移和线上切换未执行，不计为已验收。


## 2026-09-14 应用到 main
按用户要求，将已验证的隔离工作区源码应用到 main：4105 个变更路径逐项内容及权限核对一致。额外迁入 docs/subai 文档并修正忽略规则，保留 GPT 原登录方案；未提交、推送、部署或迁移旧数据库。此前 main 保留旧运行时代码的记录为历史状态。


## 2026-09-15 Console simplification
Scope: remove onboarding and administrator compliance gate; remove login agreement, model plaza and all platform social login settings/entry points. Keep password login and original GPT upstream OAuth. Plan: implement frontend/backend changes, run targeted tests/build and browser verification, publish image and update Oracle3 on port 18080 with existing new database retained.

Console simplification phases: implementation complete; source checks complete; image publish and live browser validation in progress.

Console simplification phases complete: implementation, verification, image publication, production deployment and browser validation.


## 2026-09-15 Confirm-only primary email replacement
Implement explicit confirmation for authenticated users changing an already-bound email, with no email code/current-password prompt. Preserve password hash, alias collision checks, transaction and session invalidation. First-time binding retains its separate password setup flow. Test, publish and deploy without changing the actual administrator email.
