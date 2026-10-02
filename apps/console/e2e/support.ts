import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Page } from "@playwright/test";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");

export function devPassword(): string {
  const file = readFileSync(path.join(repoRoot, "deploy/compose/.env.dev.example"), "utf8");
  const line = file.split("\n").find((row) => row.startsWith("ZITADEL_ADMIN_PASSWORD="));
  if (!line) {
    throw new Error("ZITADEL_ADMIN_PASSWORD is missing from deploy/compose/.env.dev.example");
  }
  return line.slice("ZITADEL_ADMIN_PASSWORD=".length).trim();
}

export function seedDirectoryUser(): void {
  const sql = `INSERT INTO erp.identity_users (id, login_name, display_name, company_id, roles, personas, disabled, step_up_methods)
VALUES ('dev-admin', 'admin@dev.localhost', 'Dev Admin', '00000000-0000-4000-8000-000000000001', ARRAY['Sales Agent'], ARRAY['staff'], false, ARRAY['totp'])
ON CONFLICT (login_name) DO UPDATE SET display_name = EXCLUDED.display_name, roles = EXCLUDED.roles, personas = EXCLUDED.personas, disabled = false;`;
  execFileSync(
    "docker",
    [
      "compose",
      "-f",
      "deploy/compose/docker-compose.yml",
      "--profile",
      "dev",
      "exec",
      "-T",
      "postgres",
      "psql",
      "-U",
      "postgres",
      "-d",
      "erp",
      "-v",
      "ON_ERROR_STOP=1",
      "-c",
      sql,
    ],
    { cwd: repoRoot, stdio: "pipe" },
  );
}

export async function signIn(page: Page): Promise<void> {
  seedDirectoryUser();
  await page.goto("/");
  await page.getByRole("button", { name: "Use work email" }).click();
  await page.getByLabel("Login name").fill("admin@dev.localhost");
  await page.getByLabel("Password").fill(devPassword());
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByTestId("signed-in-name")).toHaveText("Dev Admin", { timeout: 20_000 });
}

export async function expectLive(page: Page, statusId: string, errorId: string, label: string): Promise<void> {
  const error = page.getByTestId(errorId);
  const status = page.getByTestId(statusId);
  await expect(status.or(error)).toBeVisible({ timeout: 20_000 });
  if (await error.isVisible()) {
    throw new Error(`${label}: ${await error.innerText()}`);
  }
}
