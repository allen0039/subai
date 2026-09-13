import React, { useEffect, useState } from "react";
import { api } from "./api";
import { Badge, ResourcePage, Field, Column } from "./components";

// ── Resource registry: every admin surface, driven by data (§17.2) ──────────

const statusCol: Column = { name: "status", label: "状态", render: (r) => <Badge value={r.status ?? ""} /> };

const membersPage = () => (
  <ResourcePage
    title="成员"
    basePath="/api/admin/members"
    columns={[
      { name: "name", label: "用户名" },
      { name: "role", label: "角色", render: (r) => <Badge value={r.role} /> },
      statusCol,
      { name: "created_at", label: "创建时间" },
    ]}
    createFields={[
      { name: "name", label: "用户名", required: true },
      { name: "password", label: "密码", kind: "password", required: true, help: "使用 bcrypt 存储，无默认密码" },
      { name: "role", label: "角色", kind: "select", options: ["member", "admin"], default: "member" },
    ]}
    editFields={[
      { name: "status", label: "状态", kind: "select", options: ["active", "disabled"] },
      { name: "role", label: "角色", kind: "select", options: ["member", "admin"] },
    ]}
  />
);

const clientsPage = () => (
  <ResourcePage
    title="客户端（设备 / Agent）"
    basePath="/api/admin/clients"
    columns={[
      { name: "name", label: "名称" },
      { name: "type", label: "类型", render: (r) => <Badge value={r.type} /> },
      statusCol,
      { name: "member_id", label: "成员" },
      { name: "key_count", label: "Key 数" },
    ]}
    createFields={[
      { name: "member_id", label: "成员 ID", required: true, placeholder: "uuid，见成员页" },
      { name: "name", label: "名称", required: true },
      { name: "type", label: "类型", kind: "select", options: ["computer", "hermes", "cli", "other"], required: true },
      { name: "notes", label: "备注", kind: "textarea" },
    ]}
    editFields={[
      { name: "status", label: "状态", kind: "select", options: ["active", "disabled"] },
      { name: "notes", label: "备注", kind: "textarea" },
    ]}
    notice={<span className="muted small">数量不设上限；三台电脑 + 两个 Hermes 只是首批样本。</span>}
  />
);

const keysPage = () => (
  <ResourcePage
    title="API Keys"
    basePath="/api/admin/keys"
    columns={[
      { name: "name", label: "名称" },
      { name: "public_prefix", label: "前缀" },
      statusCol,
      { name: "concurrency_limit", label: "并发上限" },
      { name: "client_id", label: "客户端" },
      { name: "created_at", label: "创建时间" },
    ]}
    createFields={[
      { name: "member_id", label: "成员 ID", required: true },
      { name: "client_id", label: "客户端 ID（可选）" },
      { name: "name", label: "名称", required: true },
      { name: "concurrency_limit", label: "并发上限", kind: "number", default: 1 },
      { name: "allowed_models", label: "允许模型（逗号分隔，留空=全部）", help: "逗号分隔" },
    ]}
    editFields={[{ name: "status", label: "状态", kind: "select", options: ["active", "paused"] }]}
    rowActions={(row, reload) => (
      <>
        <button
          className="btn small danger"
          onClick={async () => {
            if (!confirm("撤销后不可恢复，确认？")) return;
            await api.post(`/api/admin/keys/${row.id}/revoke`);
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
      { name: "state", label: "状态", render: (r) => <Badge value={r.state} /> },
      { name: "concurrency_limit", label: "并发上限" },
      { name: "priority", label: "优先级" },
      { name: "credential_version", label: "凭证版本" },
      { name: "expires_at", label: "凭证到期" },
    ]}
    createFields={[
      { name: "label", label: "标签", required: true },
      { name: "access_token", label: "Access Token", kind: "password", required: true, help: "加密存储，仅创建时输入" },
      { name: "refresh_token", label: "Refresh Token", kind: "password" },
      { name: "account_id", label: "上游 Account ID" },
      { name: "concurrency_limit", label: "并发上限", kind: "number", default: 1 },
      { name: "egress_policy_id", label: "出口策略 ID（可选）" },
    ]}
    editFields={[
      { name: "state", label: "状态", kind: "select", options: ["active", "paused"] },
      { name: "concurrency_limit", label: "并发上限", kind: "number" },
      { name: "priority", label: "优先级", kind: "number" },
    ]}
    rowActions={(row, reload) => <button className="btn small" onClick={async () => {
      try {
        const result = await api.get<{data: {reason: string}[]}>(`/api/admin/accounts/${row.id}/holds`);
        const reasons = result.data.map(h => h.reason);
        if (!reasons.length) { alert("没有隔离原因"); return; }
        const reason = prompt(`隔离原因：${reasons.join("、")}。输入需解除的原因；unknown_pending 须先处置请求，reauth_required 须重新授权。`);
        if (!reason) return;
        const note = prompt("填写解除依据（必填）");
        if (!note?.trim()) return;
        await api.post(`/api/admin/accounts/${row.id}/holds`, {reason, note});
        reload();
      } catch (err) { alert(String(err)); }
    }}>隔离原因 / 解除</button>}
    notice={<span className="muted small">OAuth 授权请在“OAuth 会话”页发起；凭证永不回显。存在隔离原因时不能直接激活。</span>}
  />
);

const proxiesPage = () => (
  <ResourcePage
    title="代理出口"
    basePath="/api/admin/proxies"
    columns={[
      { name: "name", label: "名称" },
      { name: "kind", label: "类型" },
      { name: "endpoint", label: "地址" },
      statusCol,
    ]}
    createFields={[
      { name: "name", label: "名称", required: true },
      { name: "kind", label: "类型", kind: "select", options: ["direct", "http", "socks5"], required: true },
      { name: "endpoint", label: "地址（direct 留空）", placeholder: "host:port" },
      { name: "username", label: "用户名（可选）" },
      { name: "password", label: "密码（可选）", kind: "password" },
    ]}
    editFields={[{ name: "status", label: "状态", kind: "select", options: ["active", "disabled"] }]}
    rowActions={(row, reload) => (
      <button
        className="btn small"
        onClick={async () => {
          const r = await api.post<any>(`/api/admin/proxies/${row.id}/test`);
          alert(r.ok ? `探测成功 (${r.status ?? 204})` : `探测失败：${r.error}`);
          reload();
        }}
      >
        探测
      </button>
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
      { name: "failure_mode", label: "故障模式", render: (r) => <Badge value={r.failure_mode} /> },
      { name: "fallbacks", label: "备用列表", render: (r) => (r.fallbacks ?? []).join(" → ") || "—" },
    ]}
    createFields={[
      { name: "name", label: "名称", required: true },
      { name: "primary_proxy_id", label: "主出口 Profile ID", required: true },
      {
        name: "failure_mode",
        label: "故障模式",
        kind: "select",
        options: ["stop", "fallback"],
        default: "stop",
        help: "stop=故障即停；fallback=仅尝试显式备用列表，绝不自动直连",
      },
      { name: "fallback_proxy_ids", label: "备用 ID（逗号分隔）" },
    ]}
  />
);

const groupsPage = () => (
  <ResourcePage
    title="账号组"
    basePath="/api/admin/groups"
    columns={[
      { name: "name", label: "名称" },
      statusCol,
      { name: "accounts", label: "账号", render: (r) => (r.accounts ?? []).join("、") || "—" },
    ]}
    createFields={[{ name: "name", label: "组名", required: true }]}
    notice={<GroupAddForm />}
    rowActions={(row, reload) => <GroupRowActions row={row} reload={reload} />}
  />
);

const GroupAddForm: React.FC = () => <span className="muted small">在组行内“添加账号”；删除组或移除账号请用行内按钮。</span>;

const GroupRowActions: React.FC<{ row: any; reload: () => void }> = ({ row, reload }) => {
  const [accountId, setAccountId] = React.useState("");
  return (
    <span style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
      <input
        placeholder="账号ID"
        value={accountId}
        onChange={(e) => setAccountId(e.target.value)}
        style={{ width: 220 }}
      />
      <button
        className="btn small"
        onClick={async () => {
          if (!accountId) return alert("请输入账号 ID");
          await api.post(`/api/admin/groups/${row.id}`, { account_id: accountId });
          setAccountId("");
          reload();
        }}
      >
        添加账号
      </button>
      <button
        className="btn small danger"
        onClick={async () => {
          if (!confirm(`删除组 ${row.name}？`)) return;
          await api.del(`/api/admin/groups/${row.id}`);
          reload();
        }}
      >
        删除组
      </button>
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
      { name: "amount", label: "固定金额 $" },
      { name: "percent_bps", label: "百分比 bps" },
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
      { name: "owner_member_id", label: "成员 ID（按作用域）" },
      { name: "owner_key_id", label: "Key ID（按作用域）" },
      { name: "owner_account_id", label: "账号 ID（按作用域）" },
      { name: "owner_group_id", label: "组 ID（按作用域）" },
      { name: "period", label: "周期", kind: "select", options: ["day", "week"], required: true },
      { name: "timezone", label: "时区", default: "Asia/Shanghai" },
      { name: "mode", label: "模式", kind: "select", options: ["fixed", "percent"], required: true },
      { name: "amount", label: "固定金额（mode=fixed）", placeholder: "100.00" },
      { name: "percent_bps", label: "基点（mode=percent，2000=20%）", kind: "number" },
      { name: "base_policy_id", label: "基础策略 ID（mode=percent 必填）" },
    ]}
    notice={<span className="muted small">金额=NUMERIC 美元；percent 引用 fixed 基础策略；无预算覆盖的请求默认拒绝。</span>}
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
      { name: "limit", label: "快照限额 $" },
      { name: "spent", label: "已花 $" },
      { name: "reserved", label: "预留 $" },
    ]}
    notice={<span className="muted small">limit_snapshot 周期内不可覆盖；spent+reserved 即为占用。</span>}
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
      { name: "input_tokens", label: "非缓存输入 tok" },
      { name: "cached_input_tokens", label: "缓存 tok" },
      { name: "output_tokens", label: "输出 tok" },
      { name: "cost", label: "费用 $" },
    ]}
    notice={<span className="muted small">追加式账本：修正通过 adjustment 行，不直接改余额。</span>}
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
      { name: "source_url", label: "来源 URL" },
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
          setMsg(`同步结果：${r.status}（官方解析器未经验证前不会自动激活）`);
        }}
      >
        同步官方价格
      </button>{" "}
      <span className="muted small">{msg || "synthetic 版本仅用于测试，生产需要 verified official 版本"}</span>
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
      { name: "enabled", label: "启用", render: (r) => (r.enabled ? "✓" : "✗") },
      { name: "version", label: "版本" },
      { name: "source", label: "来源" },
    ]}
    editFields={[
      { name: "enabled", label: "启用", kind: "checkbox" },
      { name: "action", label: "动作", kind: "select", options: ["flag", "review", "block", "reject", "unsupported"] },
      { name: "matcher", label: "匹配器 JSON", kind: "textarea", help: "保存前会执行完整规则编译校验" },
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
          setResult(JSON.stringify(r));
        }}
      >
        测试
      </button>
      <code className="small">{result}</code>
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
      { name: "duration_ms", label: "耗时 ms" },
      { name: "summary", label: "摘要（脱敏）" },
      { name: "reviews", label: "复核", render: (r) => (r.reviews ?? []).length + " 条" },
    ]}
    rowActions={(row, reload) => (
      <button
        className="btn small"
        onClick={async () => {
          const outcome = prompt("复核结论：confirmed_violation / false_positive / exception_created", "false_positive");
          if (!outcome) return;
          let exception_rule_id: string | undefined;
          let exception_ttl_hours: number | undefined;
          if (outcome === "exception_created") {
            exception_rule_id = prompt("输入本事件命中的非 secret 规则 ID") || undefined;
            if (!exception_rule_id) return;
            const ttl = prompt("例外有效小时数（1–720）", "24");
            exception_ttl_hours = Number(ttl);
            if (!Number.isInteger(exception_ttl_hours) || exception_ttl_hours < 1 || exception_ttl_hours > 720) {
              alert("有效小时数必须在 1–720 之间");
              return;
            }
          }
          await api.post(`/api/admin/audit/events/${row.id}/review`, { outcome, note: "", exception_rule_id, exception_ttl_hours });
          reload();
        }}
      >
        复核
      </button>
    )}
    notice={<span className="muted small">复核只记录结论或创建精确例外，绝不重放请求。</span>}
  />
);

const adminEventsPage = () => (
  <ResourcePage
    title="管理事件"
    basePath="/api/admin/admin-events"
    columns={[
      { name: "created_at", label: "时间" },
      { name: "actor", label: "操作者" },
      { name: "action", label: "动作" },
      { name: "target_type", label: "对象" },
      { name: "target_id", label: "ID" },
    ]}
  />
);

const statusPage = () => {
  const [s, setS] = useState<any>(null);
  useEffect(() => {
    api.get<any>("/api/admin/status").then(setS).catch(() => {});
    const t = setInterval(() => api.get<any>("/api/admin/status").then(setS).catch(() => {}), 5000);
    return () => clearInterval(t);
  }, []);
  if (!s) return <div className="muted">加载中…</div>;
  return (
    <div>
      <h2>系统状态</h2>
      <ul className="status-list">
        <li>
          production_ready：<Badge value={s.production_ready ? "true" : "false"} />（需 P0 验证完成后开启）
        </li>
        <li>进行中请求：{s.active_requests}</li>
        <li>
          unknown 状态请求：{s.unknown_requests}
          {s.unknown_requests > 0 && <b className="req">（需人工核对账本，见 unknown_resolved 流程）</b>}
        </li>
        <li>
          账号状态：
          {(s.accounts ?? []).map((a: any) => (
            <span key={a.state}>
              {" "}
              <Badge value={a.state} />×{a.count}
            </span>
          ))}
        </li>
      </ul>
    </div>
  );
};

const oauthPage = () => {
  const [url, setUrl] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [status, setStatus] = useState("");
	const [reuseAccountID, setReuseAccountID] = useState("");
  return (
    <div>
      <h2>OAuth 授权会话</h2>
      <p className="muted small">
        PKCE + 随机 state，10 分钟有效、单次消费。回调地址必须是上游注册的地址（P0-01 验证前真实联调为 pending）。
      </p>
	<label>
		复用已有账号（重新授权，可选）
		<input value={reuseAccountID} placeholder="账号 UUID" onChange={(e) => setReuseAccountID(e.target.value)} />
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
            setStatus(r.status + (r.account_id ? ` · 账号 ${r.account_id}` : ""));
          }}
        >
          查询状态
        </button>
      )}
      {status && <p>{status}</p>}
    </div>
  );
};

const routesPage: React.FC<{ keyId: string }> = ({ keyId }) => (
  <ResourcePage
    title={`路由 · ${keyId}`}
    basePath={`/api/admin/keys/${keyId}/routes`}
    columns={[
      { name: "target_type", label: "类型" },
      { name: "target_id", label: "目标 ID" },
      { name: "priority", label: "优先级" },
    ]}
    createFields={[
      { name: "target_type", label: "类型", kind: "select", options: ["account", "group"], required: true },
      { name: "target_id", label: "目标 ID", required: true },
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

const NAV = [
  { key: "#/status", label: "状态", page: statusPage },
  { key: "#/members", label: "成员", page: membersPage },
  { key: "#/clients", label: "客户端", page: clientsPage },
  { key: "#/keys", label: "API Keys", page: keysPage },
  { key: "#/accounts", label: "上游账号", page: accountsPage },
  { key: "#/oauth", label: "OAuth 会话", page: oauthPage },
  { key: "#/groups", label: "账号组", page: groupsPage },
  { key: "#/proxies", label: "代理出口", page: proxiesPage },
  { key: "#/egress", label: "出口策略", page: egressPage },
  { key: "#/budgets", label: "预算策略", page: budgetsPage },
  { key: "#/periods", label: "预算周期", page: periodsPage },
  { key: "#/ledger", label: "用量账本", page: ledgerPage },
  { key: "#/prices", label: "价格版本", page: pricesPage },
  { key: "#/audit-rules", label: "审核规则", page: auditRulesPage },
  { key: "#/audit-events", label: "审核事件", page: auditEventsPage },
  { key: "#/admin-events", label: "管理事件", page: adminEventsPage },
];

export const App: React.FC = () => {
  const [hash, setHash] = useState(location.hash || "#/status");
	const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  useEffect(() => {
    const fn = () => setHash(location.hash || "#/status");
    window.addEventListener("hashchange", fn);
    return () => window.removeEventListener("hashchange", fn);
  }, []);
	useEffect(() => {
		api.get("/api/admin/session").then(() => setAuthenticated(true)).catch(() => setAuthenticated(false));
	}, []);

	if (authenticated === null) return <div className="login-wrap">检查登录状态…</div>;
  if (!authenticated) return <Login onLoggedIn={() => setAuthenticated(true)} />;

  const routeMatch = hash.match(/^#\/routes\/(.+)$/);
  let content: React.ReactNode;
  if (routeMatch) {
    // Render as element types so each page keeps its own hook state — calling
    // page functions inline would splice their hooks into App's hook list and
    // crash on navigation.
    content = React.createElement(routesPage, { keyId: routeMatch[1] });
  } else {
    const nav = NAV.find((n) => hash.startsWith(n.key));
    content = nav ? React.createElement(nav.page) : <div className="muted">未知页面</div>;
  }

  return (
    <div className="layout">
      <aside>
        <div className="brand">SubAI Gateway</div>
        <nav>
          {NAV.map((n) => (
            <a key={n.key} href={n.key} className={hash.startsWith(n.key) ? "active" : ""}>
              {n.label}
            </a>
          ))}
        </nav>
        <div className="grow" />
        <button
          className="btn"
          onClick={async () => {
            await api.del("/api/admin/session");
			setAuthenticated(false);
            location.hash = "#/status";
          }}
        >
          退出登录
        </button>
      </aside>
      <main>{content}</main>
    </div>
  );
};

const Login: React.FC<{ onLoggedIn: () => void }> = ({ onLoggedIn }) => {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  return (
    <div className="login-wrap">
      <form
        className="login"
        onSubmit={async (e) => {
          e.preventDefault();
          try {
			await api.post("/api/admin/session", { username, password });
            setError("");
			onLoggedIn();
          } catch (err: any) {
            setError(err.message);
          }
        }}
      >
        <h2>SubAI Gateway 管理登录</h2>
        <label>
          用户名
          <input value={username} onChange={(e) => setUsername(e.target.value)} required />
        </label>
        <label>
          密码
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </label>
        {error && <div className="error">{error}</div>}
        <button className="btn primary" type="submit">
          登录
        </button>
        <p className="muted small">登录接口有限流；首次管理员通过 SUBAI_DEV_BOOTSTRAP_ADMIN 或数据库初始化。</p>
      </form>
    </div>
  );
};
