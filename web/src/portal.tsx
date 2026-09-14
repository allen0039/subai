import React, { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { Badge, Modal } from "./components";
import { dateTime, errorText, fixedDecimal, quotaMoney } from "./locale";

const empty = (value: unknown, text = "未设置") => value === "" || value === null || value === undefined ? text : String(value);
const Empty: React.FC<{text: string}> = ({text}) => <div className="empty-state">{text}</div>;

const SubscriptionCard: React.FC<{ sub: any }> = ({ sub }) => <div className="subscription-card">
  <div><span className="eyebrow">订阅版本 {sub.plan_version}</span><h3>{sub.plan_name}</h3></div><Badge value={sub.status} domain="subscription"/>
  {[['每日额度',sub.daily_limit_usd],['每周额度',sub.weekly_limit_usd],['每月额度',sub.monthly_limit_usd]].map(([title,value]) => <div className="quota-line" key={String(title)}><span>{title}</span><b>{quotaMoney(value)}</b></div>)}
  <small>到期：{dateTime(sub.expires_at, "无到期限制")} · 计费倍率：× {fixedDecimal(sub.rate_multiplier, "1.00")} · 套餐共享并发：{sub.concurrency_limit} · 可创建密钥数：{Number(sub.max_keys) > 0 ? sub.max_keys : "不限"}</small>
</div>;

export const MyOverview: React.FC = () => {
  const [subscriptions, setSubscriptions] = useState<any[]>([]), [overview, setOverview] = useState<any | null>(null), [error, setError] = useState("");
  useEffect(() => { let mounted = true; Promise.all([api.get<any>("/api/admin/me/overview"), api.get<any>("/api/admin/me/subscriptions")]).then(([summary, rows]) => { if (mounted) { setOverview(summary); setSubscriptions(rows.data ?? []); } }).catch(cause => { if (mounted) setError(errorText(cause)); }); return () => { mounted = false; }; }, []);
  const active = subscriptions.filter(s => s.status === "active");
  const series = overview?.series ?? [];
  const peak = Math.max(1, ...series.map((item: any) => Number(item.cost) || 0));
  const points = series.map((item: any, index: number) => `${series.length === 1 ? 50 : (index / (series.length - 1)) * 100},${92 - ((Number(item.cost) || 0) / peak) * 80}`).join(" ");
  return <div className="portal"><div className="portal-hero"><span>个人控制台</span><h1>订阅、密钥与可用资源</h1><p>资源由订阅统一分配；为保护账号安全，个人台不会展示上游账号、出口或代理详情。</p></div>{error && <div className="error">概览加载失败：{error}</div>}<section className="member-flow" aria-label="订阅到资源的访问链路"><div><span>① 订阅</span><b>{overview?.active_subscriptions ?? active.length}</b><small>可用订阅</small></div><i aria-hidden="true">→</i><div><span>② 密钥</span><b>{overview?.active_keys ?? "—"}</b><small>已启用接口密钥</small></div><i aria-hidden="true">→</i><div><span>③ 资源访问</span><b>{overview?.resource_access === "managed_by_subscription" ? "已管理" : "—"}</b><small>由订阅策略决定</small></div></section><div className="metric-grid"><div className="metric"><b>{active.length}</b><span>可用订阅</span></div><div className="metric"><b>{dateTime(overview?.last_used_at, "尚未使用")}</b><span>最近一次使用</span></div><div className="metric"><b>{series.length}</b><span>近 7 日有用量的日期</span></div></div><section className="section-card"><div className="section-title"><h2>近 7 日成本摘要</h2><span className="muted small">仅包含当前成员的已记账用量</span></div>{series.length ? <div className="member-sparkline"><svg viewBox="0 0 100 100" preserveAspectRatio="none" role="img" aria-label="近七日成本趋势"><polyline points={points} fill="none" stroke="var(--accent)" strokeWidth="3" vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" /></svg></div> : <Empty text="近 7 日尚无已记账用量。"/>}</section><section className="section-card"><div className="section-title"><h2>可用订阅</h2><a className="btn small" href="#/my-subscriptions">查看全部</a></div>{active.length ? <div className="subscription-cards">{active.map(s => <SubscriptionCard key={s.id} sub={s}/>)}</div> : <Empty text="还没有可用订阅。请联系管理员分配套餐。"/>}</section></div>;
};

export const MySubscriptions: React.FC = () => {
  const [rows, setRows] = useState<any[]>([]), [error, setError] = useState("");
  const load = useCallback(() => api.get<any>("/api/admin/me/subscriptions").then(r => setRows(r.data ?? [])).catch(e => setError(errorText(e))), []);
  useEffect(() => { load(); }, [load]);
  return <div className="portal"><div className="page-head"><h2>我的订阅</h2><div className="grow"/><button className="btn" onClick={load}>刷新</button></div>{error && <div className="error">{error}</div>}<div className="subscription-cards">{rows.length ? rows.map(s => <SubscriptionCard key={s.id} sub={s}/>) : <Empty text="暂时没有套餐订阅。"/>}</div></div>;
};

export const MyKeys: React.FC = () => {
  const [keys, setKeys] = useState<any[]>([]), [subs, setSubs] = useState<any[]>([]), [error, setError] = useState(""), [flash, setFlash] = useState(""), [show, setShow] = useState(false);
  const [form, setForm] = useState({subscription_id:"",name:"",concurrency_limit:"1",expires_at:"",allowed_models:""});
  const load = useCallback(async () => { try { const [keyResult, subResult] = await Promise.all([api.get<any>("/api/admin/me/keys"),api.get<any>("/api/admin/me/subscriptions")]); setKeys(keyResult.data ?? []); setSubs((subResult.data ?? []).filter((s: any) => s.status === "active")); } catch (e) { setError(errorText(e)); } }, []);
  useEffect(() => { load(); }, [load]);
  const create = async () => { try { const result = await api.post<any>("/api/admin/me/keys", {...form, concurrency_limit:Number(form.concurrency_limit), expires_at:form.expires_at ? new Date(form.expires_at).toISOString() : undefined, allowed_models:form.allowed_models.split(",").map(value => value.trim()).filter(Boolean)}); setShow(false); setFlash(`接口密钥已创建，请立即保存：${result.key}`); load(); } catch (e) { setError(errorText(e)); } };
  const update = async (key: any, status: string) => { try { await api.patch(`/api/admin/me/keys/${key.id}`, {status,version:key.version}); load(); } catch (e) { setError(errorText(e)); } };
  const revoke = async (key: any) => { if (!confirm(`撤销接口密钥“${key.name}”后无法恢复，确认继续吗？`)) return; try { await api.post(`/api/admin/me/keys/${key.id}/revoke`); load(); } catch (e) { setError(errorText(e)); } };
  return <div className="portal"><div className="page-head"><h2>我的接口密钥</h2><div className="grow"/><button className="btn primary" disabled={!subs.length} onClick={() => { setForm({subscription_id:subs[0]?.id ?? "",name:"",concurrency_limit:"1",expires_at:"",allowed_models:""}); setShow(true); }}>创建接口密钥</button><button className="btn" onClick={load}>刷新</button></div>{!subs.length && <div className="error">没有可用于创建接口密钥的有效订阅。</div>}{flash && <div className="flash" onClick={() => setFlash("")}>{flash}</div>}{error && <div className="error">{error}</div>}<table className="tbl"><thead><tr><th>名称</th><th>所属套餐</th><th>前缀</th><th>状态</th><th>单个密钥并发</th><th>到期时间</th><th/></tr></thead><tbody>{keys.map(key => <tr key={key.id}><td>{key.name}</td><td>{key.plan_name}</td><td><code>{key.public_prefix}</code></td><td><Badge value={key.status}/></td><td>{key.concurrency_limit}</td><td>{dateTime(key.expires_at,"无到期限制")}</td><td className="actions">{key.status !== "revoked" && <><button className="btn small" onClick={() => update(key,key.status === "active" ? "paused" : "active")}>{key.status === "active" ? "暂停" : "恢复"}</button><button className="btn small danger" onClick={() => revoke(key)}>撤销</button></>}</td></tr>)}</tbody></table>{show && <Modal title="创建接口密钥" onClose={() => setShow(false)} onSubmit={create}><label className="field"><span className="field-label">使用的套餐订阅</span><select value={form.subscription_id} onChange={e => setForm({...form,subscription_id:e.target.value})}>{subs.map(sub => <option key={sub.id} value={sub.id}>{sub.plan_name} · 到期 {dateTime(sub.expires_at,"无到期限制")}</option>)}</select></label><label className="field"><span className="field-label">接口密钥名称</span><input value={form.name} onChange={e => setForm({...form,name:e.target.value})} required/></label><label className="field"><span className="field-label">单个密钥并发</span><input type="number" min="1" value={form.concurrency_limit} onChange={e => setForm({...form,concurrency_limit:e.target.value})}/><small className="muted">该值不能超过所属套餐的共享并发。</small></label><label className="field"><span className="field-label">到期时间（可选）</span><input type="datetime-local" value={form.expires_at} onChange={e => setForm({...form,expires_at:e.target.value})}/></label><label className="field"><span className="field-label">允许模型（可选，使用逗号分隔）</span><input value={form.allowed_models} onChange={e => setForm({...form,allowed_models:e.target.value})}/></label></Modal>}</div>;
};

export const SecurityPage: React.FC = () => {
  const [oldPassword,setOldPassword] = useState(""), [password,setPassword] = useState(""), [message,setMessage] = useState(""), [error,setError] = useState("");
  const submit = async () => { try { await api.patch("/api/admin/me/password",{old_password:oldPassword,new_password:password}); setMessage("密码已更新，其他登录会话已退出。"); setOldPassword(""); setPassword(""); } catch (e) { setError(errorText(e)); } };
  return <div className="portal narrow"><h2>修改密码</h2><p className="muted">修改成功后，当前会话会保留，其他设备上的登录会话会退出。</p>{message && <div className="flash">{message}</div>}{error && <div className="error">{error}</div>}<label className="field"><span className="field-label">当前密码</span><input type="password" value={oldPassword} onChange={e => setOldPassword(e.target.value)}/></label><label className="field"><span className="field-label">新密码</span><input type="password" value={password} onChange={e => setPassword(e.target.value)}/></label><button className="btn primary" onClick={submit}>更新密码</button></div>;
};

export const AdminPlans: React.FC = () => {
  const emptyForm = {name:"",description:"",daily_limit_usd:"",weekly_limit_usd:"",monthly_limit_usd:"",rate_multiplier:"1.00",concurrency_limit:"1",max_keys:"",default_validity_days:"30",allowed_models:[] as string[],restrict_models:false,pool_ids:[] as string[],model_pricing:[]};
  const [plans,setPlans] = useState<any[]>([]);
  const [pools,setPools] = useState<any[]>([]);
  const [catalogModels,setCatalogModels] = useState<any[]>([]);
  const [error,setError] = useState("");
  const [show,setShow] = useState(false);
  const [editing,setEditing] = useState<any | null>(null);
  const [form,setForm] = useState<any>(emptyForm);
  const [modelQuery,setModelQuery] = useState("");
  const [catalogBusy,setCatalogBusy] = useState(false);

  const decimalInput = (value: unknown, fallback = "") => {
    if (value === null || value === undefined || value === "") return fallback;
    return fixedDecimal(value, fallback);
  };
  const loadCatalog = useCallback(async (poolIDs: string[]) => {
    if (!poolIDs.length) { setCatalogModels([]); return; }
    const query = new URLSearchParams({pool_ids:poolIDs.join(",")});
    const result = await api.get<any>(`/api/admin/models/candidates?${query}`);
    setCatalogModels((result.data ?? []).filter((item: any) => String(item.model ?? "").trim()));
  }, []);

  const load = useCallback(async () => {
    try {
      const [planResult,poolResult] = await Promise.all([api.get<any>("/api/admin/plans"),api.get<any>("/api/admin/account-pools")]);
      setPlans(planResult.data ?? []);
      setPools(poolResult.data ?? []);
      setError("");
    } catch (e) { setError(errorText(e)); }
  }, []);
  useEffect(() => { load(); },[load]);
  useEffect(() => {
    if (!show) return;
    loadCatalog(form.pool_ids).catch(e => setError(errorText(e)));
  }, [show, form.pool_ids.join(","), loadCatalog]);

  const payload = () => {
    const {restrict_models, ...body} = form;
    return {
      ...body,
      concurrency_limit:Number(form.concurrency_limit),
      max_keys:form.max_keys === "" ? 0 : Number(form.max_keys),
      default_validity_days:Number(form.default_validity_days),
      allowed_models:restrict_models ? form.allowed_models : [],
    };
  };
  const save = async () => {
    try {
      if (editing) await api.post(`/api/admin/plans/${editing.id}/versions`,payload());
      else await api.post("/api/admin/plans",payload());
      setShow(false);
      setEditing(null);
      await load();
    } catch (e) { setError(errorText(e)); }
  };
  const publish = async (id:string) => { try { await api.post(`/api/admin/plans/${id}/publish`); await load(); } catch (e) { setError(errorText(e)); } };
  const openCreate = () => { setEditing(null); setModelQuery(""); setForm({...emptyForm,pool_ids:[],model_pricing:[]}); setShow(true); };
  const openEdit = (plan:any) => {
    setEditing(plan);
    setModelQuery("");
    setForm({
      name:plan.name ?? "", description:plan.description ?? "",
      daily_limit_usd:decimalInput(plan.daily_limit_usd), weekly_limit_usd:decimalInput(plan.weekly_limit_usd), monthly_limit_usd:decimalInput(plan.monthly_limit_usd),
      rate_multiplier:decimalInput(plan.rate_multiplier, "1.00"), concurrency_limit:String(plan.concurrency_limit || 1),
      max_keys:Number(plan.max_keys) > 0 ? String(plan.max_keys) : "", default_validity_days:String(plan.default_validity_days || 30),
      allowed_models:(plan.allowed_models ?? []).filter((model:string) => model.toLowerCase().startsWith("gpt-")), restrict_models:(plan.allowed_models ?? []).length > 0, pool_ids:(plan.pools ?? []).map((pool:any) => pool.id),
      model_pricing:plan.model_pricing ?? [],
    });
    setShow(true);
  };
  const selectedModels = new Set<string>(form.allowed_models);
  const knownModels = new Set(catalogModels.map((item:any) => String(item.model)));
  const modelChoices = [...catalogModels, ...form.allowed_models.filter((model:string) => !knownModels.has(model)).map((model:string) => ({model, unavailable:true}))]
    .filter((item:any) => String(item.model).toLowerCase().includes(modelQuery.trim().toLowerCase()));
  const selectAllVisible = () => setForm({...form,allowed_models:Array.from(new Set([...form.allowed_models,...modelChoices.map((item:any) => String(item.model))]))});
  const toggleModel = (model:string, checked:boolean) => setForm({...form,allowed_models:checked ? Array.from(new Set([...form.allowed_models,model])) : form.allowed_models.filter((item:string) => item !== model)});
  const syncCatalog = async () => {
    setCatalogBusy(true);
    try { await api.post("/api/admin/models/sync", {pool_ids:form.pool_ids}); await loadCatalog(form.pool_ids); setError(""); }
    catch (e) { setError(errorText(e)); }
    finally { setCatalogBusy(false); }
  };

  return <div className="portal">
    <div className="page-head"><h2>套餐管理</h2><div className="grow"/><button className="btn primary" onClick={openCreate}>创建套餐草稿</button><button className="btn" onClick={load}>刷新</button></div>
    <p className="muted">套餐并发由同一订阅下的所有接口密钥共享；上游账号的承载能力请在“上游账号”中单独设置。</p>
    {error && <div className="error">{error}</div>}
    <table className="tbl"><thead><tr><th>套餐</th><th>状态</th><th>额度（每日／每周／每月）</th><th>计费倍率</th><th>套餐共享并发</th><th>接口密钥数量</th><th>账号池</th><th/></tr></thead><tbody>{plans.map(plan => <tr key={plan.id}>
      <td><b>{plan.name}</b><br/><small className="muted">版本 {plan.plan_version} · {plan.description || "暂无说明"}</small></td>
      <td><Badge value={plan.status} domain="plan"/></td>
      <td>{quotaMoney(plan.daily_limit_usd)}／{quotaMoney(plan.weekly_limit_usd)}／{quotaMoney(plan.monthly_limit_usd)}</td>
      <td>× {fixedDecimal(plan.rate_multiplier, "1.00")}</td><td>{plan.concurrency_limit}</td><td>{Number(plan.max_keys) > 0 ? plan.max_keys : "不限"}</td>
      <td>{(plan.pools ?? []).map((pool:any) => pool.name).join("、") || "未配置"}</td>
      <td className="actions"><button className="btn small" onClick={() => openEdit(plan)}>编辑</button>{plan.status === "draft" && <button className="btn small primary" onClick={() => publish(plan.id)}>发布</button>}</td>
    </tr>)}</tbody></table>
    {show && <Modal className="plan-modal" title={editing ? `编辑套餐 · ${editing.name}` : "创建套餐草稿"} onClose={() => { setShow(false); setEditing(null); }} onSubmit={save} submitLabel={editing ? "保存新版本" : "创建草稿"}>
      {editing && <div className="flash">保存后生成版本 {Number(editing.plan_version) + 1}；已有订阅继续使用原版本。</div>}
      <label className="field"><span className="field-label">套餐名称</span><input value={form.name} onChange={e => setForm({...form,name:e.target.value})} required/></label>
      <label className="field"><span className="field-label">说明</span><textarea value={form.description} onChange={e => setForm({...form,description:e.target.value})}/></label>
      <div className="form-grid">{[["daily_limit_usd","每日额度（美元）"],["weekly_limit_usd","每周额度（美元）"],["monthly_limit_usd","每月额度（美元）"]].map(([key,title]) => <label className="field" key={key}><span className="field-label">{title}</span><input type="number" min="0" step="0.01" value={form[key]} placeholder="留空表示不限" onChange={e => setForm({...form,[key]:e.target.value})}/></label>)}</div>
      <label className="field"><span className="field-label">计费倍率</span><input type="number" min="0" step="0.01" value={form.rate_multiplier} onChange={e => setForm({...form,rate_multiplier:e.target.value})}/><small className="muted">模型目录价格乘以此倍率后计入用户额度。</small></label>
      <div className="form-grid">
        <label className="field"><span className="field-label">套餐共享并发</span><input type="number" min="1" value={form.concurrency_limit} onChange={e => setForm({...form,concurrency_limit:e.target.value})}/><small className="muted">同一订阅下所有密钥合计可同时执行的请求数。</small></label>
        <label className="field"><span className="field-label">可创建接口密钥数</span><input type="number" min="1" value={form.max_keys} placeholder="留空表示不限" onChange={e => setForm({...form,max_keys:e.target.value})}/><small className="muted">默认不限；填写后限制每个订阅可创建的密钥总数。</small></label>
      </div>
      <section className="model-selector" aria-label="套餐可用模型">
        <div className="model-selector-head"><div><span className="field-label">可用模型</span><small className="muted">只读取已绑定 Codex 账号实际返回的 GPT 模型，不使用计费目录作为候选项。</small></div><label className="model-toggle"><input type="checkbox" checked={form.restrict_models} onChange={e => setForm({...form,restrict_models:e.target.checked})}/><span>仅允许勾选的模型</span></label></div>
        {form.restrict_models ? <><div className="model-toolbar"><input aria-label="筛选模型" placeholder="搜索模型名称" value={modelQuery} onChange={e => setModelQuery(e.target.value)}/><span className="muted small">已选 {form.allowed_models.length} 个</span><button type="button" className="btn small" onClick={selectAllVisible} disabled={!modelChoices.length}>全选当前结果</button><button type="button" className="btn small" onClick={() => setForm({...form,allowed_models:[]})} disabled={!form.allowed_models.length}>清空选择</button><button type="button" className="btn small" onClick={syncCatalog} disabled={catalogBusy || !form.pool_ids.length}>{catalogBusy ? "正在读取上游模型" : "同步上游模型"}</button></div>
          <div className="model-check-list">{modelChoices.length ? modelChoices.map((item:any) => { const model = String(item.model); return <label key={model} className={item.unavailable ? "model-missing" : ""}><input type="checkbox" checked={selectedModels.has(model)} onChange={e => toggleModel(model,e.target.checked)}/><span>{model}</span>{item.unavailable ? <small>原有配置，当前账号未返回</small> : <small>由 {item.account_count} 个已启用账号支持</small>}</label>; }) : <div className="empty-state">请先绑定账号池，然后点击“同步上游模型”读取该池中 Codex 账号实际支持的 GPT 模型。</div>}</div>
        </> : <div className="model-all-note">当前套餐允许已绑定 Codex 账号实际支持的全部 GPT 模型；启用限制后再勾选需要开放的模型。</div>}
      </section>
      <span className="field-label">绑定账号池</span><div className="check-list">{pools.map(pool => <label key={pool.id}><input type="checkbox" checked={form.pool_ids.includes(pool.id)} onChange={e => setForm({...form,pool_ids:e.target.checked ? [...form.pool_ids,pool.id] : form.pool_ids.filter((id:string) => id !== pool.id)})}/>{pool.name}</label>)}</div>
    </Modal>}
  </div>;
};

export const AdminSubscriptions: React.FC = () => {
  const [rows,setRows] = useState<any[]>([]), [users,setUsers] = useState<any[]>([]), [plans,setPlans] = useState<any[]>([]), [error,setError] = useState(""), [show,setShow] = useState(false); const [form,setForm] = useState({member_id:"",plan_id:"",expires_at:"",notes:""});
  const load = useCallback(async () => { try { const [subResult,userResult,planResult] = await Promise.all([api.get<any>("/api/admin/subscriptions"),api.get<any>("/api/admin/users"),api.get<any>("/api/admin/plans")]); setRows(subResult.data ?? []); setUsers(userResult.data ?? []); setPlans((planResult.data ?? []).filter((plan:any) => plan.status === "active")); } catch (e) { setError(errorText(e)); } }, []); useEffect(() => { load(); },[load]);
  const assign = async () => { try { await api.post("/api/admin/subscriptions",{...form,expires_at:form.expires_at ? new Date(form.expires_at).toISOString() : undefined}); setShow(false); load(); } catch (e) { setError(errorText(e)); } };
  return <div className="portal"><div className="page-head"><h2>订阅分配</h2><div className="grow"/><button className="btn primary" onClick={() => { setForm({member_id:users[0]?.id ?? "",plan_id:plans[0]?.id ?? "",expires_at:"",notes:""}); setShow(true); }}>分配套餐</button><button className="btn" onClick={load}>刷新</button></div>{error && <div className="error">{error}</div>}<table className="tbl"><thead><tr><th>用户</th><th>套餐</th><th>状态</th><th>到期时间</th><th>额度（每日／每周／每月）</th></tr></thead><tbody>{rows.map(sub => <tr key={sub.id}><td>{sub.member_name ?? sub.member_id}</td><td>{sub.plan_name} <small className="muted">版本 {sub.plan_version}</small></td><td><Badge value={sub.status} domain="subscription"/></td><td>{dateTime(sub.expires_at,"无到期限制")}</td><td>{quotaMoney(sub.daily_limit_usd)}／{quotaMoney(sub.weekly_limit_usd)}／{quotaMoney(sub.monthly_limit_usd)}</td></tr>)}</tbody></table>{show && <Modal title="分配套餐" onClose={() => setShow(false)} onSubmit={assign}><label className="field"><span className="field-label">用户</span><select value={form.member_id} onChange={e => setForm({...form,member_id:e.target.value})}>{users.map(user => <option key={user.id} value={user.id}>{user.name}</option>)}</select></label><label className="field"><span className="field-label">套餐</span><select value={form.plan_id} onChange={e => setForm({...form,plan_id:e.target.value})}>{plans.map(plan => <option key={plan.id} value={plan.id}>{plan.name}</option>)}</select></label><label className="field"><span className="field-label">到期时间（留空时使用套餐默认有效期）</span><input type="datetime-local" value={form.expires_at} onChange={e => setForm({...form,expires_at:e.target.value})}/></label><label className="field"><span className="field-label">备注</span><textarea value={form.notes} onChange={e => setForm({...form,notes:e.target.value})}/></label></Modal>}</div>;
};

export const AdminUserEntitlements: React.FC<{ memberId:string }> = ({ memberId }) => {
  const [member,setMember] = useState<any>(), [accounts,setAccounts] = useState<any[]>([]), [pools,setPools] = useState<any[]>([]), [accountGrants,setAccountGrants] = useState<any[]>([]), [poolGrants,setPoolGrants] = useState<any[]>([]), [kind,setKind] = useState<"account"|"pool">("account"), [targetID,setTargetID] = useState(""), [priority,setPriority] = useState("50"), [notes,setNotes] = useState(""), [error,setError] = useState("");
  const load = useCallback(async () => { try { const [users,accountResult,poolResult,direct,pooled] = await Promise.all([api.get<any>("/api/admin/users"),api.get<any>("/api/admin/accounts"),api.get<any>("/api/admin/account-pools"),api.get<any>(`/api/admin/users/${memberId}/account-grants`),api.get<any>(`/api/admin/users/${memberId}/pool-grants`)]); setMember((users.data ?? []).find((user:any) => user.id === memberId)); setAccounts((accountResult.data ?? []).filter((account:any) => account.state === "active")); setPools(poolResult.data ?? []); setAccountGrants(direct.data ?? []); setPoolGrants(pooled.data ?? []); } catch (e) { setError(errorText(e)); } },[memberId]); useEffect(() => { load(); },[load]);
  const candidates = kind === "account" ? accounts : pools; useEffect(() => { setTargetID(candidates[0]?.id ?? ""); },[kind,accounts.length,pools.length]);
  const add = async () => { try { await api.post(`/api/admin/users/${memberId}/${kind === "account" ? "account-grants" : "pool-grants"}`,{target_id:targetID,priority:Number(priority),notes}); setNotes(""); load(); } catch (e) { setError(errorText(e)); } };
  const update = async (grant:any, grantKind:"account"|"pool", status:string) => { try { await api.patch(`/api/admin/users/${memberId}/${grantKind === "account" ? "account-grants" : "pool-grants"}/${grant.id}`,{status,version:grant.version}); load(); } catch (e) { setError(errorText(e)); } };
  const Grants: React.FC<{rows:any[]; grantKind:"account"|"pool"; title:string}> = ({rows,grantKind,title}) => <section className="section-card"><div className="section-title"><h2>{title}</h2><span className="muted">仅作为套餐账号池之外的追加资源</span></div>{rows.length ? <table className="tbl"><thead><tr><th>资源</th><th>优先级</th><th>状态</th><th>备注</th><th/></tr></thead><tbody>{rows.map(grant => <tr key={grant.id}><td>{grant.target_name}</td><td>{grant.priority}</td><td><Badge value={grant.status}/></td><td>{empty(grant.notes,"无")}</td><td className="actions">{grant.status !== "revoked" && <button className="btn small danger" onClick={() => update(grant,grantKind,"revoked")}>撤销</button>}{grant.status === "disabled" && <button className="btn small" onClick={() => update(grant,grantKind,"active")}>恢复</button>}</td></tr>)}</tbody></table> : <Empty text="尚未追加资源。"/>}</section>;
  return <div className="portal"><div className="page-head"><h2>{member?.name ?? "用户"} · 资源追加</h2><div className="grow"/><a className="btn" href="#/users">返回用户管理</a><button className="btn" onClick={load}>刷新</button></div><p className="muted">套餐提供基础账号池；这里可以按需给该用户额外绑定单个上游账号或账号池。</p>{error && <div className="error">{error}</div>}<section className="section-card"><h2>追加资源</h2><div className="form-grid"><label className="field"><span className="field-label">资源类型</span><select value={kind} onChange={e => setKind(e.target.value as "account"|"pool")}><option value="account">单个上游账号</option><option value="pool">账号池</option></select></label><label className="field"><span className="field-label">选择资源</span><select value={targetID} onChange={e => setTargetID(e.target.value)}>{candidates.map(item => <option key={item.id} value={item.id}>{item.label ?? item.name}</option>)}</select></label><label className="field"><span className="field-label">优先级</span><input type="number" min="1" value={priority} onChange={e => setPriority(e.target.value)}/></label><label className="field"><span className="field-label">备注</span><input value={notes} onChange={e => setNotes(e.target.value)}/></label></div><button className="btn primary" disabled={!targetID} onClick={add}>追加给此用户</button></section><Grants rows={accountGrants} grantKind="account" title="已分配的单个账号"/><Grants rows={poolGrants} grantKind="pool" title="已分配的账号池"/></div>;
};
