import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

// wcag2a/2aa are the two levels axe's own docs recommend as a baseline; best-
// practice rules are excluded since they're opinionated style preferences
// rather than accessibility failures.
const TAGS = ["wcag2a", "wcag2aa"];

test("the landing page has no serious or critical accessibility violations", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("● Connected")).toBeVisible({ timeout: 10_000 });

  const results = await new AxeBuilder({ page }).withTags(TAGS).analyze();
  const serious = results.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(serious, JSON.stringify(serious, null, 2)).toEqual([]);
});

test("the compare page has no serious or critical accessibility violations", async ({ page }) => {
  await page.goto("/compare");
  await expect(page.getByRole("heading", { name: "Compare Algorithms" })).toBeVisible();

  const results = await new AxeBuilder({ page }).withTags(TAGS).analyze();
  const serious = results.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(serious, JSON.stringify(serious, null, 2)).toEqual([]);
});

test("a replay page has no serious or critical accessibility violations", async ({ page }) => {
  // placing an order first gives the page something to save and reopen,
  // rather than depending on a specific showcase id existing.
  await page.goto("/");
  await page.locator("#node-n-0-0").click();
  await page.locator("#node-n-1-1").click();
  await expect(page.getByText(/assigned to the order/)).toBeVisible({ timeout: 10_000 });

  await page.getByRole("button", { name: "Save replay" }).click();
  const link = page.getByRole("link", { name: "Open saved replay" });
  await expect(link).toBeVisible({ timeout: 10_000 });
  await page.goto((await link.getAttribute("href"))!);
  await expect(page.getByText(/Replay ·/)).toBeVisible();

  const results = await new AxeBuilder({ page }).withTags(TAGS).analyze();
  const serious = results.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(serious, JSON.stringify(serious, null, 2)).toEqual([]);
});
