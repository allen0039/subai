import React, { useEffect, useState, useCallback } from "react";
import { api, ListResponse } from "./api";

// ── Generic resource table + create/edit modal (§17.2 surface) ──────────────

export type FieldKind = "text" | "number" | "select" | "textarea" | "checkbox" | "password";

export interface Field {
  name: string;
  label: string;
  kind?: FieldKind;
  options?: string[];
  optionsPath?: string;
  required?: boolean;
  placeholder?: string;
  help?: string;
  default?: any;
  // hide in create form (server-generated)
  omitInCreate?: boolean;
}

export interface Column {
  name: string;
  label: string;
  // render override
  render?: (row: any) => React.ReactNode;
}

export interface ResourcePageProps {
  title: string;
  basePath: string; // e.g. /api/admin/members
  columns: Column[];
  createFields?: Field[]; // omit to disable create
  editFields?: Field[]; // patch body; uses row.version automatically
  rowActions?: (row: any, reload: () => void) => React.ReactNode;
  notice?: React.ReactNode;
}

export const ResourcePage: React.FC<ResourcePageProps> = ({
  title,
  basePath,
  columns,
  createFields,
  editFields,
  rowActions,
  notice,
}) => {
  const [rows, setRows] = useState<any[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [createBody, setCreateBody] = useState<Record<string, any>>({});
  const [editRow, setEditRow] = useState<any | null>(null);
  const [editBody, setEditBody] = useState<Record<string, any>>({});
  const [flash, setFlash] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const pageSize = 20;

  const load = useCallback(async (off = 0) => {
    setLoading(true);
    setError(null);
    try {
      const sep = basePath.includes("?") ? "&" : "?";
      // fetch one extra row to detect a next page (review P2-13: 分页)
      const r = await api.get<ListResponse>(`${basePath}${sep}limit=${pageSize + 1}&offset=${off}`);
      const data = r.data ?? [];
      setRows(data.slice(0, pageSize));
      setHasMore(data.length > pageSize);
      setOffset(off);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  }, [basePath]);

  const [hasMore, setHasMore] = useState(false);
  const [offset, setOffset] = useState(0);

  useEffect(() => {
    load(0);
  }, [load]);

  const openCreate = () => {
    // Initialize real form values from field defaults (review P2-13), typed
    // per field kind (review R2-07): optional numbers stay undefined so we
    // never submit "" where the API expects *int.
    const body: Record<string, any> = {};
    for (const f of createFields ?? []) {
      if (f.default !== undefined) {
        body[f.name] = f.default;
      } else if (f.kind === "number") {
        body[f.name] = undefined;
      } else if (f.kind === "checkbox") {
        body[f.name] = false;
      } else {
        body[f.name] = "";
      }
    }
    setCreateBody(body);
    setShowCreate(true);
  };

  const doCreate = async () => {
    try {
      const resp = await api.post<any>(basePath, buildCreatePayload(createFields ?? [], createBody));
      setShowCreate(false);
      setCreateBody({});
      if (resp?.key) {
        setFlash(`Key 已创建，仅此一次显示，请立即保存：${resp.key}`);
      } else {
        setFlash("已创建");
      }
      load(offset);
    } catch (e: any) {
      setError(e.message);
    }
  };

  const doPatch = async () => {
    try {
      const patch = { ...editBody };
      if (patch.proxy_id === "") delete patch.proxy_id;
      await api.patch(`${basePath}/${editRow.id}`, { ...patch, version: editRow.version });
      setEditRow(null);
      setFlash("已更新");
      load(offset);
    } catch (e: any) {
      setError(e.message);
    }
  };

  return (
    <div>
      <div className="page-head">
        <h2>{title}</h2>
        <div className="grow" />
        {notice}
        {createFields && (
          <button className="btn primary" onClick={openCreate}>
            新建
          </button>
        )}
        <button className="btn" onClick={() => load(offset)}>
          刷新
        </button>
      </div>
      <div className="pager">
        <button className="btn small" disabled={offset === 0 || loading} onClick={() => load(Math.max(0, offset - pageSize))}>
          上一页
        </button>
        <span className="muted small">
          第 {offset + 1}–{offset + rows.length} 条
        </span>
        <button className="btn small" disabled={!hasMore || loading} onClick={() => load(offset + pageSize)}>
          下一页
        </button>
      </div>
      {flash && (
        <div className="flash" onClick={() => setFlash(null)}>
          {flash}
        </div>
      )}
      {error && <div className="error">{error}</div>}
      {loading ? (
        <div className="muted">加载中…</div>
      ) : rows.length === 0 ? (
        <div className="muted">暂无数据（空状态）</div>
      ) : (
        <table className="tbl">
          <thead>
            <tr>
              {columns.map((c) => (
                <th key={c.name}>{c.label}</th>
              ))}
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id}>
                {columns.map((c) => (
                  <td key={c.name}>{c.render ? c.render(row) : String(row[c.name] ?? "")}</td>
                ))}
                <td className="actions">
                  {editFields && (
                    <button
                      className="btn small"
                      onClick={() => {
                        setError(null);
                        setEditRow(row);
                        setEditBody({});
                      }}
                    >
                      编辑
                    </button>
                  )}
                  {rowActions?.(row, load)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showCreate && createFields && (
        <Modal title={`新建 · ${title}`} onClose={() => setShowCreate(false)} onSubmit={doCreate}>
          {createFields.map((f) => (
            <FormField key={f.name} field={f} value={createBody[f.name] ?? f.default} onChange={(v) => setCreateBody((b) => ({ ...b, [f.name]: v }))} />
          ))}
        </Modal>
      )}
      {editRow && editFields && (
        <Modal title={`编辑 · ${title}`} onClose={() => setEditRow(null)} onSubmit={doPatch}>
          {error && <div className="error" role="alert">{error}</div>}
          {editFields.map((f) => (
            <FormField key={f.name} field={f} value={editBody[f.name] ?? editRow[f.name] ?? ""} onChange={(v) => setEditBody((b) => ({ ...b, [f.name]: v }))} />
          ))}
        </Modal>
      )}
    </div>
  );
};

// buildCreatePayload drops values that must not reach the API (review R2-07):
// undefined optional numbers, empty strings on non-required text fields, and
// normalizes number strings to real numbers.
export function buildCreatePayload(fields: Field[], body: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = {};
  for (const f of fields) {
    const v = body[f.name];
    if (v === undefined || v === null) continue;
    if (typeof v === "string") {
      const trimmed = v.trim();
      if (trimmed === "") {
        if (f.required) out[f.name] = ""; // let the backend produce a clear error
        continue;
      }
      if (f.kind === "number") {
        const n = Number(trimmed);
        if (!Number.isNaN(n)) {
          out[f.name] = n;
          continue;
        }
      }
      if (f.name === "allowed_models" || f.name === "fallback_proxy_ids" || f.name === "pool_ids") {
        out[f.name] = trimmed.split(",").map((s) => s.trim()).filter(Boolean);
        continue;
      }
      out[f.name] = v;
      continue;
    }
    out[f.name] = v;
  }
  return out;
}

export const FormField: React.FC<{ field: Field; value: any; onChange: (v: any) => void }> = ({
  field,
  value,
  onChange,
}) => {
  const { kind = "text", label, options, required, placeholder, help } = field;
  const [remoteOptions, setRemoteOptions] = useState<any[]>([]);
  const [optionsError, setOptionsError] = useState("");
  useEffect(() => {
    if (!field.optionsPath) return;
    let cancelled = false;
    (async () => {
      const rows: any[] = [];
      for (let offset = 0; ; offset += 100) {
        const result = await api.get<ListResponse>(`${field.optionsPath}?limit=100&offset=${offset}`);
        rows.push(...result.data);
        if (result.data.length < 100) break;
      }
      if (!cancelled) setRemoteOptions(rows);
    })().catch(e => { if (!cancelled) setOptionsError(e.message); });
    return () => { cancelled = true; };
  }, [field.optionsPath]);
  return (
    <label className="field">
      <span className="field-label">
        {label}
        {required && <b className="req">*</b>}
      </span>
      {kind === "select" ? (
        <select value={value ?? ""} onChange={(e) => onChange(e.target.value)} required={required}>
          <option value="">—</option>
          {field.optionsPath && remoteOptions.map(o => <option key={o.id} value={o.id} disabled={o.status === "disabled"}>{o.name}{o.endpoint ? ` · ${o.endpoint}` : ""}{o.primary ? ` · ${o.primary}` : ""}{o.status === "disabled" ? "（已禁用）" : ""}</option>)}
          {(options ?? []).map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      ) : kind === "textarea" ? (
        <textarea value={value ?? ""} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} rows={4} />
      ) : kind === "checkbox" ? (
        <input type="checkbox" checked={!!value} onChange={(e) => onChange(e.target.checked)} />
      ) : (
        <input
          type={kind === "number" ? "number" : kind === "password" ? "password" : "text"}
          value={value ?? ""}
          placeholder={placeholder}
          onChange={(e) => onChange(kind === "number" ? Number(e.target.value) : e.target.value)}
          required={required}
        />
      )}
      {optionsError && <span className="error">{optionsError}</span>}
      {help && <span className="help">{help}</span>}
    </label>
  );
};

export const Modal: React.FC<{ title: string; onClose: () => void; onSubmit: () => void; children: React.ReactNode }> = ({
  title,
  onClose,
  onSubmit,
  children,
}) => (
  <div className="modal-backdrop" onClick={onClose}>
    <div className="modal" onClick={(e) => e.stopPropagation()}>
      <h3>{title}</h3>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
      >
        {children}
        <div className="modal-actions">
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn primary">
            提交
          </button>
        </div>
      </form>
    </div>
  </div>
);

export const Badge: React.FC<{ value: string }> = ({ value }) => (
  <span className={`badge badge-${value}`}>{value}</span>
);
