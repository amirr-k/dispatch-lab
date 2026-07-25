import { expect, test } from "@playwright/test";

test("runs an algorithm comparison and reports both strategies' measured metrics", async ({ page }) => {
  await page.goto("/compare");
  await expect(page.getByRole("heading", { name: "Compare Algorithms" })).toBeVisible();

  await page.getByPlaceholder("e.g. 42").fill("42");
  await page.getByRole("button", { name: "Run Comparison" }).click();

  // a real batch-optimized run against 12 drivers needs more than the
  // default action timeout to finish and round-trip.
  await expect(page.getByText("Completed deliveries")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Unassigned orders")).toBeVisible();
  await expect(page.getByText("Assignment compute time (ms)")).toBeVisible();

  // the same seed and driver count is required to reproduce byte-identical
  // metrics (Phase 4's exit gate) - the scenario summary line is the on-page
  // proof of exactly which seed produced this table.
  await expect(page.getByText(/Scenario: seed 42/)).toBeVisible();

  await expect(page.getByRole("button", { name: "Download JSON" })).toBeVisible();
});

test("reruns with a different seed and gets a different scenario summary", async ({ page }) => {
  await page.goto("/compare");

  await page.getByPlaceholder("e.g. 42").fill("7");
  await page.getByRole("button", { name: "Run Comparison" }).click();

  await expect(page.getByText(/Scenario: seed 7,/)).toBeVisible({ timeout: 15_000 });
});
