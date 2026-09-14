import React, { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { Badge, Modal, ResourcePage, Field, Column } from "./components";
import { cellText, dateTime, errorText, label, money } from "./locale";
import { AdminPlans, AdminSubscriptions, AdminUserEntitlements, MyKeys, MyOverview, MySubscriptions, SecurityPage } from "./portal";

// ── Resource registry: every admin surface, driven by data (§17.2) ──────────

const statusCol: Column = { name: "status", label: "状态", render: (r) => <Badge value={r.status ?? ""} /> };

const membersPage = () => (
  <ResourcePage
    title="用户管理"
    basePath="/api/admin/users"
    columns={[
      { name: "name", label: "用户名" },
      { name: "role", label: "角色", render: (r) => <Badge value={r.role} /> },
      statusCol,
      { name: "subscription_count", label: "订阅" },
      { name: "key_count", label: "接口密钥数" },
      { name: "created_at", label: "创建时间" },
    ]}
    createFields={[
      { name: "name", label: "用户名", required: true },
      { name: "password", label: "密码", kind: "password", required: true, help: "密码经过安全加密处理，不设默认密码" },
      { name: "role", label: "角色", kind: "select", options: ["member", "admin"], default: "member" },
    ]}
    editFields={[
      { name: "name", label: "用户名" },
      { name: "password", label: "新密码", kind: "password", help: "留空则不修改；重置会注销其他会话" },
      { name: "status", label: "状态", kind: "select", options: ["active", "disabled"] },
      { name: "role", label: "角色", kind: "select", options: ["member", "admin"] },
    ]}
    rowActions={(row) => <a className="btn small" href={`#/users/${row.id}`}>权益</a>}
  />
);

const clientsPage = () => (
  <ResourcePage
    title="客户端（设备）"
    basePath="/api/admin/clients"
    columns={[
      { name: "name", label: "名称" },
      { name: "type", label: "类型", render: (r) => <Badge value={r.type} /> },
      statusCol,
      { name: "member_name", label: "所属用户" },
      { name: "key_count", label: "接口密钥数" },
    ]}
    createFields={[
      { name: "member_id", label: "所属用户", kind: "select", optionsPath: "/api/admin/users", required: true },
      { name: "name", label: "名称", required: true },
      { name: "type", label: "类型", kind: "select", options: ["computer", "hermes", "cli", "other"], required: true },
      { name: "notes", label: "备注", kind: "textarea" },
    ]}
    editFields={[
      { name: "status", label: "状态", kind: "select", options: ["active", "disabled"] },
      { name: "notes", label: "备注", kind: "textarea" },
    ]}
    notice={<span className="muted small">客户端数量不设上限。</span>}
  />
);

const keysPage = () => (
  <ResourcePage
    title="接口密钥"
    basePath="/api/admin/keys"
    columns={[
      { name: "name", label: "名称" },
      { name: "public_prefix", label: "前缀" },
      statusCol,
      { name: "concurrency_limit", label: "单个密钥并发" },
      { name: "client_id", label: "客户端" },
      { name: "created_at", label: "创建时间" },
    ]}
    createFields={[
      { name: "member_id", label: "所属用户", kind: "select", optionsPath: "/api/admin/users", required: true },
      { name: "client_id", label: "关联客户端（可选）", kind: "select", optionsPath: "/api/admin/clients" },
      { name: "name", label: "名称", required: true },
      { name: "concurrency_limit", label: "单个密钥并发", kind: "number", min: 1, default: 1, help: "只限制这个接口密钥同时执行的请求数。" },
      { name: "allowed_models", label: "允许模型（逗号分隔，留空=全部）", help: "逗号分隔" },
    ]}
    editFields={[{ name: "status", label: "状态", kind: "select", options: ["active", "paused"] }]}
    rowActions={(row, reload) => (
      <>
        <button
          className="btn small danger"
          onClick={async () => {
            if (!confirm("撤销后不可恢复，确认？")) return;
            try { await api.post(`/api/admin/keys/${row.id}/revoke`); }
            catch (error) { alert(errorText(error)); return; }
            reload();
          }}
        >
          撤销
        </button>
        <a className="btn small" href={`#/routes/${row.id}`}>
          路由
        </a>
      </>
    )}
  />
);

const accountsPage = () => (
  <ResourcePage
    title="上游账号"
    basePath="/api/admin/accounts"
    columns={[
      { name: "label", label: "标签" },
      { name: "provider", label: "上游类型", render: (r) => r.provider === "openai_compatible" ? "兼容中转" : "Codex OAuth" },
      { name: "upstream_base_url", label: "兼容端点", render: (r) => r.provider === "openai_compatible" ? r.upstream_base_url : "—" },
      { name: "state", label: "状态", render: (r) => <Badge value={r.state} domain="account" /> },
      { name: "concurrency_limit", label: "账号并发容量" },
      { name: "priority", label: "优先级" },
      { name: "proxy_name", label: "当前代理" },
      { name: "quota", label: "官方额度", render: (r) => <QuotaSummary row={r} /> },
      { name: "expires_at", label: "登录凭证到期" },
    ]}
    createFields={[
      { name: "label", label: "标签", required: true },
      { name: "provider", label: "上游类型", kind: "select", default: "codex", options: [{ value: "codex", label: "Codex OAuth 直连" }, { value: "openai_compatible", label: "OpenAI 兼容中转（CPA / Sub2API）" }], help: "兼容中转会按其 /v1/responses 接口调用，并作为账号池的一个成员参与调度。" },
      { name: "upstream_base_url", label: "兼容上游地址", required: true, placeholder: "http://cpa.example:8317/v1", visibleWhen: body => body.provider === "openai_compatible", help: "填写 /v1 基地址或完整的 /responses 地址；不填密钥到 URL 中。" },
      { name: "access_token", label: "访问令牌 / 中转 API Key", kind: "password", required: true, help: "加密保存，仅在创建时填写；兼容中转通常填写 CPA 或 Sub2API 的 API Key。" },
      { name: "refresh_token", label: "刷新令牌（可选）", kind: "password", visibleWhen: body => body.provider === "codex" },
      { name: "account_id", label: "上游账号标识（可选）", visibleWhen: body => body.provider === "codex" },
      { name: "concurrency_limit", label: "账号并发容量", kind: "number", min: 1, default: 1, help: "该上游账号可同时承载的请求数。调度会跳过已满账号，并优先选择当前负载较低的账号。" },
      { name: "egress_policy_id", label: "出口策略（可选）", kind: "select", optionsPath: "/api/admin/egress-policies" },
    ]}
    editFields={[
      { name: "label", label: "标签", required: true },
      { name: "proxy_id", label: "切换代理", kind: "select", optionsPath: "/api/admin/proxies", help: "不选择则保留当前出口策略；选择后使用该代理，故障即停止" },
      { name: "state", label: "状态", kind: "select", options: ["active", "paused"] },
      { name: "concurrency_limit", label: "账号并发容量", kind: "number", min: 1, help: "只限制这个上游账号；不会改变套餐或单个接口密钥的并发限制。" },
      { name: "priority", label: "优先级", kind: "number" },
      { name: "upstream_base_url", label: "兼容上游地址", visibleWhen: body => body.provider === "openai_compatible", help: "/v1 基地址或完整 /responses 地址。" },
      { name: "api_key", label: "轮换中转 API Key", kind: "password", visibleWhen: body => body.provider === "openai_compatible", help: "留空不修改；密钥不会再次显示。" },
    ]}
    rowActions={(row, reload) => <>{row.provider === "codex" && <><QuotaRefreshAction row={row} reload={reload} /><QuotaActions row={row} reload={reload} /></>}<HoldActions row={row} reload={reload} /></>}
    notice={<OAuthStarter />}
    filters={[
      { name: "q", label: "账号", placeholder: "按标签前缀搜索" },
      { name: "state", label: "状态", kind: "select", options: ["active", "paused", "refreshing", "reauth_required", "quota_exhausted", "proxy_unavailable", "recovery_hold"] },
    ]}
  />
);

type QuotaWindow = { used_percent?: number; limit_window_seconds?: number; reset_after_seconds?: number; reset_at?: number };
type ResetCredit = { expires_at?: string };
type ResetCreditInfo = { known: boolean; available: number; credits: { expiresAt: Date; expired: boolean }[] };
type QuotaRefreshResponse = { quota: any; quota_fetched_at?: string; quota_error?: string };
type ResetCreditConsumeResponse = { consumed: boolean; windows_reset?: number; quota?: any; quota_fetched_at?: string; warning?: string };

function quotaWindows(quota: any): QuotaWindow[] {
  return [quota?.rate_limit?.primary_window, quota?.rate_limit?.secondary_window].filter(Boolean);
}

function findQuotaWindow(quota: any, kind: "five" | "week"): QuotaWindow | undefined {
  return quotaWindows(quota).find(window => {
    const seconds = Number(window.limit_window_seconds ?? 0);
    return kind === "five" ? seconds > 0 && seconds <= 6 * 3600 : seconds >= 6 * 24 * 3600;
  });
}

function remainingPercent(window?: QuotaWindow): number | null {
  if (!window || !Number.isFinite(Number(window.used_percent))) return null;
  return Math.max(0, Math.min(100, 100 - Number(window.used_percent)));
}

function resetCreditInfo(quota: any): ResetCreditInfo {
  const value = quota?.rate_limit_reset_credits;
  if (!value || !Number.isFinite(Number(value.available_count))) {
    return { known: false, available: 0, credits: [] };
  }
  const now = Date.now();
  const credits = (Array.isArray(value.credits) ? value.credits : [])
    .map((credit: ResetCredit) => new Date(String(credit?.expires_at ?? "")))
    .filter((expiresAt: Date) => Number.isFinite(expiresAt.getTime()))
    .sort((a: Date, b: Date) => a.getTime() - b.getTime())
    .map((expiresAt: Date) => ({ expiresAt, expired: expiresAt.getTime() <= now }));
  const expiredCount = credits.filter((credit: { expired: boolean }) => credit.expired).length;
  return {
    known: true,
    available: Math.max(0, Math.floor(Number(value.available_count)) - expiredCount),
    credits,
  };
}

function resetTime(window?: QuotaWindow): string {
  if (!window) return "未提供";
  if (Number(window.reset_at) > 0) return dateTime(new Date(Number(window.reset_at) * 1000).toISOString());
  if (Number(window.reset_after_seconds) >= 0) return dateTime(new Date(Date.now() + Number(window.reset_after_seconds) * 1000).toISOString());
  return "未提供";
}

function resetDate(window?: QuotaWindow): Date | null {
  if (!window) return null;
  if (Number(window.reset_at) > 0) return new Date(Number(window.reset_at) * 1000);
  if (Number(window.reset_after_seconds) >= 0) return new Date(Date.now() + Number(window.reset_after_seconds) * 1000);
  return null;
}

function resetCountdown(window?: QuotaWindow): string {
  const target = resetDate(window);
  if (!target || !Number.isFinite(target.getTime())) return "等待官方返回";
  const minutes = Math.max(0, Math.ceil((target.getTime() - Date.now()) / 60000));
  if (minutes === 0) return "即将恢复";
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const mins = minutes % 60;
  if (days > 0) return `${days} 天 ${hours} 小时后`;
  if (hours > 0) return `${hours} 小时 ${mins} 分钟后`;
  return `${mins} 分钟后`;
}

const planNames: Record<string,string> = {
  free: "免费版", plus: "个人增强版", pro: "专业版", team: "团队版", business: "商业版", enterprise: "企业版", edu: "教育版",
};

function planName(value: unknown): string {
  const key = String(value ?? "").trim().toLowerCase();
  return planNames[key] ?? (key ? "其他套餐" : "未提供");
}

const QuotaBar: React.FC<{ label: string; window?: QuotaWindow }> = ({ label: title, window }) => {
  const remaining = remainingPercent(window);
  const tone = remaining == null ? "unknown" : remaining <= 20 ? "critical" : remaining <= 50 ? "warning" : "healthy";
  const display = remaining == null ? "—" : remaining.toFixed(remaining % 1 ? 1 : 0);
  return <div className={`quota-window quota-window-${tone}`}>
    <div className="quota-window-head">
      <div><span className="quota-window-icon" aria-hidden="true">{title.startsWith("五") ? "时" : "周"}</span><div><b>{title}</b><small>{window ? "滚动用量窗口" : "暂无窗口数据"}</small></div></div>
      <div className="quota-percent"><span>剩余</span><strong>{display}{remaining != null && <em>%</em>}</strong></div>
    </div>
    <div className="quota-progress" aria-label={`${title}${remaining == null ? "暂无数据" : `剩余 ${remaining}%`}`}><span style={{width: `${remaining ?? 0}%`}} /></div>
    <div className="quota-reset"><span>预计恢复</span><b>{resetCountdown(window)}</b><time>{resetTime(window)}</time></div>
  </div>;
};

const QuotaSummary: React.FC<{ row: any }> = ({ row }) => {
  const five = remainingPercent(findQuotaWindow(row.quota, "five"));
  const week = remainingPercent(findQuotaWindow(row.quota, "week"));
  const resetCards = resetCreditInfo(row.quota);
  if (!row.quota) return <span className="muted small">尚未同步</span>;
  return <span className="quota-summary">五小时 {five == null ? "未知" : `${Math.round(five)}%`} · 每周 {week == null ? "未知" : `${Math.round(week)}%`} · 重置卡 {resetCards.known ? resetCards.available : "未知"}</span>;
};

const QuotaRefreshAction: React.FC<{ row: any; reload: () => void }> = ({ row, reload }) => {
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState("");
  const refresh = async () => {
    setRefreshing(true);
    setError("");
    try {
      await api.post(`/api/admin/accounts/${row.id}/quota/refresh`);
      reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setRefreshing(false);
    }
  };
  return <span className="quota-refresh-action">
    <button className="btn small" disabled={refreshing} onClick={refresh}>{refreshing ? "刷新中…" : "刷新额度"}</button>
    {error && <span className="quota-action-error" role="alert" title={error}>{error}</span>}
  </span>;
};

const QuotaActions: React.FC<{ row: any; reload: () => void }> = ({ row, reload }) => {
  const [open, setOpen] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState("");
  const [displayRow, setDisplayRow] = useState(row);
  const [needsReload, setNeedsReload] = useState(false);
  const [consumeOpen, setConsumeOpen] = useState(false);
  const [consuming, setConsuming] = useState(false);
  const [consumeError, setConsumeError] = useState("");
  const [consumeWarning, setConsumeWarning] = useState("");
  const refresh = async () => {
    setRefreshing(true); setError("");
    try {
      const result = await api.post<QuotaRefreshResponse>(`/api/admin/accounts/${row.id}/quota/refresh`);
      setDisplayRow((current: any) => ({...current, ...result, quota: result.quota}));
      setNeedsReload(true);
      setConsumeWarning("");
    }
    catch (e) { setError(errorText(e)); }
    finally { setRefreshing(false); }
  };
  const close = () => {
    setOpen(false);
    if (needsReload) reload();
  };
  const resetCards = resetCreditInfo(displayRow.quota);
  const consume = async () => {
    setConsuming(true);
    setConsumeError("");
    try {
      const result = await api.post<ResetCreditConsumeResponse>(`/api/admin/accounts/${row.id}/quota/reset-credits/consume`);
      if (!result.consumed) throw new Error("官方未确认重置卡已使用，请刷新后重试。");
      if (result.quota) {
        setDisplayRow((current: any) => ({...current, quota: result.quota, quota_fetched_at: result.quota_fetched_at ?? current.quota_fetched_at, quota_error: ""}));
      }
      setNeedsReload(true);
      setConsumeWarning(result.warning ?? "");
      setConsumeOpen(false);
    } catch (e) {
      setConsumeError(errorText(e));
    } finally {
      setConsuming(false);
    }
  };
  return <>
    <button className="btn small" onClick={() => { setDisplayRow(row); setNeedsReload(false); setConsumeWarning(""); setError(""); setOpen(true); }}>额度详情</button>
    {open && <Modal className="quota-modal" title="官方账号额度" onClose={close} onSubmit={refresh} submitLabel={refreshing ? "正在同步…" : "刷新官方额度"} submitDisabled={refreshing}>
      <div className="quota-details">
        <section className="quota-account-head">
          <div className="quota-account-mark" aria-hidden="true">智</div>
          <div className="quota-account-copy"><span>当前账号</span><b>{displayRow.quota?.email || displayRow.quota?.upstream_account_id || displayRow.label}</b><small>{displayRow.label}</small></div>
          <span className="quota-plan">{planName(displayRow.quota?.plan_type)}</span>
        </section>
        <section className="quota-overview">
          <div><span>订阅有效期</span><b>{dateTime(displayRow.quota?.subscription_expires_at, "官方暂未提供")}</b></div>
          <div><span>额度状态</span><b className={displayRow.quota ? "quota-online" : ""}>{displayRow.quota ? "数据已同步" : "等待首次同步"}</b></div>
        </section>
        <section className="quota-window-grid">
          <QuotaBar label="五小时额度" window={findQuotaWindow(displayRow.quota, "five")} />
          <QuotaBar label="周额度" window={findQuotaWindow(displayRow.quota, "week")} />
        </section>
        <section className="quota-reset-cards">
          <div className="quota-reset-card-head">
            <div><span>额度重置卡</span><small>可用于恢复官方 Codex 额度</small></div>
            <strong>{resetCards.known ? `${resetCards.available} 张` : "尚未获取"}</strong>
          </div>
          {!resetCards.known && <p>点击“刷新官方额度”获取重置卡数量与过期时间。</p>}
          {resetCards.known && resetCards.available === 0 && resetCards.credits.length === 0 && <p>当前没有可用的额度重置卡。</p>}
          {resetCards.known && resetCards.available > 0 && resetCards.credits.length === 0 && <p>官方暂未提供重置卡过期时间。</p>}
          {!!resetCards.credits.length && <div className="quota-credit-list">
            {resetCards.credits.map((credit, index) => <div key={`${credit.expiresAt.toISOString()}-${index}`}>
              <span>第 {index + 1} 张{index === 0 && !credit.expired ? " · 最近过期" : ""}</span>
              <time>{dateTime(credit.expiresAt.toISOString())}</time>
              {credit.expired && <b>已过期</b>}
            </div>)}
          </div>}
          {resetCards.known && resetCards.available > 0 && !consumeWarning && <div className="quota-consume-action">
            <span>每次使用 1 张，并立即恢复官方额度窗口。</span>
            <button type="button" className="btn small danger" disabled={refreshing || consuming} onClick={() => { setConsumeError(""); setConsumeOpen(true); }}>使用 1 张重置卡</button>
          </div>}
          {consumeWarning && <p className="quota-consume-warning">{consumeWarning}</p>}
        </section>
        <section className="quota-sync-status">
          <div><span>数据更新时间</span><b>{dateTime(displayRow.quota_fetched_at, "尚未同步")}</b></div>
          <div><span>最近同步尝试</span><b>{dateTime(displayRow.quota_last_attempt_at, "尚未尝试")}</b></div>
        </section>
        {displayRow.quota_error && <div className="error">上次同步失败：{displayRow.quota_error}</div>}
        {error && <div className="error">{error}</div>}
        <p className="quota-note"><span aria-hidden="true">i</span>额度、重置卡和过期时间来自官方账号服务，订阅有效期仅在官方返回时显示。</p>
      </div>
    </Modal>}
    {consumeOpen && <Modal className="quota-consume-modal" title="确认使用重置卡" onClose={() => setConsumeOpen(false)} onSubmit={consume} submitLabel={consuming ? "正在使用…" : "确认使用 1 张"} submitDisabled={consuming}>
      <div className="quota-consume-confirm">
        <p>将使用当前账号的一张官方额度重置卡。此操作会立即消耗该卡，无法撤销。</p>
        <p>成功后系统会重新获取五小时、周额度和剩余重置卡信息。</p>
        {consumeError && <div className="error" role="alert">{consumeError}</div>}
      </div>
    </Modal>}
  </>;
};

const releasableHoldReasons = ["over_reserve", "admin_action", "admin_pause", "legacy_review"];
const HoldActions: React.FC<{ row: any; reload: () => void }> = ({ row, reload }) => {
  const [open, setOpen] = useState(false);
  const [holds, setHolds] = useState<any[]>([]);
  const [reason, setReason] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const load = async () => {
    try {
      const result = await api.get<{data: any[]}>(`/api/admin/accounts/${row.id}/holds`);
      const data = result.data ?? [];
      setHolds(data);
      setReason(data.find(hold => releasableHoldReasons.includes(hold.reason))?.reason ?? "");
      setError(""); setOpen(true);
    } catch (e) { setError(errorText(e)); setOpen(true); }
  };
  const release = async () => {
    if (!reason || !note.trim()) { setError("请先选择可解除的原因并填写处理依据。"); return; }
    try {
      await api.post(`/api/admin/accounts/${row.id}/holds`, {reason, note: note.trim()});
      setOpen(false); reload();
    } catch (e) { setError(errorText(e)); }
  };
  return <>
    <button className="btn small" onClick={load}>查看隔离原因</button>
    {open && <Modal title={`账号隔离原因 · ${row.label}`} onClose={() => setOpen(false)} onSubmit={release}>
      {error && <div className="error" role="alert">{error}</div>}
      {!holds.length && !error && <p className="muted">该账号当前没有隔离原因。</p>}
      {!!holds.length && <div className="hold-list">
        {holds.map(hold => <div key={hold.reason} className="hold-item">
          <b>{label(hold.reason)}</b>
          <span>{releasableHoldReasons.includes(hold.reason) ? "可在确认处理后解除。" : hold.reason === "reauth_required" ? "需要重新授权后才能解除。" : "需要先在对应流程中完成处理。"}</span>
        </div>)}
      </div>}
      {reason && <>
        <label className="field"><span className="field-label">解除原因</span><select value={reason} onChange={e => setReason(e.target.value)}>
          {holds.filter(hold => releasableHoldReasons.includes(hold.reason)).map(hold => <option key={hold.reason} value={hold.reason}>{label(hold.reason)}</option>)}
        </select></label>
        <label className="field"><span className="field-label">处理依据</span><textarea value={note} onChange={e => setNote(e.target.value)} rows={3} required placeholder="例如：已核对费用明细，确认可以恢复使用" /></label>
      </>}
    </Modal>}
  </>;
};

const proxiesPage = () => (
  <ResourcePage
    title="代理出口"
    basePath="/api/admin/proxies"
    columns={[
      { name: "name", label: "名称" },
      { name: "kind", label: "类型", render: (r) => label(r.kind) },
      { name: "endpoint", label: "地址" },
      statusCol,
    ]}
    createFields={[
      { name: "name", label: "名称", required: true },
      { name: "kind", label: "类型", kind: "select", options: ["direct", "http", "socks5"], required: true },
      { name: "endpoint", label: "地址", placeholder: "主机地址:端口", help: "选择直接连接时无需填写" },
      { name: "username", label: "用户名（可选）" },
      { name: "password", label: "密码（可选）", kind: "password" },
    ]}
    editFields={[
      { name: "name", label: "名称", required: true },
      { name: "kind", label: "类型", kind: "select", options: ["direct", "http", "socks5"], required: true },
      { name: "endpoint", label: "地址", placeholder: "主机地址:端口", help: "选择直接连接时无需填写" },
      { name: "username", label: "新用户名", help: "不修改则保留原用户名" },
      { name: "password", label: "新密码", kind: "password", help: "留空则保留原密码" },
      { name: "clear_credentials", label: "清除代理用户名和密码", kind: "checkbox" },
      { name: "status", label: "状态", kind: "select", options: ["active", "disabled"], required: true },
    ]}
    rowActions={(row, reload) => (
      <><button className="btn small danger" onClick={async () => {
        if (!confirm(`确认删除代理“${row.name}”？`)) return;
        try { await api.del(`/api/admin/proxies/${row.id}`, {version: row.version}); reload(); }
        catch (err) { alert(errorText(err)); }
      }}>删除</button><button
        className="btn small"
        onClick={async () => {
          try {
            const r = await api.post<any>(`/api/admin/proxies/${row.id}/test`);
            alert(r.ok ? `探测成功 (${r.status ?? 204})` : `探测失败：${r.error}`);
            reload();
          } catch (err) { alert(errorText(err)); }
        }}
      >
        探测
      </button></>
    )}
  />
);

const egressPage = () => (
  <ResourcePage
    title="出口策略"
    basePath="/api/admin/egress-policies"
    columns={[
      { name: "name", label: "名称" },
      { name: "primary", label: "主出口" },
      { name: "failure_mode", label: "故障处理", render: (r) => <Badge value={r.failure_mode} /> },
      { name: "fallbacks", label: "备用代理", render: (r) => (r.fallbacks ?? []).join(" → ") || "未配置" },
    ]}
    createFields={[
      { name: "name", label: "名称", required: true },
      { name: "primary_proxy_id", label: "主代理", kind: "select", optionsPath: "/api/admin/proxies", required: true },
      {
        name: "failure_mode",
        label: "故障模式",
        kind: "select",
        options: ["stop", "fallback"],
        default: "stop",
        help: "故障时停止会终止请求；切换备用代理只使用下方选择的代理。",
      },
      { name: "fallback_proxy_ids", label: "备用代理", kind: "multi-select", optionsPath: "/api/admin/proxies", visibleWhen: (body) => body.failure_mode === "fallback" },
    ]}
  />
);

const groupsPage = () => (
  <ResourcePage
    title="服务分组"
    basePath="/api/admin/service-groups"
    columns={[
      { name: "name", label: "名称" },
      { name: "platform", label: "上游平台", render: (r) => r.platform === "codex" ? "Codex" : r.platform === "openai_compatible" ? "OpenAI 兼容" : r.platform === "composite" ? "组合分组" : r.platform },
      { name: "subscription_type", label: "用途", render: (r) => r.subscription_type === "subscription" ? "订阅服务" : "内部服务" },
      { name: "description", label: "说明", render: (r) => r.description || "—" },
      { name: "strategy", label: "调度", render: (r) => <Badge value={r.strategy ?? "round_robin"} /> },
      { name: "model_allowlist_enabled", label: "模型范围", render: (r) => r.model_allowlist_enabled ? "仅白名单" : "全部已同步模型" },
      statusCol,
      { name: "accounts", label: "账号", render: (r) => (r.accounts ?? []).join("、") || "—" },
    ]}
    createFields={[
      { name: "name", label: "服务分组名称", required: true },
      { name: "description", label: "说明", kind: "textarea" },
      { name: "platform", label: "上游平台", kind: "select", options: [{value:"codex",label:"Codex"},{value:"openai_compatible",label:"OpenAI 兼容"},{value:"anthropic",label:"Anthropic"},{value:"gemini",label:"Gemini"},{value:"composite",label:"组合分组"}], default: "codex" },
      { name: "subscription_type", label: "用途", kind: "select", options: [{value:"subscription",label:"订阅服务"},{value:"standard",label:"内部服务"}], default: "subscription", help: "订阅套餐只能绑定订阅服务分组。" },
      { name: "strategy", label: "调度策略", kind: "select", options: ["round_robin", "weighted_round_robin", "priority_failover"], default: "round_robin" },
      { name: "rate_multiplier", label: "计费倍率", kind: "number", min: 0, step: 0.01, default: 1, help: "服务分组的倍率优先于历史套餐倍率。" },
      { name: "daily_limit_usd", label: "每日额度（美元）", kind: "number", min: 0, step: 0.01, placeholder: "留空表示不限" },
      { name: "weekly_limit_usd", label: "每周额度（美元）", kind: "number", min: 0, step: 0.01, placeholder: "留空表示不限" },
      { name: "monthly_limit_usd", label: "每月额度（美元）", kind: "number", min: 0, step: 0.01, placeholder: "留空表示不限" },
      { name: "default_validity_days", label: "默认有效天数", kind: "number", min: 1, default: 30 },
      { name: "model_allowlist_enabled", label: "仅允许模型白名单", kind: "checkbox", default: false },
    ]}
    editFields={[
      { name: "name", label: "服务分组名称", required: true },
      { name: "description", label: "说明", kind: "textarea" },
      { name: "platform", label: "上游平台", kind: "select", options: [{value:"codex",label:"Codex"},{value:"openai_compatible",label:"OpenAI 兼容"},{value:"anthropic",label:"Anthropic"},{value:"gemini",label:"Gemini"},{value:"composite",label:"组合分组"}] },
      { name: "subscription_type", label: "用途", kind: "select", options: [{value:"subscription",label:"订阅服务"},{value:"standard",label:"内部服务"}] },
      { name: "strategy", label: "调度策略", kind: "select", options: ["round_robin", "weighted_round_robin", "priority_failover"] },
      { name: "rate_multiplier", label: "计费倍率", kind: "number", min: 0, step: 0.01 },
      { name: "daily_limit_usd", label: "每日额度（美元）", kind: "number", min: 0, step: 0.01, placeholder: "留空表示不限" },
      { name: "weekly_limit_usd", label: "每周额度（美元）", kind: "number", min: 0, step: 0.01, placeholder: "留空表示不限" },
      { name: "monthly_limit_usd", label: "每月额度（美元）", kind: "number", min: 0, step: 0.01, placeholder: "留空表示不限" },
      { name: "default_validity_days", label: "默认有效天数", kind: "number", min: 1 },
      { name: "model_allowlist_enabled", label: "仅允许模型白名单", kind: "checkbox" },
      { name: "status", label: "状态", kind: "select", options: ["active", "disabled"] },
    ]}
    notice={<GroupAddForm />}
    rowActions={(row, reload) => <GroupRowActions row={row} reload={reload} />}
  />
);

const GroupAddForm: React.FC = () => <span className="muted small">账号能力、模型同步、映射和白名单都属于服务分组；套餐不再直接绑定账号。</span>;

const ServiceGroupModels: React.FC<{ row:any; reload:()=>void }> = ({row,reload}) => {
  const [open,setOpen] = React.useState(false), [models,setModels] = React.useState<any[]>([]), [rules,setRules] = React.useState<any[]>([]), [routes,setRoutes] = React.useState<any[]>([]), [targets,setTargets] = React.useState<any[]>([]), [busy,setBusy] = React.useState(false), [error,setError] = React.useState(""), [routeForm,setRouteForm] = React.useState({public_model:"",target_group_id:"",upstream_model:""});
  const load = async () => { try { const [candidate,configured,mapped,groupList] = await Promise.all([api.get<any>(`/api/admin/service-groups/${row.id}/models`),api.get<any>(`/api/admin/service-groups/${row.id}/model-rules`),api.get<any>(`/api/admin/service-groups/${row.id}/model-routes`),api.get<any>("/api/admin/service-groups")]); setModels(candidate.data ?? []); setRules(configured.data ?? []); setRoutes(mapped.data ?? []); const concrete=(groupList.data ?? []).filter((group:any) => group.id !== row.id && group.status === "active" && group.platform !== "composite"); setTargets(concrete); setRouteForm(current => ({...current,target_group_id:current.target_group_id || concrete[0]?.id || ""})); setError(""); } catch (e) { setError(errorText(e)); } };
  const show = async () => { setOpen(true); await load(); };
  const sync = async () => { setBusy(true); try { await api.post(`/api/admin/service-groups/${row.id}/models/sync`); await load(); reload(); } catch (e) { setError(errorText(e)); } finally { setBusy(false); } };
  const toggle = async (model:string, checked:boolean) => { try { await api.post(`/api/admin/service-groups/${row.id}/model-rules`,{public_model:model,status:checked ? "active" : "disabled"}); await load(); } catch (e) { setError(errorText(e)); } };
  const saveRoute = async () => { try { await api.post(`/api/admin/service-groups/${row.id}/model-routes`,routeForm); setRouteForm({public_model:"",target_group_id:targets[0]?.id ?? "",upstream_model:""}); await load(); } catch (e) { setError(errorText(e)); } };
  const deleteRoute = async (model:string) => { try { await api.del(`/api/admin/service-groups/${row.id}/model-routes?model=${encodeURIComponent(model)}`); await load(); } catch (e) { setError(errorText(e)); } };
  const active = new Set(rules.filter(rule => rule.status === "active").map(rule => rule.public_model));
  return <><button className="btn small" onClick={show}>模型配置</button>{open && <Modal className="plan-modal" title={`模型配置 · ${row.name}`} onClose={() => setOpen(false)} onSubmit={row.platform === "composite" ? () => setOpen(false) : sync} submitLabel={row.platform === "composite" ? "完成" : busy ? "正在同步…" : "同步上游模型"} submitDisabled={busy}>
    <p className="muted">模型目录包含账号实时清单与平台预置模型；平台预置条目不代表当前账号已支持。{row.model_allowlist_enabled ? "当前已启用白名单，仅勾选模型可被调用。" : "当前未启用白名单，所有已同步模型都可被调用。"}</p>
    {row.platform === "composite" ? <section className="section-card"><div className="section-title"><h2>组合模型路由</h2><span className="muted small">每个公开模型明确指向一个具体服务分组。</span></div><div className="form-grid"><label className="field"><span className="field-label">公开模型</span><input value={routeForm.public_model} placeholder="例如：gpt-5.6" onChange={e=>setRouteForm({...routeForm,public_model:e.target.value})}/></label><label className="field"><span className="field-label">目标服务分组</span><select value={routeForm.target_group_id} onChange={e=>setRouteForm({...routeForm,target_group_id:e.target.value})}>{targets.map(target=><option key={target.id} value={target.id}>{target.name} · {target.platform}</option>)}</select></label><label className="field"><span className="field-label">目标上游模型（可选）</span><input value={routeForm.upstream_model} placeholder="留空沿用公开模型" onChange={e=>setRouteForm({...routeForm,upstream_model:e.target.value})}/></label></div><button type="button" className="btn small primary" disabled={!routeForm.public_model || !routeForm.target_group_id} onClick={saveRoute}>保存模型路由</button>{routes.length ? <table className="tbl"><thead><tr><th>公开模型</th><th>目标分组</th><th>目标模型</th><th/></tr></thead><tbody>{routes.map(route=><tr key={route.id}><td>{route.public_model}</td><td>{targets.find(target=>target.id===route.target_group_id)?.name ?? route.target_group_id}</td><td>{route.upstream_model || route.public_model}</td><td><button type="button" className="btn small danger" onClick={()=>deleteRoute(route.public_model)}>删除</button></td></tr>)}</tbody></table> : <div className="empty-state">尚未添加模型路由。</div>}</section> : <div className="model-check-list">{models.length ? models.map(model => <label key={model.model}><input type="checkbox" disabled={!row.model_allowlist_enabled || model.selectable === false} checked={model.selectable !== false && (!row.model_allowlist_enabled || active.has(model.model))} onChange={e => toggle(model.model,e.target.checked)}/><span>{model.model}</span><small>{model.source === "platform" ? (model.model.startsWith("gpt-image-") ? "平台目录 · 生图接口尚未接入" : "平台目录 · 账号尚未同步到") : `账号实时清单 · ${model.account_count} 个账号`}</small></label>) : <div className="empty-state">尚未同步到上游模型。确认账号授权和出口策略后点击“同步上游模型”。</div>}</div>}
    {row.model_allowlist_enabled && !models.length && <p className="muted small">请先同步模型，再勾选白名单。若需要关闭白名单，请编辑服务分组。</p>}
    {error && <div className="error">{error}</div>}
  </Modal>}</>;
};

const GroupRowActions: React.FC<{ row: any; reload: () => void }> = ({ row, reload }) => {
  const [accounts, setAccounts] = React.useState<any[]>([]);
  const [accountId, setAccountId] = React.useState("");
  const [error, setError] = React.useState("");
  React.useEffect(() => {
    api.get<any>("/api/admin/accounts?limit=100").then(r => {
      const rows = (r.data ?? []).filter((account: any) => account.state === "active" && (row.platform === "composite" ? false : account.provider === row.platform));
      setAccounts(rows);
      setAccountId(rows[0]?.id ?? "");
    }).catch(e => setError(errorText(e)));
  }, []);
  return (
    <span className="inline-actions">
      <ServiceGroupModels row={row} reload={reload}/>
      <select aria-label="选择要添加的账号"
        value={accountId}
        onChange={(e) => setAccountId(e.target.value)}
      >
        <option value="">请选择账号</option>
        {accounts.map(account => <option key={account.id} value={account.id}>{account.label}</option>)}
      </select>
      <button
        className="btn small"
        onClick={async () => {
          if (!accountId) return;
          try { await api.post(`/api/admin/service-groups/${row.id}/accounts`, { account_id: accountId }); reload(); }
          catch (e) { setError(errorText(e)); }
        }}
      >
        添加账号
      </button>
      {(row.account_ids ?? []).map((id: string, index: number) => <button
        key={id}
        className="btn small"
        title={`从账号池移除“${row.accounts?.[index] ?? "账号"}”`}
        onClick={async () => {
          if (!confirm(`确认从服务分组“${row.name}”移除账号“${row.accounts?.[index] ?? "该账号"}”？`)) return;
          try { await api.del(`/api/admin/service-groups/${row.id}/accounts/${id}`); reload(); }
          catch (e) { setError(errorText(e)); }
        }}
      >
        移除 {row.accounts?.[index] ?? "账号"}
      </button>)}
      <button
        className="btn small danger"
        onClick={async () => {
          if (!confirm(`确认删除服务分组“${row.name}”？`)) return;
          try { await api.del(`/api/admin/service-groups/${row.id}`); reload(); }
          catch (e) { setError(errorText(e)); }
        }}
      >
        删除服务分组
      </button>
      {error && <span className="error small">{error}</span>}
    </span>
  );
};

const budgetsPage = () => (
  <ResourcePage
    title="预算策略"
    editFields={[{ name: "status", label: "状态", kind: "select", options: ["active", "disabled"] }]}
    basePath="/api/admin/budget-policies"
    columns={[
      { name: "owner_type", label: "作用域" },
      { name: "period", label: "周期" },
      { name: "mode", label: "模式" },
      { name: "amount", label: "固定金额（美元）", render: r => money(r.amount, "不适用") },
      { name: "percent_bps", label: "比例", render: r => cellText("percent_bps", r.percent_bps) },
      { name: "timezone", label: "时区" },
      statusCol,
    ]}
    createFields={[
      {
        name: "owner_type",
        label: "作用域",
        kind: "select",
        options: ["member", "key", "account", "group", "key_account", "key_group"],
        required: true,
      },
      { name: "owner_member_id", label: "用户", kind: "select", optionsPath: "/api/admin/users", visibleWhen: b => b.owner_type === "member" || b.owner_type === "key_account" || b.owner_type === "key_group" },
      { name: "owner_key_id", label: "接口密钥", kind: "select", optionsPath: "/api/admin/keys", visibleWhen: b => b.owner_type === "key" || b.owner_type === "key_account" || b.owner_type === "key_group" },
      { name: "owner_account_id", label: "上游账号", kind: "select", optionsPath: "/api/admin/accounts", visibleWhen: b => b.owner_type === "account" || b.owner_type === "key_account" },
      { name: "owner_group_id", label: "账号池", kind: "select", optionsPath: "/api/admin/account-pools", visibleWhen: b => b.owner_type === "group" || b.owner_type === "key_group" },
      { name: "period", label: "周期", kind: "select", options: ["day", "week"], required: true },
      { name: "timezone", label: "时区", kind: "select", options: [{ value: "Asia/Shanghai", label: "北京时间" }, { value: "UTC", label: "协调世界时" }], default: "Asia/Shanghai" },
      { name: "mode", label: "模式", kind: "select", options: ["fixed", "percent"], required: true },
      { name: "amount", label: "固定金额（美元）", placeholder: "例如：100.00", visibleWhen: b => b.mode === "fixed" },
      { name: "percent_bps", label: "比例（%）", kind: "number", min: 0, max: 100, step: 0.01, scale: 100, visibleWhen: b => b.mode === "percent", help: "例如填写 20，表示使用基础策略金额的 20%。" },
      { name: "base_policy_id", label: "基础预算策略", kind: "select", optionsPath: "/api/admin/budget-policies", optionsFilter: r => r.mode === "fixed" && r.status === "active", visibleWhen: b => b.mode === "percent", help: "请选择同一周期的已启用固定金额策略。" },
    ]}
    notice={<span className="muted small">金额以美元计。按比例策略基于一项固定金额策略计算；没有适用预算的请求会被拒绝。</span>}
  />
);

const periodsPage = () => (
  <ResourcePage
    title="预算周期"
    basePath="/api/admin/budget-periods"
    columns={[
      { name: "policy_id", label: "策略" },
      { name: "period_start", label: "开始" },
      { name: "period_end", label: "结束" },
      { name: "limit", label: "周期限额（美元）", render: r => money(r.limit, "未设置") },
      { name: "spent", label: "已使用（美元）", render: r => money(r.spent, "0 美元") },
      { name: "reserved", label: "已预留（美元）", render: r => money(r.reserved, "0 美元") },
    ]}
    notice={<span className="muted small">周期限额创建后不可修改；已使用与已预留之和为当前占用额度。</span>}
  />
);

const ledgerPage = () => (
  <ResourcePage
    title="用量账本"
    basePath="/api/admin/ledger"
    columns={[
      { name: "created_at", label: "时间" },
      { name: "request_id", label: "请求" },
      { name: "entry_type", label: "类型" },
      { name: "input_tokens", label: "非缓存输入词元" },
      { name: "cached_input_tokens", label: "缓存输入词元" },
      { name: "output_tokens", label: "输出词元" },
      { name: "cost", label: "费用（美元）", render: r => money(r.cost, "0 美元") },
    ]}
    notice={<span className="muted small">账本只追加记录；如需修正，会新增一条费用调整记录，不会直接修改原记录。</span>}
  />
);

const pricesPage = () => (
  <ResourcePage
    title="价格版本"
    basePath="/api/admin/prices/versions"
    columns={[
      { name: "created_at", label: "时间" },
      { name: "origin", label: "来源", render: (r) => <Badge value={r.origin} /> },
      { name: "status", label: "状态", render: (r) => <Badge value={r.status} /> },
      { name: "model_count", label: "模型数" },
      { name: "source_url", label: "来源地址" },
      { name: "notes", label: "说明" },
    ]}
    notice={<PricesActions />}
  />
);

const PricesActions: React.FC = () => {
  const [msg, setMsg] = useState("");
  return (
    <span>
      <button
        className="btn"
        onClick={async () => {
          const r = await api.post<any>("/api/admin/prices/sync");
          setMsg(`同步结果：${label(r.status)}。未验证的官方数据不会自动启用。`);
        }}
      >
        同步官方价格
      </button>{" "}
      <span className="muted small">{msg || "测试价格仅用于测试环境；生产环境需要已验证的官方价格。"}</span>
    </span>
  );
};

const auditRulesPage = () => (
  <ResourcePage
    title="审核规则"
    basePath="/api/admin/audit/rules"
    columns={[
      { name: "rule_id", label: "规则" },
      { name: "category", label: "类别", render: (r) => <Badge value={r.category} /> },
      { name: "title", label: "标题" },
      { name: "action", label: "动作", render: (r) => <Badge value={r.action} /> },
      { name: "enabled", label: "启用", render: (r) => (r.enabled ? "已启用" : "已停用") },
      { name: "version", label: "版本" },
      { name: "source", label: "来源" },
    ]}
    editFields={[
      { name: "enabled", label: "启用", kind: "checkbox" },
      { name: "action", label: "动作", kind: "select", options: ["flag", "review", "block", "reject", "unsupported"] },
      { name: "matcher", label: "匹配规则", kind: "textarea", help: "保存前会验证规则格式。" },
      { name: "message", label: "提示", kind: "textarea" },
    ]}
    notice={<RuleTester />}
  />
);

const RuleTester: React.FC = () => {
  const [text, setText] = useState("");
  const [result, setResult] = useState<string>("");
  return (
    <span className="rule-tester">
      <input
        placeholder="输入测试文本（仅本地规则，不发送外部）"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <button
        className="btn"
        onClick={async () => {
          const r = await api.post<any>("/api/admin/audit/rules/validate", { text, scope: "input" });
          const hitCount = Array.isArray(r.hits) ? r.hits.length : 0;
          setResult(r.secret_blocked ? "检测到敏感凭据，规则会拦截该内容。" : hitCount ? `命中 ${hitCount} 条规则。` : "未命中规则。");
        }}
      >
        测试
      </button>
      <span className="small">{result}</span>
    </span>
  );
};

const auditEventsPage = () => (
  <ResourcePage
    title="审核事件"
    basePath="/api/admin/audit/events"
    columns={[
      { name: "created_at", label: "时间" },
      { name: "request_id", label: "请求" },
      { name: "decision", label: "决定", render: (r) => <Badge value={r.decision} /> },
      { name: "coverage", label: "覆盖" },
      { name: "cache_state", label: "缓存" },
      { name: "duration_ms", label: "耗时（毫秒）" },
      { name: "summary", label: "摘要（脱敏）" },
      { name: "reviews", label: "复核", render: (r) => (r.reviews ?? []).length + " 条" },
    ]}
    rowActions={(row, reload) => <AuditReviewAction row={row} reload={reload} />}
    notice={<span className="muted small">复核只记录结论或创建精确例外，绝不重放请求。</span>}
  />
);

const AuditReviewAction: React.FC<{ row: any; reload: () => void }> = ({ row, reload }) => {
  const [open, setOpen] = useState(false);
  const [outcome, setOutcome] = useState("false_positive");
  const [note, setNote] = useState("");
  const [ruleID, setRuleID] = useState("");
  const [hours, setHours] = useState("24");
  const [error, setError] = useState("");
  const submit = async () => {
    const ttl = Number(hours);
    if (outcome === "exception_created" && !ruleID.trim()) { setError("创建例外时必须填写命中的规则编号。"); return; }
    if (outcome === "exception_created" && (!Number.isInteger(ttl) || ttl < 1 || ttl > 720)) { setError("例外有效期必须是 1 到 720 小时之间的整数。"); return; }
    try {
      await api.post(`/api/admin/audit/events/${row.id}/review`, {
        outcome, note: note.trim(),
        exception_rule_id: outcome === "exception_created" ? ruleID.trim() : undefined,
        exception_ttl_hours: outcome === "exception_created" ? ttl : undefined,
      });
      setOpen(false); reload();
    } catch (e) { setError(errorText(e)); }
  };
  return <>
    <button className="btn small" onClick={() => { setError(""); setOpen(true); }}>复核</button>
    {open && <Modal title="审核复核" onClose={() => setOpen(false)} onSubmit={submit}>
      {error && <div className="error" role="alert">{error}</div>}
      <label className="field"><span className="field-label">复核结论</span><select value={outcome} onChange={e => setOutcome(e.target.value)}>
        <option value="confirmed_violation">确认违规</option><option value="false_positive">判定误报</option><option value="exception_created">创建精确例外</option>
      </select></label>
      {outcome === "exception_created" && <>
        <label className="field"><span className="field-label">命中的规则编号</span><input value={ruleID} onChange={e => setRuleID(e.target.value)} placeholder="请从该事件命中的规则中复制" required /></label>
        <label className="field"><span className="field-label">例外有效期（小时）</span><input type="number" min="1" max="720" value={hours} onChange={e => setHours(e.target.value)} required /></label>
      </>}
      <label className="field"><span className="field-label">复核说明（可选）</span><textarea value={note} onChange={e => setNote(e.target.value)} rows={3} /></label>
    </Modal>}
  </>;
};

const adminEventsPage = () => (
  <ResourcePage
    title="管理事件"
    basePath="/api/admin/admin-events"
    columns={[
      { name: "created_at", label: "时间" },
      { name: "actor", label: "操作者" },
      { name: "action", label: "操作", render: r => label(r.action, "action") },
      { name: "target_type", label: "对象", render: r => label(r.target_type) },
      { name: "target_id", label: "对象编号" },
    ]}
  />
);

const RequestTrend: React.FC<{ series: any[] }> = ({ series }) => {
  if (!series.length) return <div className="empty-state">所选时间范围内还没有请求记录。</div>;
  const peak = Math.max(1, ...series.map(item => Number(item.requests) || 0));
  const points = series.map((item, index) => {
    const x = series.length === 1 ? 50 : (index / (series.length - 1)) * 100;
    const y = 92 - ((Number(item.requests) || 0) / peak) * 80;
    return `${x},${y}`;
  }).join(" ");
  const last = series[series.length - 1];
  return <div className="request-trend"><svg viewBox="0 0 100 100" preserveAspectRatio="none" role="img" aria-label={`请求趋势，最高 ${peak} 次请求`}><polyline points={points} fill="none" stroke="var(--accent)" strokeWidth="3" vectorEffect="non-scaling-stroke" strokeLinejoin="round" strokeLinecap="round" /></svg><div className="trend-scale"><span>{series[0]?.start ? dateTime(series[0].start) : ""}</span><span>峰值 {peak}</span><span>{last?.start ? dateTime(last.start) : ""}</span></div></div>;
};

const RecentEvents: React.FC = () => {
  const [events, setEvents] = useState<any[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    let mounted = true;
    const load = async () => { try { const response = await api.get<any>("/api/admin/events"); if (mounted) { setEvents(response.data ?? []); setError(""); } } catch (cause) { if (mounted) setError(errorText(cause)); } };
    void load();
    const timer = window.setInterval(() => { void load(); }, 30000);
    return () => { mounted = false; window.clearInterval(timer); };
  }, []);
  if (error) return <div className="muted small">事件暂不可用：{error}</div>;
  if (!events.length) return <div className="empty-state">最近 24 小时没有可展示的管理或审核事件。</div>;
  return <div className="recent-events">{events.slice(0, 8).map(event => <div className="recent-event" key={`${event.kind}:${event.id}`}><span className={`event-kind event-kind-${event.kind}`}>{event.kind === "audit" ? "审" : "管"}</span><div><b>{label(event.title, event.kind === "audit" ? "" : "action")}</b><small>{event.description || "系统事件"}</small></div><time>{dateTime(event.created_at)}</time></div>)}</div>;
};

const statusPage = () => {
  const [range, setRange] = useState<"24h" | "7d">("24h");
  const [s, setS] = useState<any>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let mounted = true;
    const load = async () => {
      try {
        const next = await api.get<any>(`/api/admin/dashboard?range=${range}`);
        if (mounted) { setS(next); setError(""); }
      } catch (cause) { if (mounted) setError(errorText(cause)); }
    };
    void load();
    const timer = window.setInterval(() => { void load(); }, 30000);
    return () => { mounted = false; window.clearInterval(timer); };
  }, [range]);
  if (!s) return <div className="status-loading" aria-live="polite">正在加载控制塔数据…</div>;
  const accountStates = new Map<string, number>((s.accounts ?? []).map((account: any) => [String(account.state), Number(account.count) || 0]));
  const activeAccounts = accountStates.get("active") ?? 0;
  const inactiveAccounts = [...accountStates.entries()].filter(([state]) => state !== "active").reduce((total, [, count]) => total + count, 0);
  const requests = (s.series ?? []).reduce((total: number, item: any) => total + (Number(item.requests) || 0), 0);
  const succeeded = (s.series ?? []).reduce((total: number, item: any) => total + (Number(item.succeeded) || 0), 0);
  const successRate = requests ? `${((succeeded / requests) * 100).toFixed(1)}%` : "—";
  return <div className="status-dashboard">
    <div className="page-head"><div><p className="eyebrow">CONTROL TOWER</p><h2>系统状态</h2></div><div className="grow" /><div className="range-switch" aria-label="统计范围"><button className={range === "24h" ? "active" : ""} type="button" onClick={() => setRange("24h")}>24 小时</button><button className={range === "7d" ? "active" : ""} type="button" onClick={() => setRange("7d")}>7 天</button></div><Badge value={s.production_ready ? "true" : "false"} domain="readiness" /></div>
    <p className="muted status-description">数据截至 {dateTime(s.as_of)}；聚合粒度为{range === "24h" ? "小时" : "天"}。</p>
    {error && <div className="error" role="alert">状态更新失败：{error}</div>}
    <section className="status-card-grid" aria-label="运行摘要"><div className="status-card"><span>服务就绪</span><b>{s.production_ready ? "正常" : "受阻"}</b><small>{s.production_ready ? "生产流量可接入" : "请检查阻塞原因"}</small></div><div className="status-card"><span>请求数</span><b>{requests}</b><small>成功率 {successRate}</small></div><div className="status-card"><span>待确认结果</span><b>{Number(s.unknown_requests) || 0}</b><small>需要完成结果核对</small></div><div className="status-card"><span>可用上游账号</span><b>{activeAccounts}</b><small>{inactiveAccounts ? `${inactiveAccounts} 个非活跃账号` : "所有已登记账号均活跃"}</small></div></section>
    <section className="dashboard-grid"><div className="status-panel"><div className="panel-heading"><h3>请求趋势</h3><span className="muted small">仅含已记录请求</span></div><RequestTrend series={s.series ?? []} /></div><div className="status-panel"><div className="panel-heading"><h3>配额快照</h3><span className="muted small">过期阈值 {Math.round((Number(s.quota?.stale_after_seconds) || 0) / 60)} 分钟</span></div><div className="quota-kpis"><b>{Number(s.quota?.coverage) || 0}<small>已同步</small></b><b className={s.quota?.stale ? "warn" : ""}>{Number(s.quota?.stale) || 0}<small>已过期</small></b></div></div></section>
    {!s.production_ready && (s.not_ready_reasons ?? []).length > 0 && <section className="status-panel"><h3>阻塞原因</h3><ul>{s.not_ready_reasons.map((reason: string) => <li key={reason}>{reason}</li>)}</ul></section>}
    <section className="dashboard-grid"><div className="status-panel"><h3>账号状态</h3><div className="status-state-list">{(s.accounts ?? []).length ? (s.accounts ?? []).map((account: any) => <span key={account.state}><Badge value={account.state} domain="account" /> <b>{account.count}</b></span>) : <span className="muted">尚未登记上游账号</span>}</div></div><div className="status-panel"><h3>最近事件</h3><RecentEvents /></div></section>
  </div>;
};

const oauthPage = () => {
  const [url, setUrl] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [status, setStatus] = useState("");
	const [reuseAccountID, setReuseAccountID] = useState("");
  return (
    <div>
      <h2>授权会话</h2>
      <p className="muted small">授权会话有效期为十分钟且只能使用一次。回调地址必须与上游服务已登记的地址一致。</p>
	<label>
		复用已有账号（重新授权，可选）
		<input value={reuseAccountID} placeholder="请输入账号编号" onChange={(e) => setReuseAccountID(e.target.value)} />
	</label>
      <button
        className="btn primary"
        onClick={async () => {
			const r = await api.post<any>("/api/admin/accounts/oauth/sessions", reuseAccountID ? { reuse_account_id: reuseAccountID } : {});
          setUrl(r.authorize_url);
          setSessionId(r.id);
        }}
      >
        发起授权
      </button>
      {url && (
        <p>
          <a href={url} target="_blank" rel="noreferrer">
            打开授权链接
          </a>{" "}
          <span className="muted small">（会话 {sessionId}）</span>
        </p>
      )}
      {sessionId && (
        <button
          className="btn"
          onClick={async () => {
            const r = await api.get<any>(`/api/admin/accounts/oauth/sessions/${sessionId}`);
            setStatus(`${label(r.status, "oauth")}${r.account_id ? " · 已关联账号" : ""}`);
          }}
        >
          查询状态
        </button>
      )}
      {status && <p>{status}</p>}
    </div>
  );
};

const OAuthStarter: React.FC = () => {
  const [url, setUrl] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [status, setStatus] = useState("");
  const [callbackURL, setCallbackURL] = useState("");
  const [message, setMessage] = useState("");
  return <span className="oauth-starter">
    <span className="rule-tester">
      <button className="btn" onClick={async () => {
        try {
          const r = await api.post<any>("/api/admin/accounts/oauth/sessions", {});
          setUrl(r.authorize_url); setSessionId(r.id); setStatus("pending"); setMessage(""); setCallbackURL("");
        } catch (e: any) { setMessage(e.message); }
      }}>授权接入</button>
      {url && <a className="btn small" href={url} target="_blank" rel="noreferrer">打开授权页</a>}
      {sessionId && <button className="btn small" onClick={async()=>{
        try { const r=await api.get<any>(`/api/admin/accounts/oauth/sessions/${sessionId}`); setStatus(r.status); if (r.status === "completed") window.dispatchEvent(new Event("subai:refresh:/api/admin/accounts")); }
        catch (e: any) { setMessage(e.message); }
      }}>查询状态</button>}
      {status && <span className="muted small">状态：{label(status, "oauth")}</span>}
    </span>
    {sessionId && <span className="oauth-callback-row">
      <input
        aria-label="授权回调地址"
        placeholder="粘贴浏览器地址栏中的完整回调地址"
        value={callbackURL}
        onChange={(e)=>setCallbackURL(e.target.value)}
      />
      <button className="btn small primary" disabled={!callbackURL.trim()} onClick={async()=>{
        try {
          const r=await api.post<any>(`/api/admin/accounts/oauth/sessions/${sessionId}/callback`, {callback_url: callbackURL.trim()});
          setStatus("completed"); setMessage(r.quota_synced ? "授权成功，账号信息和官方额度已同步" : "授权成功，官方额度暂未同步，可在额度详情中重试"); setCallbackURL("");
          window.dispatchEvent(new Event("subai:refresh:/api/admin/accounts"));
        } catch (e: any) { setMessage(e.message); }
      }}>完成授权</button>
    </span>}
    {sessionId && <span className="muted small">登录成功后浏览器会跳回本机地址；若页面无法打开，请复制地址栏中的完整链接并粘贴到这里。</span>}
    {message && <span className={status === "completed" ? "small" : "error small"}>{message}</span>}
  </span>;
};

const routesPage: React.FC<{ keyId: string }> = ({ keyId }) => (
  <ResourcePage
    title="接口密钥路由"
    basePath={`/api/admin/keys/${keyId}/routes`}
    columns={[
      { name: "target_type", label: "类型" },
      { name: "target_id", label: "目标编号" },
      { name: "priority", label: "优先级" },
    ]}
    createFields={[
      { name: "target_type", label: "类型", kind: "select", options: ["account", "group"], required: true },
      { name: "target_id", label: "目标", kind: "select", optionsPath: "/api/admin/accounts", required: true, visibleWhen: body => body.target_type === "account" },
      { name: "target_id", label: "目标", kind: "select", optionsPath: "/api/admin/account-pools", required: true, visibleWhen: body => body.target_type === "group" },
      { name: "priority", label: "优先级", kind: "number", default: 100 },
    ]}
    rowActions={(row, reload) => (
      <button
        className="btn small danger"
        onClick={async () => {
          await api.del(`/api/admin/keys/${keyId}/routes?route_id=${row.id}`);
          reload();
        }}
      >
        删除
      </button>
    )}
  />
);

// ── App shell ────────────────────────────────────────────────────────────────

type NavItem = { key: string; label: string; page: React.ComponentType<any> };
type NavGroup = { label?: string; items: NavItem[] };

const ADMIN_NAV_GROUPS: NavGroup[] = [
  { items: [
    { key: "#/status", label: "仪表盘", page: statusPage },
    { key: "#/ledger", label: "用量账本", page: ledgerPage },
  ] },
  { label: "用户与订阅", items: [
    { key: "#/users", label: "用户管理", page: membersPage },
    { key: "#/plans", label: "套餐管理", page: AdminPlans },
    { key: "#/subscriptions", label: "订阅分配", page: AdminSubscriptions },
  ] },
  { label: "资源池", items: [
    { key: "#/accounts", label: "上游账号", page: accountsPage },
    { key: "#/pools", label: "服务分组", page: groupsPage },
    { key: "#/proxies", label: "代理出口", page: proxiesPage },
  ] },
  { label: "策略", items: [
    { key: "#/egress", label: "出口策略", page: egressPage },
    { key: "#/budgets", label: "预算策略", page: budgetsPage },
    { key: "#/periods", label: "预算周期", page: periodsPage },
    { key: "#/prices", label: "价格版本", page: pricesPage },
  ] },
  { label: "审计", items: [
    { key: "#/audit-rules", label: "审核规则", page: auditRulesPage },
    { key: "#/audit-events", label: "审核事件", page: auditEventsPage },
    { key: "#/admin-events", label: "管理事件", page: adminEventsPage },
  ] },
];

const ADMIN_NAV = ADMIN_NAV_GROUPS.flatMap((group) => group.items);

const USER_NAV: NavItem[] = [
  { key: "#/overview", label: "我的概览", page: MyOverview },
  { key: "#/my-keys", label: "我的接口密钥", page: MyKeys },
  { key: "#/my-subscriptions", label: "我的订阅", page: MySubscriptions },
  { key: "#/security", label: "账户安全", page: SecurityPage },
];

type Identity = { member_id: string; name: string; role: "admin" | "member" };
type BuildInfo = { version: string; revision: string; built_at: string };
type Theme = "dark" | "light";

function preferredTheme(): Theme {
  const saved = window.localStorage.getItem("subai-theme");
  if (saved === "light" || saved === "dark") return saved;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

const ThemeToggle: React.FC<{ theme: Theme; onToggle: () => void; compact?: boolean }> = ({ theme, onToggle, compact = false }) => {
  const nextLabel = theme === "dark" ? "切换到浅色主题" : "切换到深色主题";
  return <button className={`theme-toggle${compact ? " compact" : ""}`} type="button" onClick={onToggle} aria-label={nextLabel} title={nextLabel}>
    <span aria-hidden="true">{theme === "dark" ? "☀" : "☾"}</span>
    {!compact && <span>{theme === "dark" ? "浅色" : "深色"}</span>}
  </button>;
};

const VersionStamp: React.FC<{ build: BuildInfo | null }> = ({ build }) => {
  if (!build) return <div className="app-version muted">版本未知</div>;
  const revision = build.revision && build.revision !== "unknown"
    ? `${build.revision.slice(0, 12)}${build.revision.endsWith("-dirty") ? "-dirty" : ""}`
    : "开发构建";
  const version = build.version === "dev" ? "dev" : `v${build.version}`;
  return <div className="app-version" title={`构建时间：${build.built_at}\n完整修订：${build.revision}`}>
    <span>应用版本</span><b>{version}</b><code>{revision}</code>
  </div>;
};

const GlobalSearch: React.FC = () => {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<any[]>([]);
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); setOpen(true); }
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);
  useEffect(() => { if (open) window.setTimeout(() => inputRef.current?.focus(), 0); }, [open]);
  useEffect(() => {
    if (!open || query.trim().length < 2) { setResults([]); setError(""); return; }
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      try { const response = await api.get<any>(`/api/admin/search?q=${encodeURIComponent(query.trim())}`, { signal: controller.signal }); setResults(response.data ?? []); setError(""); }
      catch (cause: any) { if (cause?.name !== "AbortError") { setResults([]); setError(errorText(cause)); } }
    }, 180);
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [open, query]);
  return <><button className="btn small" type="button" onClick={() => setOpen(true)}>搜索 <kbd>⌘K</kbd></button>{open && <div className="search-backdrop" onMouseDown={() => setOpen(false)}><section className="search-dialog" role="dialog" aria-modal="true" aria-label="全局搜索" onMouseDown={event => event.stopPropagation()}><label className="field"><span className="field-label">搜索用户、密钥、账号或账号池</span><input ref={inputRef} value={query} onChange={event => setQuery(event.target.value)} placeholder="至少输入两个字符，按名称或前缀搜索" /></label>{error && <div className="error">搜索失败：{error}</div>}{query.trim().length < 2 ? <p className="muted small">输入至少两个字符开始搜索。</p> : results.length ? <div className="search-results">{results.map(result => <a key={`${result.type}:${result.id}`} href={result.href} onClick={() => setOpen(false)}><Badge value={result.status} domain={result.type === "account" ? "account" : ""} /><span>{result.title}</span><small>{result.type}</small></a>)}</div> : <p className="muted small">没有匹配的资源。</p>}</section></div>}</>;
};

export const App: React.FC = () => {
  const [hash, setHash] = useState(location.hash || "");
  const [identity, setIdentity] = useState<Identity | null | undefined>(undefined);
  const [build, setBuild] = useState<BuildInfo | null>(null);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [theme, setTheme] = useState<Theme>(preferredTheme);
  useEffect(() => {
    const fn = () => setHash(location.hash || "#/status");
    window.addEventListener("hashchange", fn);
    return () => window.removeEventListener("hashchange", fn);
  }, []);
  const refreshIdentity = async () => {
    try { setIdentity(await api.get<Identity>("/api/admin/session")); }
    catch { setIdentity(null); }
  };
	useEffect(() => { refreshIdentity(); }, []);
	useEffect(() => { api.get<BuildInfo>("/api/version").then(setBuild).catch(() => setBuild(null)); }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    window.localStorage.setItem("subai-theme", theme);
  }, [theme]);

  const toggleTheme = () => setTheme((value) => value === "dark" ? "light" : "dark");

	if (identity === undefined) return <div className="login-wrap">检查登录状态…</div>;
  if (!identity) return <Login onLoggedIn={refreshIdentity} build={build} theme={theme} onToggleTheme={toggleTheme} />;

  const navGroups = identity.role === "admin"
    ? ADMIN_NAV_GROUPS
    : [{ label: "个人中心", items: USER_NAV }];
  const navItems = navGroups.flatMap((group) => group.items);
  const defaultHash = identity.role === "admin" ? "#/status" : "#/overview";
  const activeHash = hash || defaultHash;

  const routeMatch = activeHash.match(/^#\/routes\/(.+)$/);
  const userMatch = activeHash.match(/^#\/users\/([0-9a-f-]{36})$/i);
  let content: React.ReactNode;
  if (routeMatch && identity.role === "admin") {
    // Render as element types so each page keeps its own hook state — calling
    // page functions inline would splice their hooks into App's hook list and
    // crash on navigation.
    content = React.createElement(routesPage, { keyId: routeMatch[1] });
  } else if (userMatch && identity.role === "admin") {
    content = <AdminUserEntitlements memberId={userMatch[1]} />;
  } else {
    const nav = navItems.find((n) => activeHash.startsWith(n.key));
    content = nav ? React.createElement(nav.page) : <div className="muted">未知页面</div>;
  }

  const activeNav = navItems.find((n) => activeHash.startsWith(n.key));

  return (
    <div className={`layout control-shell ${sidebarCollapsed ? "sidebar-collapsed" : ""}`}>
      <aside className="control-sidebar">
        <div className="brand-row">
          <div className="brand">SubAI <small>{identity.role === "admin" ? "运营台" : "个人台"}</small></div>
          <button
            className="sidebar-toggle"
            type="button"
            onClick={() => setSidebarCollapsed((value) => !value)}
            aria-label={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
            title={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
          >
            {sidebarCollapsed ? "›" : "‹"}
          </button>
        </div>
        <nav aria-label={identity.role === "admin" ? "管理导航" : "个人导航"}>
          {navGroups.map((group, index) => (
            <div className="nav-group" key={group.label ?? `overview-${index}`}>
              {group.label && <div className="nav-group-label">{group.label}</div>}
              {group.items.map((n) => (
                <a key={n.key} href={n.key} className={activeHash.startsWith(n.key) ? "active" : ""}>
                  <span className="nav-item-marker" aria-hidden="true" />
                  <span>{n.label}</span>
                </a>
              ))}
            </div>
          ))}
        </nav>
        <div className="grow" />
        <VersionStamp build={build} />
        <div className="identity-name">{identity.name}<br/><span>{identity.role === "admin" ? "管理员" : "普通用户"}</span></div>
        <button
          className="btn"
          onClick={async () => {
            await api.del("/api/admin/session");
			setIdentity(null);
            location.hash = "";
          }}
        >
          退出登录
        </button>
      </aside>
      <div className="control-main">
        <header className="control-topbar">
          <div className="breadcrumb"><span>{identity.role === "admin" ? "运营台" : "个人台"}</span><b>{activeNav?.label ?? "未知页面"}</b></div>
          <div className="topbar-actions">
            {identity.role === "admin" && <GlobalSearch />}
            <button className="btn small" type="button" onClick={() => window.location.reload()}>刷新</button>
            <ThemeToggle theme={theme} onToggle={toggleTheme} compact />
          </div>
        </header>
        <main>{content}</main>
      </div>
    </div>
  );
};

const Login: React.FC<{ onLoggedIn: () => void | Promise<void>; build: BuildInfo | null; theme: Theme; onToggleTheme: () => void }> = ({ onLoggedIn, build, theme, onToggleTheme }) => {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  return (
    <div className="login-wrap">
      <div className="login-theme-control"><ThemeToggle theme={theme} onToggle={onToggleTheme} /></div>
      <form
        className="login"
        onSubmit={async (e) => {
          e.preventDefault();
          try {
			await api.post("/api/admin/session", { username, password });
            setError("");
			await onLoggedIn();
          } catch (err: any) {
            setError(err.message);
          }
        }}
      >
        <h2>SubAI 管理平台</h2>
        <label>
          用户名
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required />
        </label>
        <label>
          密码
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required />
        </label>
        {error && <div className="error">{error}</div>}
        <button className="btn primary" type="submit">
          登录
        </button>
        <p className="muted small">管理员和普通用户使用同一入口；系统会根据角色进入对应控制台。</p>
        <VersionStamp build={build} />
      </form>
    </div>
  );
};
