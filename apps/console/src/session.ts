type Envelope<T> = { data: T; error?: { message?: string } };

async function post<T>(path: string, body: unknown): Promise<Envelope<T>> {
  const response = await fetch(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": crypto.randomUUID(),
    },
    body: JSON.stringify(body),
  });
  const payload = (await response.json()) as Envelope<T> & { error?: { message?: string } };
  if (!response.ok) {
    throw new Error(payload.error?.message || `request failed (${response.status})`);
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

export type Profile = {
  name: string;
  roles: string[];
  personas: string[];
  accessToken: string;
};

export async function openSession(loginName: string, password: string): Promise<Profile> {
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
  const tokens = await post<{ access_token: string }>("/api/v1/auth/token", {
    session_id: session.data.session_id,
    device_id: enrolled.data.device_id,
  });
  const me = await fetch("/api/v1/me", {
    headers: { Authorization: `Bearer ${tokens.data.access_token}` },
  });
  const profile = (await me.json()) as Envelope<{ name?: string; roles?: string[]; personas?: string[] }>;
  if (!me.ok || !profile.data?.name) {
    throw new Error(profile.error?.message || "Current user was not returned");
  }
  return {
    name: profile.data.name,
    roles: profile.data.roles ?? [],
    personas: profile.data.personas ?? [],
    accessToken: tokens.data.access_token,
  };
}

export async function signIn(loginName: string, password: string): Promise<string> {
  return (await openSession(loginName, password)).name;
}
