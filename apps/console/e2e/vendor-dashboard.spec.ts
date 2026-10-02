import { expect, test } from "@playwright/test";
import { expectLive, signIn } from "./support";

test("vendor dashboard filters a SKU and shows counts and best price", async ({ page, request }) => {
  const health = await request.get("http://127.0.0.1:8080/health");
  expect(health.ok(), await health.text()).toBeTruthy();
  await signIn(page);
  await page.getByRole("button", { name: "Vendor dashboard" }).click();
  await page.getByLabel("Raw-material SKU").fill("RM-TEST");
  await page.getByRole("button", { name: "Apply SKU filter" }).click();
  await expectLive(page, "vendor-dashboard-status", "vendor-dashboard-error", "vendor dashboard");
  await expect(page.getByTestId("invoice-counts")).toBeVisible();
  await expect(page.getByTestId("best-price")).toBeVisible();
  await expect(page.getByTestId("blocked-group")).toBeVisible();
});

test("sales agent does not see approve or blacklist", async ({ page, request }) => {
  const health = await request.get("http://127.0.0.1:8080/health");
  expect(health.ok(), await health.text()).toBeTruthy();
  await signIn(page);
  await page.getByRole("button", { name: "Vendor dashboard" }).click();
  await expect(page.getByRole("button", { name: "Approve" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Blacklist" })).toHaveCount(0);
});

test("a swipe opens the review sheet and does not approve", async ({ page, request }) => {
  const health = await request.get("http://127.0.0.1:8080/health");
  expect(health.ok(), await health.text()).toBeTruthy();
  const posts: string[] = [];
  page.on("request", (req) => {
    if (req.method() === "POST" && /\/approve|\/blacklist|\/reject/.test(req.url())) {
      posts.push(req.url());
    }
  });
  await signIn(page);
  await page.getByRole("button", { name: "Vendor dashboard" }).click();
  await page.getByLabel("Raw-material SKU").fill("RM-TEST");
  await page.getByRole("button", { name: "Apply SKU filter" }).click();
  await expectLive(page, "vendor-dashboard-status", "vendor-dashboard-error", "vendor dashboard swipe");
  const row = page.getByTestId("vendor-row").first();
  await expect(row).toBeVisible();
  const box = await row.boundingBox();
  if (!box) {
    throw new Error("vendor row has no box to swipe");
  }
  await page.mouse.move(box.x + box.width - 8, box.y + Math.min(box.height / 2, 20));
  await page.mouse.down();
  await page.mouse.move(box.x + 8, box.y + Math.min(box.height / 2, 20));
  await page.mouse.up();
  await expect(page.getByTestId("approval-sheet")).toBeVisible();
  expect(posts).toEqual([]);
  await expect(page.getByRole("button", { name: "Approve" })).toHaveCount(0);
});
