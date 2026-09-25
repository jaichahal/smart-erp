import { expect, test } from "@playwright/test";
import { signIn } from "./support";

test("persona home follows GET /api/v1/me", async ({ page, request }) => {
  const health = await request.get("http://127.0.0.1:8080/health");
  expect(health.ok(), await health.text()).toBeTruthy();
  await signIn(page);
  await expect(page.getByTestId("persona-home")).toHaveText("Sales Agent");
  for (const tab of ["My Day", "Stock", "Orders", "Activity", "Profile"]) {
    await expect(page.getByRole("button", { name: tab, exact: true })).toBeVisible();
  }
  const home = page.getByRole("button", { name: "Vendor dashboard" });
  const box = await home.boundingBox();
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
});
