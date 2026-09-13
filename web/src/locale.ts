/** 中文展示词表。只翻译系统枚举；名称、密钥、模型和地址保持原值。 */
export const terms: Record<string, string> = {
  active: "已启用", paused: "已暂停", disabled: "已禁用", revoked: "已撤销",
  draft: "草稿", expired: "已到期", archived: "已归档", superseded: "已替换", suspended: "已暂停", scheduled: "待生效",
  pending: "等待处理", completed: "已完成", cancelled: "已取消", failed: "失败", unknown: "结果待确认",
  reauth_required: "需要重新授权", quota_exhausted: "额度已用尽", recovery_hold: "等待恢复确认",
  cooling_down: "冷却中", cooldown: "冷却中", healthy: "正常", unhealthy: "异常",
  member: "普通用户", admin: "管理员", computer: "电脑", hermes: "Hermes", cli: "命令行客户端", other: "其他",
  direct: "直接连接", http: "网页代理", socks5: "套接字代理", stop: "故障时停止", fallback: "切换备用代理",
  round_robin: "轮询", weighted_round_robin: "加权轮询", priority_failover: "按优先级故障切换",
  account: "上游账号", group: "账号池", pool: "账号池", key: "接口密钥", subscription: "订阅",
  key_account: "密钥与账号", key_group: "密钥与账号池", day: "每日", week: "每周", month: "每月",
  fixed: "固定金额", percent: "按比例", charge: "扣费", adjustment: "费用调整",
  synthetic: "测试价格", official: "官方", manual: "手动", verified: "已验证", unverified: "待验证",
  secret: "敏感凭据", content: "内容安全", injection: "指令注入", exfil: "数据外泄", format: "格式检查",
  flag: "标记", review: "待复核", block: "拦截", reject: "拒绝", unsupported: "不支持", allow: "放行", unavailable: "服务不可用",
  builtin: "系统内置", custom: "自定义", full_visible: "已检查全部可见内容", partial: "部分内容可检查", hit: "命中缓存", miss: "未命中缓存",
  confirmed_violation: "确认违规", false_positive: "判定误报", exception_created: "创建例外",
  over_reserve: "实际费用超出预留", unknown_pending: "请求结果待核对", admin_action: "管理员隔离", admin_pause: "管理员暂停", legacy_review: "历史状态待复核",
  true: "是", false: "否", low: "低", medium: "中", high: "高",
  held: "已预留", settled: "已结算", released: "已释放", received: "已接收", queued: "排队中", auditing: "审核中", reserved: "已预留",
  dispatched: "已发送", streaming: "响应中", succeeded: "成功", rejected: "已拒绝", unknown_resolved: "已核对结果", aborted: "已中止",
  input: "输入", output: "输出", instruction: "指令", instructions: "指令", tool_definition: "工具定义", tool_result: "工具结果", quoted_text: "引用文本", code: "代码",
  api_key: "接口密钥", client: "客户端", proxy: "代理", proxy_profile: "代理", egress_policy: "出口策略", account_group: "账号池", key_route: "密钥路由",
  budget_policy: "预算策略", price_version: "价格版本", audit_rule: "审核规则", audit_event: "审核事件", oauth_session: "授权会话", request: "请求", plan: "套餐", account_grant: "账号追加授权", pool_grant: "账号池追加授权",
};
export const domains: Record<string, Record<string, string>> = {
  account: { active: "正常" }, subscription: { active: "生效中" }, plan: { active: "已发布" },
  oauth: { pending: "等待授权", completed: "授权完成", failed: "授权失败", expired: "授权已过期", cancelled: "授权已取消" },
  readiness: { true: "已就绪", false: "未就绪" }, owner_type: { member: "用户" },
};
const actions: Record<string, string> = {
  create: "创建", update: "修改", delete: "删除", revoke: "撤销", publish: "发布", assign: "分配", sync: "同步", override: "手动覆盖",
  add_account: "添加账号到", remove_account: "从账号池移除账号", release_hold: "解除账号隔离", review: "复核", resolve_unknown: "核对请求结果", password_change: "修改密码", session_start: "发起授权", session_complete: "完成授权",
};
export const missingTerms = new Set<string>();
export function label(value: unknown, domain = ""): string {
  if (value === null || value === undefined || value === "") return "未设置";
  const key = String(value);
  const translated = domains[domain]?.[key] ?? terms[key];
  if (translated) return translated;
  if (domain === "action" && key.includes(".")) {
    const parts = key.split(".");
    const verb = actions[parts.pop()!];
    const subject = parts[0] === "user" ? terms[parts[1]] : terms[parts[0]] ?? ({oauth: "授权", prices: "价格", route: "路由"} as Record<string,string>)[parts[0]];
    if (verb && subject) return `${verb}${subject}`;
  }
  if (!missingTerms.has(`${domain}:${key}`)) {
    missingTerms.add(`${domain}:${key}`);
    if (typeof window !== "undefined" && window.location.hostname === "localhost") console.warn("缺少中文词条", domain, key);
  }
  return "未知状态";
}

export function dateTime(value: unknown, empty = "未设置"): string {
  if (value === null || value === undefined || value === "") return empty;
  // PostgreSQL timestamp text has a space separator, microseconds and sometimes a short offset.
  const normalized = String(value).replace(" ", "T").replace(/(\.\d{3})\d+/, "$1").replace(/([+-]\d{2})$/, "$1:00");
  const date = new Date(normalized);
  if (!Number.isFinite(date.getTime())) return "时间格式异常";
  return new Intl.DateTimeFormat("zh-CN", {timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23"}).format(date);
}
export function money(value: unknown, empty = "不限"): string {
  if (value === null || value === undefined || value === "") return empty;
  // Preserve decimal precision from the ledger; do not round tiny charges to zero.
  return `${String(value)} 美元`;
}
const enumColumns = new Set(["state", "status", "role", "kind", "type", "strategy", "failure_mode", "owner_type", "period", "mode", "entry_type", "origin", "category", "action", "source", "decision", "coverage", "cache_state", "target_type"]);
export function cellText(name: string, value: unknown): string {
  if (enumColumns.has(name)) return label(value, name);
  if (name.endsWith("_at") || name === "period_start" || name === "period_end") return dateTime(value);
  if (name === "percent_bps") return value == null || Number(value) < 0 ? "不适用" : `${Number(value) / 100}%`;
  if (name === "timezone") return value === "Asia/Shanghai" ? "北京时间" : value === "UTC" ? "协调世界时" : String(value || "未设置");
  return value === null || value === undefined || value === "" ? "未设置" : String(value);
}

export function errorText(error: unknown, status = 0): string {
  const text = error instanceof Error ? error.message : String(error ?? "");
  if (/[\u3400-\u9fff]/.test(text) && !/[a-zA-Z_]{2}/.test(text)) return text;
  if (/unresolved hold|hold reasons/.test(text)) return "该账号仍有未解除的隔离原因，请处理后再启用。";
  if (/version conflict/.test(text)) return "数据已更新或已删除，请刷新后重新编辑。";
  if (/duplicate|already exists|unique constraint/.test(text)) return "名称或关联已存在，请检查后重试。";
  if (/timeout|timed out|deadline/i.test(text)) return "连接超时，请检查地址和网络后重试。";
  if (/authentication|proxy.*407|credentials/i.test(text)) return "认证失败，请检查用户名和密码。";
  if (/refused|unreachable|no such host|fetch|network/i.test(text)) return "连接失败，请检查地址、端口和网络后重试。";
  if (status === 401) return "登录已过期或登录信息不正确，请重新登录。";
  if (status === 403) return "当前账号没有执行此操作的权限。";
  if (status === 404) return "记录不存在或已删除，请刷新后重试。";
  if (status === 409) return "操作未完成，请检查数据是否已更新或仍存在关联。";
  if (status === 429) return "操作过于频繁，请稍后重试。";
  if (status === 400 || status === 422) return "填写的信息不符合要求，请检查必填项和数值后重试。";
  return "操作失败，请稍后重试；如持续失败，请联系管理员查看日志。";
}
