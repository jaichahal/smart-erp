import { expect, test } from "@playwright/test";
import { expectLive, signIn } from "./support";

test("purchase list loads from the live API", async ({ page, request }) => {
  const health = await request.get("http://127.0.0.1:8080/health");
  expect(health.ok(), await health.text()).toBeTruthy();
  await signIn(page);
  await page.getByRole("button", { name: "Purchase list" }).click();
  await expectLive(page, "purchase-list-status", "purchase-list-error", "purchase list");
  await expect(page.getByTestId("purchase-list")).toBeVisible();
});
