// Thin API client. The current user is sent as X-Bywork-User; in production
// the SSO gateway injects this header and the user switcher is hidden.

export class ApiError extends Error {
  code: number;
  data: any;
  constructor(code: number, msg: string, data?: any) {
    super(msg);
    this.code = code;
    this.data = data;
  }
}

const USER_KEY = 'bywork.user';

export function currentUser(): string {
  try {
    return localStorage.getItem(USER_KEY) || 'rm_a';
  } catch {
    return 'rm_a';
  }
}

export function setCurrentUser(u: string) {
  try {
    localStorage.setItem(USER_KEY, u);
  } catch {
    /* ignore */
  }
}

export async function api<T = any>(method: string, path: string, body?: any): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json', 'X-Bywork-User': currentUser() },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let json: any = null;
  try {
    json = await res.json();
  } catch {
    throw new ApiError(res.status, `HTTP ${res.status}`);
  }
  if (!res.ok || (json && json.code !== 0)) {
    throw new ApiError(json?.code ?? res.status, json?.msg ?? `HTTP ${res.status}`, json?.data);
  }
  return json.data as T;
}

export const get = <T = any>(p: string) => api<T>('GET', p);
export const post = <T = any>(p: string, b?: any) => api<T>('POST', p, b ?? {});
export const put = <T = any>(p: string, b?: any) => api<T>('PUT', p, b ?? {});
export const del = <T = any>(p: string) => api<T>('DELETE', p);

export function requestKey() {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

const pad = (n: number) => String(n).padStart(2, '0');

// Display in UTC+8 (Asia/Kuala_Lumpur), matching the server's card formatting.
export function fmtTime(ms?: number, withDate = true) {
  if (!ms) return '—';
  const d = new Date(ms + 8 * 3600 * 1000);
  const t = `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
  return withDate ? `${pad(d.getUTCMonth() + 1)}/${pad(d.getUTCDate())} ${t}` : t;
}

export function fmtDuration(ms?: number) {
  if (!ms || ms < 0) return '—';
  const m = Math.floor(ms / 60000);
  if (m < 60) return `${m} 分钟`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} 小时`;
  return `${Math.floor(h / 24)} 天`;
}

export const closeReasonText: Record<string, string> = {
  completed: '处理完成',
  invalidated: 'Case 已失效',
  admin_terminated: '管理员终止',
  flow_failed: '流程执行失败',
  shadow_ended: '影子运行结束',
};

export const typeStatusText: Record<string, string> = {
  draft: '草稿',
  shadow: '影子运行',
  enabled: '启用',
  disabled: '停用',
};

export const attentionText: Record<string, string> = {
  rm_missing: 'RM 映射缺失',
  delivery_failed: '投递失败',
  data_read_failed: '数据读取失败',
  flow_error: '流程执行异常',
  assignee_left: '处理人已离开团队',
};
