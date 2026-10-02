import { expect, test } from "@playwright/test";
import { signIn } from "./support";

test("sales order submits against the live API", async ({ page, request }) => {
  const health = await request.get("http://127.0.0.1:8080/health");
  expect(health.ok(), await health.text()).toBeTruthy();
  await signIn(page);
  await page.getByRole("button", { name: "Sales order" }).click();
  await page.getByLabel("Customer").fill("00000000-0000-4000-8000-000000000002");
  await page.getByLabel("SKU").fill("FG-1");
  await page.getByLabel("Quantity").fill("1");
  await page.getByLabel("Unit price").fill("10.00");
  await page.getByRole("button", { name: "Submit order" }).click();
  const error = page.getByTestId("sales-order-error");
  const number = page.getByTestId("sales-order-number");
  await expect(number.or(error)).toBeVisible({ timeout: 20_000 });
  if (await error.isVisible()) {
    throw new Error(`sales order: ${await error.innerText()}`);
  }
});
