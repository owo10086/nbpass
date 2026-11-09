export class APIError extends Error {
  constructor(message: string, public status: number, public details: Record<string, unknown> = {}) { super(message); }
}

export async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  return apiRawJSON<T>(path, method, body === undefined ? undefined : JSON.stringify(body));
}

// 规则文件导入保留原始 JSON，让服务端能够拒绝重复键等歧义输入。
export async function apiRawJSON<T>(path: string, method = 'GET', body?: string): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body,
  });
  if (response.status === 204) return undefined as T;
  const result = await response.json();
  if (!response.ok) throw new APIError(result.error ?? '操作失败，请重试', response.status, result);
  return result as T;
}
