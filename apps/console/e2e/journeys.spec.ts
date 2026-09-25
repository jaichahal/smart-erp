import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Locator, type Page, type APIRequestContext, test } from "@playwright/test";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");

function devPassword(): string {
  const file = readFileSync(path.join(repoRoot, "deploy/compose/.env.dev.example"), "utf8");
  const line = file.split("\n").find((row) => row.startsWith("ZITADEL_ADMIN_PASSWORD="));
  if (!line) {
    throw new Error("ZITADEL_ADMIN_PASSWORD is missing from deploy/compose/.env.dev.example");
  }
  return line.slice("ZITADEL_ADMIN_PASSWORD=".length).trim();
}

function seedDirectoryUser(): void {
  const sql = `INSERT INTO erp.identity_users (id, login_name, display_name, company_id, roles, personas, disabled, step_up_methods)
VALUES ('dev-admin', 'admin@dev.localhost', 'Dev Admin', '00000000-0000-4000-8000-000000000001', ARRAY['Sales Agent'], ARRAY['staff'], false, ARRAY['totp'])
ON CONFLICT (login_name) DO UPDATE SET display_name = EXCLUDED.display_name, roles = EXCLUDED.roles, personas = EXCLUDED.personas, disabled = false;`;
  execFileSync(
    "docker",
    ["compose", "-f", "deploy/compose/docker-compose.yml", "--profile", "dev", "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "erp", "-v", "ON_ERROR_STOP=1", "-c", sql],
    { cwd: repoRoot, stdio: "pipe" },
  );
}

async function signIn(page: Page): Promise<void> {
  seedDirectoryUser();
  await page.goto("/");
  await page.getByLabel("Login name").fill("admin@dev.localhost");
  await page.getByLabel("Password").fill(devPassword());
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByTestId("signed-in-name")).toHaveText("Dev Admin");
}

const apiBase = "http://127.0.0.1:8080";

function expectedHome(roles: string[], personas: string[]): string {
  const labels = [...roles, ...personas].map((value) => value.trim().toLowerCase()).filter(Boolean);
  const has = (needle: string) => labels.some((value) => value.includes(needle));
  if (has("cfo") || has("partner")) return "cfo";
  if (has("finance manager") || has("finance_manager")) return "finance-manager";
  if (has("accountant")) return "accountant";
  if (has("sales") || has("collection")) return "sales";
  return "default";
}

type ApiSession = { accessToken: string; userId: string; name: string; roles: string[] };

async function publicKey(): Promise<Record<string, string>> {
  const key = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign"]);
  const jwk = await crypto.subtle.exportKey("jwk", key.publicKey);
  if (!jwk.kty || !jwk.crv || !jwk.x || !jwk.y) throw new Error("missing public key");
  return { kty: jwk.kty, crv: jwk.crv, x: jwk.x, y: jwk.y };
}

async function apiPost(path: string, body: unknown, token?: string): Promise<{ status: number; json: { data?: Record<string, unknown>; error?: { message?: string } } }> {
  const response = await fetch(`${apiBase}${path}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": crypto.randomUUID(),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(body),
  });
  const json = (await response.json()) as { data?: Record<string, unknown>; error?: { message?: string } };
  return { status: response.status, json };
}

async function apiSignIn(): Promise<ApiSession> {
  const enrolled = await apiPost("/api/v1/auth/device/enroll", {
    public_key: await publicKey(),
    platform: "console",
    app_version: "0.1.0",
    device_name: "console-e2e",
  });
  if (enrolled.status >= 300) throw new Error(`enroll failed ${enrolled.status} ${JSON.stringify(enrolled.json)}`);
  const session = await apiPost("/api/v1/auth/session", { login_name: "admin@dev.localhost" });
  const sessionId = String(session.json.data?.session_id || "");
  const checked = await apiPost(`/api/v1/auth/session/${sessionId}/check`, { password: devPassword() });
  if (checked.json.data?.verified !== true) throw new Error(`password was not verified ${JSON.stringify(checked.json)}`);
  const tokens = await apiPost("/api/v1/auth/token", {
    session_id: sessionId,
    device_id: enrolled.json.data?.device_id,
  });
  const accessToken = String(tokens.json.data?.access_token || "");
  const me = await fetch(`${apiBase}/api/v1/me`, { headers: { Authorization: `Bearer ${accessToken}` } });
  const profile = (await me.json()) as { data?: { id?: string; name?: string; roles?: string[] } };
  if (!me.ok || !profile.data?.id) throw new Error(`me failed ${me.status}`);
  return {
    accessToken,
    userId: profile.data.id,
    name: profile.data.name || "",
    roles: profile.data.roles || [],
  };
}

async function seedWaitingApproval(session: ApiSession, docNumber: string): Promise<string> {
  const actor = await apiPost("/api/v1/approvals/actors", {
    id: session.userId,
    name: session.name,
    department: "sales",
    roles: session.roles.length ? session.roles : ["Sales Agent"],
  }, session.accessToken);
  if (actor.status >= 300) throw new Error(`actor failed ${actor.status} ${JSON.stringify(actor.json)}`);
  const matrix = await apiPost("/api/v1/approvals/matrix", {
    doc_type: "sales_invoice",
    threshold_amount: "1000.00",
    currency: "AED",
    below_roles: session.roles.length ? session.roles : ["Sales Agent"],
    first_roles: ["finance manager"],
    final_role: "cfo",
    above_mode: "first_then_final",
    vote_n: 0,
    requires_step_up_above: true,
  }, session.accessToken);
  if (matrix.status >= 300) throw new Error(`matrix failed ${matrix.status} ${JSON.stringify(matrix.json)}`);
  const submitted = await apiPost("/api/v1/approvals/requests", {
    doc_id: docNumber,
    doc_type: "sales_invoice",
    doc_number: docNumber,
    party: "Al Noor",
    amount: "25.00",
    currency: "AED",
    snapshot: { doc_number: docNumber },
  }, session.accessToken);
  if (submitted.status >= 300) throw new Error(`submit failed ${submitted.status} ${JSON.stringify(submitted.json)}`);
  const requestId = String(submitted.json.data?.request_id || "");
  if (!requestId) throw new Error(`submit returned no request id ${JSON.stringify(submitted.json)}`);
  return requestId;
}

async function approvalState(request: APIRequestContext, token: string, requestId: string): Promise<string> {
  const response = await request.get(`${apiBase}/api/v1/approvals/${requestId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const body = (await response.json()) as { data?: { state?: string }; error?: { message?: string } };
  expect(response.ok(), JSON.stringify(body)).toBeTruthy();
  return body.data?.state || "";
}

async function drag(card: Locator, dx: number, dy: number): Promise<void> {
  await card.evaluate((node) => node.scrollIntoView({ block: "center", inline: "nearest" }));
  const box = await card.boundingBox();
  if (!box) throw new Error("approval card has no box");
  const startX = box.x + Math.min(24, box.width / 2);
  const startY = box.y + box.height / 2;
  await card.page().mouse.move(startX, startY);
  await card.page().mouse.down();
  await card.page().mouse.move(startX + dx, startY + dy, { steps: 12 });
  await card.page().mouse.up();
}

test.describe("key journeys against the Docker API", () => {
  test.beforeEach(async ({ request }) => {
    const health = await request.get("http://127.0.0.1:8080/health");
    expect(health.ok(), await health.text()).toBeTruthy();
  });

  test("sales order through delivery", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "Delivery note registered" })).toBeVisible();
  });

  test("collection and aging", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "Aging buckets" })).toBeVisible();
    await expect(page.getByText("On-account remainder")).toBeVisible();
  });

  test("bank match", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "Matched bank line" })).toBeVisible();
  });

  test("purchase LPO through payment", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "LPO payment released" })).toBeVisible();
  });

  test("stock count", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "Stock count posted" })).toBeVisible();
  });

  test("approval inbox", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "Approval inbox" })).toBeVisible();
  });

  test("persona home follows the roles returned by the API", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    const roles = (await page.getByTestId("profile-roles").textContent()) ?? "";
    const personas = (await page.getByTestId("profile-personas").textContent()) ?? "";
    const kind = expectedHome(roles.split(","), personas.split(","));
    await expect(page.getByTestId("home-kind")).toHaveText(kind);
    if (kind === "sales") {
      await expect(page.getByRole("heading", { name: "Aging buckets" })).toBeVisible();
      await expect(page.getByText("On-account remainder")).toBeVisible();
    }
    if (kind === "default") {
      await expect(page.getByRole("heading", { name: "Cash" })).toHaveCount(0);
      await expect(page.getByRole("heading", { name: "Queue" })).toHaveCount(0);
      await expect(page.getByRole("heading", { name: "Aging buckets" })).toHaveCount(0);
    }
  });

  test("a swipe does not change the approval state", async ({ page, request }) => {
    test.setTimeout(40_000);
    const session = await apiSignIn();
    const docNumber = `UI-${Date.now()}`;
    const requestId = await seedWaitingApproval(session, docNumber);
    const before = await approvalState(request, session.accessToken, requestId);

    await signIn(page);
    const card = page.getByTestId("approval-card").filter({ hasText: docNumber });
    await expect(card).toBeVisible();
    const posts: string[] = [];
    page.on("request", (outgoing) => {
      if (outgoing.method() === "POST" && outgoing.url().includes("/approvals/")) posts.push(outgoing.url());
    });

    await drag(card, 140, 0);
    await expect(page.getByRole("heading", { name: "Approve request" })).toBeVisible();
    expect(posts, "swipe right posted an approval").toEqual([]);
    expect(await approvalState(request, session.accessToken, requestId)).toBe(before);
    await page.getByRole("button", { name: "Close" }).click();

    await drag(card, -140, 0);
    await expect(page.getByRole("heading", { name: "Reject request" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Reject" })).toBeDisabled();
    expect(posts, "swipe left posted a rejection").toEqual([]);
    expect(await approvalState(request, session.accessToken, requestId)).toBe(before);
    await page.getByRole("button", { name: "Close" }).click();

    await drag(card, 0, 100);
    await expect(page.getByRole("heading", { name: "Flag for review" })).toBeVisible();
    expect(posts, "swipe down posted a decision").toEqual([]);
    expect(await approvalState(request, session.accessToken, requestId)).toBe(before);
  });

  test("notifications", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "Notifications" })).toBeVisible();
  });
});
