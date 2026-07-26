import { expect, test } from "@playwright/test";

test("runs an algorithm comparison and reports both strategies' measured metrics", async ({ page }) => {
  await page.goto("/compare");
  await expect(page.getByRole("heading", { name: "Compare Algorithms" })).toBeVisible();

  await page.getByPlaceholder("e.g. 42").fill("42");
  await page.getByRole("button", { name: "Run Comparison" }).click();

  // a real batch-optimized run against 12 drivers needs more than the
  // default action timeout to finish and round-trip.
  await expect(page.getByRole("rowheader", { name: /Completed deliveries/ })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("rowheader", { name: /Unassigned orders/ })).toBeVisible();
  await expect(page.getByRole("rowheader", { name: /Assignment compute time/ })).toBeVisible();

  // the same seed, driver count and demand level are required to reproduce
  // byte-identical metrics (Phase 4's exit gate) - the scenario summary line
  // is the on-page proof of exactly which inputs produced this table.
  await expect(page.getByText(/Scenario: seed 42, 12 drivers, steady demand/)).toBeVisible();

  await expect(page.getByRole("button", { name: "Download JSON" })).toBeVisible();
});

test("reruns with a different seed and gets a different scenario summary", async ({ page }) => {
  await page.goto("/compare");

  await page.getByPlaceholder("e.g. 42").fill("7");
  await page.getByRole("button", { name: "Run Comparison" }).click();

  await expect(page.getByText(/Scenario: seed 7,/)).toBeVisible({ timeout: 15_000 });
});

// The demand control is the one input that decides whether batch optimization
// can beat greedy nearest-driver at all, so it has to actually reach the
// backend rather than only change the button that looks selected.
test("demand level changes the workload both strategies are run against", async ({ page }) => {
  await page.goto("/compare");
  await page.getByPlaceholder("e.g. 42").fill("42");

  await page.getByRole("button", { name: /^Light/ }).click();
  await page.getByRole("button", { name: "Run Comparison" }).click();
  await expect(page.getByText(/light demand \(12 orders\)/)).toBeVisible({ timeout: 15_000 });

  const lightPickup = await page.getByRole("row", { name: /Average pickup time/ }).innerText();

  await page.getByRole("button", { name: /^Rush/ }).click();
  await page.getByRole("button", { name: "Run Comparison" }).click();
  await expect(page.getByText(/rush demand \(40 orders\)/)).toBeVisible({ timeout: 30_000 });

  const rushPickup = await page.getByRole("row", { name: /Average pickup time/ }).innerText();
  expect(rushPickup).not.toBe(lightPickup);
});

// Whether a delta helps or hurts depends on the metric, so the table states
// the direction in words per row instead of colouring the number.
test("reports the difference as a signed delta column, not a colour", async ({ page }) => {
  await page.goto("/compare");
  await page.getByPlaceholder("e.g. 42").fill("42");
  await page.getByRole("button", { name: "Run Comparison" }).click();

  await expect(page.getByRole("columnheader", { name: /Delta/ })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("rowheader", { name: /Average pickup time.*lower is better/s })).toBeVisible();
  await expect(page.getByRole("rowheader", { name: /Completed deliveries.*higher is better/s })).toBeVisible();

  const delta = page.getByRole("row", { name: /Average pickup time/ }).getByRole("cell").nth(2);
  await expect(delta).toHaveText(/^(no change|[+−][\d.]+ \([+−][\d.]+%\))$/);
});
