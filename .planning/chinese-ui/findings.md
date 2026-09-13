# 检查发现
- 工作区开始时干净；前一轮账号代理编辑已经存在。
- 下拉选项和 Badge 直接显示枚举；通用表格直接显示字段。
- App 和 portal 散布 Key、OAuth、开发编号、原始时间等文案。
- writeErr 统一返回原始 message，前端直接抛出；可增加 code 和 display_message 以保持兼容。
- 隔离和复核使用 prompt 要求英文值；需专门组件。
