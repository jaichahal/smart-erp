import { expect, test } from "@playwright/test";

test("phone step asks for one number and the code hits the session API", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByTestId("onboarding-progress")).toHaveText("1 of 3");
  await expect(page.getByLabel("Phone number")).toBeVisible();
  await expect(page.getByLabel("Login name")).toHaveCount(0);
  await page.getByLabel("Phone number").fill("501234567");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByTestId("onboarding-progress")).toHaveText("2 of 3");
  await page.getByLabel("Code").fill("000000");
  await page.getByRole("button", { name: "Verify code" }).click();
  await expect(page.getByTestId("otp-error")).toContainText("HTTP");
  await expect(page.getByTestId("signed-in-name")).toHaveCount(0);
});
