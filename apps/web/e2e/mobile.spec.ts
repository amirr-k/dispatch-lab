import { expect, test } from "@playwright/test";

// an iPhone-sized viewport with touch emulation, kept on chromium rather
// than pulling in devices["iPhone 13"] (which forces webkit) - a second
// browser engine is a heavy dependency for one layout smoke test, and
// chromium's touch/viewport emulation is enough to catch a fixed-width
// element breaking mobile layout, which is the one thing this guards.
test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

test("the live demo renders and is usable at a mobile viewport with no horizontal overflow", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("DispatchLab").first()).toBeVisible();
  await expect(page.getByText("● Connected")).toBeVisible({ timeout: 10_000 });

  // the map is an SVG with viewBox scaling, not a fixed pixel size - the one
  // real regression this guards against is a fixed-width element (a table,
  // a wide flex row that refuses to wrap) forcing the page to scroll
  // sideways on a phone.
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(1);

  // placing an order has to work with touch taps, not just mouse clicks.
  await page.locator("#node-n-0-0").tap();
  await expect(page.getByText(/Pickup selected: n-0-0/)).toBeVisible();
  await page.locator("#node-n-1-1").tap();
  await expect(page.getByText(/^Driver .+ assigned$/)).toBeVisible({ timeout: 10_000 });
});

test("the compare page is usable at a mobile viewport", async ({ page }) => {
  await page.goto("/compare");
  await expect(page.getByRole("heading", { name: "Compare Algorithms" })).toBeVisible();

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});
