import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Page, test } from "@playwright/test";

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

  test("notifications", async ({ page }) => {
    test.setTimeout(20_000);
    await signIn(page);
    await expect(page.getByRole("heading", { name: "Notifications" })).toBeVisible();
  });
});
