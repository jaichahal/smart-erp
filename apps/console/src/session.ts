export type Profile = {
  id: string;
  name: string;
  roles: string[];
  personas: string[];
  companyId: string;
  accessToken: string;
};

type Envelope<T> = { data: T; error?: { code?: string; message?: string } };

export class ApiError extends Error {
  code: string;

  constructor(code: string, message: string) {
    super(message);
    this.code = code;
  }
}

async function post<T>(path: string, body: unknown): Promise<Envelope<T>> {
  const response = await fetch(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": crypto.randomUUID(),
    },
    body: JSON.stringify(body),
  });
  const payload = (await response.json()) as Envelope<T>;
  if (!response.ok) {
    throw new ApiError(payload.error?.code || "REQUEST_FAILED", payload.error?.message || `request failed (${response.status})`);
  }
  return payload;
}

async function consolePublicKey(): Promise<Record<string, string>> {
  const key = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign"]);
  const jwk = await crypto.subtle.exportKey("jwk", key.publicKey);
  if (!jwk.kty || !jwk.crv || !jwk.x || !jwk.y) {
    throw new Error("browser did not export an EC public key");
  }
  return { kty: jwk.kty, crv: jwk.crv, x: jwk.x, y: jwk.y };
}

export async function signIn(loginName: string, password: string): Promise<Profile> {
  const publicKey = await consolePublicKey();
  const enrolled = await post<{ device_id: string }>("/api/v1/auth/device/enroll", {
    public_key: publicKey,
    platform: "console",
    app_version: "0.1.0",
    device_name: "console",
  });
  const session = await post<{ session_id: string }>("/api/v1/auth/session", { login_name: loginName });
  const checked = await post<{ verified?: boolean }>(`/api/v1/auth/session/${session.data.session_id}/check`, { password });
  if (!checked.data.verified) {
    throw new Error("Sign-in was not verified");
  }
  const tokens = await post<{ access_token: string; user?: ProfileWire }>("/api/v1/auth/token", {
    session_id: session.data.session_id,
    device_id: enrolled.data.device_id,
  });
  const me = await fetch("/api/v1/me", {
    headers: { Authorization: `Bearer ${tokens.data.access_token}`, Accept: "application/json" },
  });
  const profile = (await me.json()) as Envelope<ProfileWire>;
  if (!me.ok || !profile.data?.name) {
    throw new Error(profile.error?.message || "Current user was not returned");
  }
  return {
    id: profile.data.id,
    name: profile.data.name,
    roles: profile.data.roles ?? [],
    personas: profile.data.personas ?? [],
    companyId: profile.data.company_id,
    accessToken: tokens.data.access_token,
  };
}

type ProfileWire = {
  id: string;
  name: string;
  roles?: string[];
  personas?: string[];
  company_id: string;
};

export async function apiSend(token: string, method: string, path: string, body?: unknown): Promise<{ status: number; data: unknown; error?: { code?: string; message?: string } }> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    Authorization: `Bearer ${token}`,
  };
  let payload: string | undefined;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["Idempotency-Key"] = crypto.randomUUID();
    payload = JSON.stringify(body);
  }
  const response = await fetch(path, { method, headers, body: payload });
  const json = (await response.json()) as Envelope<unknown>;
  return { status: response.status, data: json.data, error: json.error };
}
