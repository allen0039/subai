// Minimal API client for the SubAI admin surface (§17.2).
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const resp = await fetch(path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
  });
  if (resp.status === 401 && !path.endsWith("/session")) {
    throw new ApiError(401, "会话已过期，请重新登录");
  }
  const text = await resp.text();
  let data: any = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* non-JSON */
  }
  if (!resp.ok) {
    const msg = data?.error?.message ?? data?.error ?? text ?? resp.statusText;
    throw new ApiError(resp.status, typeof msg === "string" ? msg : JSON.stringify(msg));
  }
  return data as T;
}

export const api = {
  get: <T>(p: string) => request<T>("GET", p),
  post: <T>(p: string, b?: unknown) => request<T>("POST", p, b ?? {}),
  patch: <T>(p: string, b: unknown) => request<T>("PATCH", p, b),
  del: <T>(p: string, b?: unknown) => request<T>("DELETE", p, b ?? {}),
};

export interface ListResponse<T = any> {
  data: T[];
  limit?: number;
  offset?: number;
}
