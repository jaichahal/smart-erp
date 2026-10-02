import { expect, test } from "@playwright/test";
import { signIn } from "./support";

test("collection receipt submits against the live API", async ({ page, request }) => {
  const health = await request.get("http://127.0.0.1:8080/health");
  expect(health.ok(), await health.text()).toBeTruthy();
  await signIn(page);
  await page.getByRole("button", { name: "Collection receipt" }).click();
  await page.getByLabel("Amount").fill("25.00");
  await page.getByRole("button", { name: "Record receipt" }).click();
  const error = page.getByTestId("collection-receipt-error");
  const number = page.getByTestId("collection-receipt-number");
  await expect(number.or(error)).toBeVisible({ timeout: 20_000 });
  if (await error.isVisible()) {
    throw new Error(`collection receipt: ${await error.innerText()}`);
  }
});
