export type ApiResult = {
  ok: boolean;
  status: number;
  body: unknown;
  text: string;
};

export async function apiCall(token: string, method: string, path: string, payload?: unknown): Promise<ApiResult> {
  const response = await fetch(path, {
    method,
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      ...(payload === undefined
        ? {}
        : {
            "Content-Type": "application/json",
            "Idempotency-Key": crypto.randomUUID(),
          }),
    },
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  const text = await response.text();
  let body: unknown = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }
  return { ok: response.ok, status: response.status, body, text };
}

export function apiFailure(method: string, path: string, result: ApiResult): string {
  const error = (result.body as { error?: { message?: string } } | null)?.error?.message;
  const detail = error || result.text.slice(0, 300);
  return `${method} ${path} failed (${result.status}) ${detail}`;
}
