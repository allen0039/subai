import React, { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { Badge, Modal } from "./components";

const fmt = (v: unknown) => (v === "" || v === null || v === undefined ? "—" : String(v));

export const MyOverview: React.FC = () => {
  const [subscriptions, setSubscriptions] = useState<any[]>([]);
  const [keys, setKeys] = useState<any[]>([]);
  useEffect(() => {
    api.get<any>("/api/admin/me/subscriptions").then((r) => setSubscriptions(r.data ?? [])).catch(() => {});
    api.get<any>("/api/admin/me/keys").then((r) => setKeys(r.data ?? [])).catch(() => {});
  }, []);
  const active = subscriptions.filter((s) => s.status === "active");
  return <div className="portal">
    <div className="portal-hero"><span>个人控制台</span><h1>选择订阅，创建你的 Key</h1><p>每个 Key 都绑定一份订阅；额度和可用账号随订阅实时生效。</p></div>
    <div className="metric-grid">
      <div className="metric"><b>{active.length}</b><span>可用订阅</span></div>
      <div className="metric"><b>{keys.filter((k) => k.status === "active").length}</b><span>启用 Key</span></div>
      <div className="metric"><b>{subscriptions.filter((s) => s.status === "expired").length}</b><span>已到期订阅</span></div>
    </div>
    <section className="section-card"><div className="section-title"><h2>可用订阅</h2><a className="btn small" href="#/my-subscriptions">查看全部</a></div>
      {active.length === 0 ? <Empty text="还没有可用订阅。请联系管理员分配套餐。" /> : <div className="subscription-cards">{active.map((s) => <SubscriptionCard key={s.id} sub={s} />)}</div>}
    </section>
  </div>;
};

const SubscriptionCard: React.FC<{ sub: any }> = ({ sub }) => <div className="subscription-card">
  <div><span className="eyebrow">订阅 #{sub.plan_version}</span><h3>{sub.plan_name}</h3></div><Badge value={sub.status} />
  <div className="quota-line"><span>日额度</span><b>${fmt(sub.daily_limit_usd)}</b></div>
  <div className="quota-line"><span>周额度</span><b>${fmt(sub.weekly_limit_usd)}</b></div>
  <div className="quota-line"><span>月额度</span><b>${fmt(sub.monthly_limit_usd)}</b></div>
  <small>到期：{fmt(sub.expires_at)} · 并发：{sub.concurrency_limit} · Key：{sub.max_keys}</small>
</div>;

export const MySubscriptions: React.FC = () => {
  const [rows, setRows] = useState<any[]>([]); const [error,setError]=useState("");
  const load=useCallback(()=>api.get<any>("/api/admin/me/subscriptions").then((r)=>setRows(r.data??[])).catch((e)=>setError(e.message)),[]);
  useEffect(()=>{load();},[load]);
  return <div className="portal"><div className="page-head"><div><span className="eyebrow">我的资源</span><h2>我的订阅</h2></div><div className="grow"/><button className="btn" onClick={load}>刷新</button></div>{error&&<div className="error">{error}</div>}
    <div className="subscription-cards">{rows.length?rows.map((s)=><SubscriptionCard key={s.id} sub={s}/>):<Empty text="暂时没有套餐订阅。"/>}</div>
  </div>;
};

export const MyKeys: React.FC = () => {
  const [keys,setKeys]=useState<any[]>([]),[subs,setSubs]=useState<any[]>([]),[error,setError]=useState(""),[flash,setFlash]=useState(""),[show,setShow]=useState(false);
  const [form,setForm]=useState({subscription_id:"",name:"",concurrency_limit:"1",expires_at:"",allowed_models:""});
  const load=useCallback(async()=>{try{const [k,s]=await Promise.all([api.get<any>("/api/admin/me/keys"),api.get<any>("/api/admin/me/subscriptions")]);setKeys(k.data??[]);setSubs((s.data??[]).filter((v:any)=>v.status==="active"));}catch(e:any){setError(e.message)}},[]);
  useEffect(()=>{load()},[load]);
  const create=async()=>{try{const allowed_models=form.allowed_models.split(",").map(s=>s.trim()).filter(Boolean);const r=await api.post<any>("/api/admin/me/keys",{subscription_id:form.subscription_id,name:form.name,concurrency_limit:Number(form.concurrency_limit),expires_at:form.expires_at?new Date(form.expires_at).toISOString():undefined,allowed_models});setShow(false);setFlash(`Key 已创建，请立即保存：${r.key}`);load()}catch(e:any){setError(e.message)}};
  const patch=async(k:any,status:string)=>{try{await api.patch(`/api/admin/me/keys/${k.id}`,{status,version:k.version});load()}catch(e:any){setError(e.message)}};
  const revoke=async(k:any)=>{if(!confirm(`撤销 ${k.name} 后无法恢复，确定吗？`))return;try{await api.post(`/api/admin/me/keys/${k.id}/revoke`);load()}catch(e:any){setError(e.message)}};
  return <div className="portal"><div className="page-head"><div><span className="eyebrow">身份凭证</span><h2>我的 API Key</h2></div><div className="grow"/><button className="btn primary" disabled={!subs.length} onClick={()=>{setForm({subscription_id:subs[0]?.id??"",name:"",concurrency_limit:"1",expires_at:"",allowed_models:""});setShow(true)}}>创建 Key</button><button className="btn" onClick={load}>刷新</button></div>
    {!subs.length&&<div className="error">没有可用于创建 Key 的有效订阅。</div>}{flash&&<div className="flash" onClick={()=>setFlash("")}>{flash}</div>}{error&&<div className="error">{error}</div>}
    <table className="tbl"><thead><tr><th>名称</th><th>所属套餐</th><th>前缀</th><th>状态</th><th>并发</th><th>到期</th><th/></tr></thead><tbody>{keys.map(k=><tr key={k.id}><td>{k.name}</td><td>{k.plan_name}</td><td><code>{k.public_prefix}</code></td><td><Badge value={k.status}/></td><td>{k.concurrency_limit}</td><td>{fmt(k.expires_at)}</td><td className="actions">{k.status!=="revoked"&&<><button className="btn small" onClick={()=>patch(k,k.status==="active"?"paused":"active")}>{k.status==="active"?"暂停":"恢复"}</button><button className="btn small danger" onClick={()=>revoke(k)}>撤销</button></>}</td></tr>)}</tbody></table>
    {show&&<Modal title="创建 API Key" onClose={()=>setShow(false)} onSubmit={create}><label className="field"><span className="field-label">使用的套餐订阅</span><select value={form.subscription_id} onChange={e=>setForm({...form,subscription_id:e.target.value})}>{subs.map(s=><option key={s.id} value={s.id}>{s.plan_name} · 到期 {s.expires_at}</option>)}</select></label><label className="field"><span className="field-label">Key 名称</span><input value={form.name} onChange={e=>setForm({...form,name:e.target.value})} required/></label><label className="field"><span className="field-label">并发上限</span><input type="number" min="1" value={form.concurrency_limit} onChange={e=>setForm({...form,concurrency_limit:e.target.value})}/></label><label className="field"><span className="field-label">Key 到期时间（可选）</span><input type="datetime-local" value={form.expires_at} onChange={e=>setForm({...form,expires_at:e.target.value})}/></label><label className="field"><span className="field-label">允许模型（可选，逗号分隔）</span><input value={form.allowed_models} onChange={e=>setForm({...form,allowed_models:e.target.value})}/></label></Modal>}
  </div>;
};

export const SecurityPage: React.FC = () => {
  const [oldPassword,setOld]=useState(""),[password,setPassword]=useState(""),[message,setMessage]=useState(""),[error,setError]=useState("");
  const submit=async()=>{try{await api.patch("/api/admin/me/password",{old_password:oldPassword,new_password:password});setMessage("密码已更新，其他登录会话已注销。");setOld("");setPassword("")}catch(e:any){setError(e.message)}};
  return <div className="portal narrow"><span className="eyebrow">账户安全</span><h2>修改密码</h2><p className="muted">修改成功后，当前会话会保留，其他设备上的登录会话会退出。</p>{message&&<div className="flash">{message}</div>}{error&&<div className="error">{error}</div>}<label className="field"><span className="field-label">当前密码</span><input type="password" value={oldPassword} onChange={e=>setOld(e.target.value)}/></label><label className="field"><span className="field-label">新密码</span><input type="password" value={password} onChange={e=>setPassword(e.target.value)}/></label><button className="btn primary" onClick={submit}>更新密码</button></div>;
};

export const AdminPlans: React.FC = () => {
  const [plans,setPlans]=useState<any[]>([]),[pools,setPools]=useState<any[]>([]),[error,setError]=useState(""),[show,setShow]=useState(false);
  const [form,setForm]=useState<any>({name:"",description:"",daily_limit_usd:"",weekly_limit_usd:"",monthly_limit_usd:"",concurrency_limit:"1",max_keys:"1",default_validity_days:"30",allowed_models:"",pool_ids:[] as string[]});
  const load=useCallback(async()=>{try{const [p,g]=await Promise.all([api.get<any>("/api/admin/plans"),api.get<any>("/api/admin/account-pools")]);setPlans(p.data??[]);setPools(g.data??[])}catch(e:any){setError(e.message)}},[]);useEffect(()=>{load()},[load]);
  const create=async()=>{try{await api.post("/api/admin/plans",{...form,concurrency_limit:Number(form.concurrency_limit),max_keys:Number(form.max_keys),default_validity_days:Number(form.default_validity_days),allowed_models:form.allowed_models.split(",").map((x:string)=>x.trim()).filter(Boolean)});setShow(false);load()}catch(e:any){setError(e.message)}};
  const publish=async(id:string)=>{try{await api.post(`/api/admin/plans/${id}/publish`);load()}catch(e:any){setError(e.message)}};
  return <div className="portal"><div className="page-head"><div><span className="eyebrow">资源产品</span><h2>套餐管理</h2></div><div className="grow"/><button className="btn primary" onClick={()=>{setForm({name:"",description:"",daily_limit_usd:"",weekly_limit_usd:"",monthly_limit_usd:"",concurrency_limit:"1",max_keys:"1",default_validity_days:"30",allowed_models:"",pool_ids:[]});setShow(true)}}>创建套餐草稿</button><button className="btn" onClick={load}>刷新</button></div>{error&&<div className="error">{error}</div>}<table className="tbl"><thead><tr><th>套餐</th><th>状态</th><th>额度（日/周/月）</th><th>并发 / Key</th><th>账号池</th><th/></tr></thead><tbody>{plans.map(p=><tr key={p.id}><td><b>{p.name}</b><br/><small className="muted">v{p.plan_version} · {p.description}</small></td><td><Badge value={p.status}/></td><td>${fmt(p.daily_limit_usd)} / ${fmt(p.weekly_limit_usd)} / ${fmt(p.monthly_limit_usd)}</td><td>{p.concurrency_limit} / {p.max_keys}</td><td>{(p.pools??[]).map((x:any)=>x.name).join("、")||"—"}</td><td className="actions">{p.status==="draft"&&<button className="btn small primary" onClick={()=>publish(p.id)}>发布</button>}</td></tr>)}</tbody></table>
    {show&&<Modal title="创建套餐草稿" onClose={()=>setShow(false)} onSubmit={create}><label className="field"><span className="field-label">套餐名称</span><input value={form.name} onChange={e=>setForm({...form,name:e.target.value})} required/></label><label className="field"><span className="field-label">说明</span><textarea value={form.description} onChange={e=>setForm({...form,description:e.target.value})}/></label><div className="form-grid">{[["daily_limit_usd","日额度 $"],["weekly_limit_usd","周额度 $"],["monthly_limit_usd","月额度 $"]].map(([key,label])=><label className="field" key={key}><span className="field-label">{label}</span><input value={form[key]} placeholder="留空=不限制" onChange={e=>setForm({...form,[key]:e.target.value})}/></label>)}</div><div className="form-grid"><label className="field"><span className="field-label">并发</span><input type="number" value={form.concurrency_limit} onChange={e=>setForm({...form,concurrency_limit:e.target.value})}/></label><label className="field"><span className="field-label">Key 数量</span><input type="number" value={form.max_keys} onChange={e=>setForm({...form,max_keys:e.target.value})}/></label></div><label className="field"><span className="field-label">模型（逗号分隔，留空=全部）</span><input value={form.allowed_models} onChange={e=>setForm({...form,allowed_models:e.target.value})}/></label><span className="field-label">绑定账号池</span><div className="check-list">{pools.map(p=><label key={p.id}><input type="checkbox" checked={form.pool_ids.includes(p.id)} onChange={e=>setForm({...form,pool_ids:e.target.checked?[...form.pool_ids,p.id]:form.pool_ids.filter((id:string)=>id!==p.id)})}/>{p.name} <small>({p.accounts?.length??0} 个账号)</small></label>)}</div></Modal>}
  </div>;
};

export const AdminSubscriptions: React.FC = () => {
  const [rows,setRows]=useState<any[]>([]),[users,setUsers]=useState<any[]>([]),[plans,setPlans]=useState<any[]>([]),[error,setError]=useState(""),[show,setShow]=useState(false);const [form,setForm]=useState({member_id:"",plan_id:"",expires_at:"",notes:""});
  const load=useCallback(async()=>{try{const [s,u,p]=await Promise.all([api.get<any>("/api/admin/subscriptions"),api.get<any>("/api/admin/users"),api.get<any>("/api/admin/plans")]);setRows(s.data??[]);setUsers(u.data??[]);setPlans((p.data??[]).filter((x:any)=>x.status==="active"))}catch(e:any){setError(e.message)}},[]);useEffect(()=>{load()},[load]);
  const assign=async()=>{try{await api.post("/api/admin/subscriptions",{...form,expires_at:form.expires_at?new Date(form.expires_at).toISOString():undefined});setShow(false);load()}catch(e:any){setError(e.message)}};
  return <div className="portal"><div className="page-head"><div><span className="eyebrow">用户权益</span><h2>订阅分配</h2></div><div className="grow"/><button className="btn primary" onClick={()=>{setForm({member_id:users[0]?.id??"",plan_id:plans[0]?.id??"",expires_at:"",notes:""});setShow(true)}}>分配套餐</button><button className="btn" onClick={load}>刷新</button></div>{error&&<div className="error">{error}</div>}<table className="tbl"><thead><tr><th>用户</th><th>套餐</th><th>状态</th><th>到期</th><th>额度（日/周/月）</th></tr></thead><tbody>{rows.map(s=><tr key={s.id}><td>{s.member_id}</td><td>{s.plan_name} <small className="muted">v{s.plan_version}</small></td><td><Badge value={s.status}/></td><td>{s.expires_at}</td><td>${fmt(s.daily_limit_usd)} / ${fmt(s.weekly_limit_usd)} / ${fmt(s.monthly_limit_usd)}</td></tr>)}</tbody></table>
    {show&&<Modal title="分配套餐" onClose={()=>setShow(false)} onSubmit={assign}><label className="field"><span className="field-label">用户</span><select value={form.member_id} onChange={e=>setForm({...form,member_id:e.target.value})}>{users.map(u=><option key={u.id} value={u.id}>{u.name}</option>)}</select></label><label className="field"><span className="field-label">套餐</span><select value={form.plan_id} onChange={e=>setForm({...form,plan_id:e.target.value})}>{plans.map(p=><option key={p.id} value={p.id}>{p.name}</option>)}</select></label><label className="field"><span className="field-label">到期时间（留空使用套餐默认天数）</span><input type="datetime-local" value={form.expires_at} onChange={e=>setForm({...form,expires_at:e.target.value})}/></label><label className="field"><span className="field-label">备注</span><textarea value={form.notes} onChange={e=>setForm({...form,notes:e.target.value})}/></label></Modal>}
  </div>;
};

export const AdminUserEntitlements: React.FC<{ memberId: string }> = ({ memberId }) => {
  const [member, setMember] = useState<any>();
  const [accounts, setAccounts] = useState<any[]>([]), [pools, setPools] = useState<any[]>([]);
  const [accountGrants, setAccountGrants] = useState<any[]>([]), [poolGrants, setPoolGrants] = useState<any[]>([]);
  const [kind, setKind] = useState<"account" | "pool">("account");
  const [targetID, setTargetID] = useState(""), [priority, setPriority] = useState("50"), [notes, setNotes] = useState("");
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      const [users, accountsResult, poolsResult, direct, pooled] = await Promise.all([
        api.get<any>("/api/admin/users"), api.get<any>("/api/admin/accounts"), api.get<any>("/api/admin/account-pools"),
        api.get<any>(`/api/admin/users/${memberId}/account-grants`), api.get<any>(`/api/admin/users/${memberId}/pool-grants`),
      ]);
      setMember((users.data ?? []).find((u: any) => u.id === memberId));
      setAccounts((accountsResult.data ?? []).filter((a: any) => a.status === "active"));
      setPools(poolsResult.data ?? []); setAccountGrants(direct.data ?? []); setPoolGrants(pooled.data ?? []);
    } catch (e: any) { setError(e.message); }
  }, [memberId]);
  useEffect(() => { load(); }, [load]);
  const candidates = kind === "account" ? accounts : pools;
  useEffect(() => { setTargetID(candidates[0]?.id ?? ""); }, [kind, accounts.length, pools.length]);
  const add = async () => {
    try {
      await api.post(`/api/admin/users/${memberId}/${kind === "account" ? "account-grants" : "pool-grants"}`, { target_id: targetID, priority: Number(priority), notes });
      setNotes(""); load();
    } catch (e: any) { setError(e.message); }
  };
  const setGrantStatus = async (grant: any, grantKind: "account" | "pool", status: string) => {
    try { await api.patch(`/api/admin/users/${memberId}/${grantKind === "account" ? "account-grants" : "pool-grants"}/${grant.id}`, { status, version: grant.version }); load(); }
    catch (e: any) { setError(e.message); }
  };
  const GrantTable: React.FC<{ rows: any[]; grantKind: "account" | "pool"; label: string }> = ({ rows, grantKind, label }) => <section className="section-card">
    <div className="section-title"><h2>{label}</h2><span className="muted">仅作为套餐账号池之外的追加资源</span></div>
    {rows.length === 0 ? <Empty text="尚未追加资源。" /> : <table className="tbl"><thead><tr><th>资源</th><th>优先级</th><th>状态</th><th>备注</th><th /></tr></thead><tbody>{rows.map(g => <tr key={g.id}><td>{g.target_name}</td><td>{g.priority}</td><td><Badge value={g.status} /></td><td>{g.notes || "—"}</td><td className="actions">{g.status !== "revoked" && <button className="btn small danger" onClick={() => setGrantStatus(g, grantKind, "revoked")}>撤销</button>}{g.status === "disabled" && <button className="btn small" onClick={() => setGrantStatus(g, grantKind, "active")}>恢复</button>}</td></tr>)}</tbody></table>}
  </section>;
  return <div className="portal">
    <div className="page-head"><div><span className="eyebrow">用户权益</span><h2>{member?.name ?? "用户"} · 资源追加</h2></div><div className="grow" /><a className="btn" href="#/users">返回用户管理</a><button className="btn" onClick={load}>刷新</button></div>
    <p className="muted">套餐负责基础账号池；这里可按需给该用户额外绑定单个上游账号或多个账号池。</p>{error && <div className="error">{error}</div>}
    <section className="section-card"><div className="section-title"><h2>追加资源</h2></div><div className="form-grid"><label className="field"><span className="field-label">资源类型</span><select value={kind} onChange={e => setKind(e.target.value as "account" | "pool")}><option value="account">单个上游账号</option><option value="pool">账号池</option></select></label><label className="field"><span className="field-label">选择资源</span><select value={targetID} onChange={e => setTargetID(e.target.value)}>{candidates.map((item: any) => <option key={item.id} value={item.id}>{item.label ?? item.name}</option>)}</select></label><label className="field"><span className="field-label">优先级</span><input type="number" min="1" value={priority} onChange={e => setPriority(e.target.value)} /></label><label className="field"><span className="field-label">备注</span><input value={notes} onChange={e => setNotes(e.target.value)} /></label></div><button className="btn primary" disabled={!targetID} onClick={add}>追加给此用户</button></section>
    <GrantTable rows={accountGrants} grantKind="account" label="已分配的单个账号" />
    <GrantTable rows={poolGrants} grantKind="pool" label="已分配的账号池" />
  </div>;
};

const Empty: React.FC<{text:string}> = ({text}) => <div className="empty-state">{text}</div>;
