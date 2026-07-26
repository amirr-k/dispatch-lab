import { expect, test } from "@playwright/test";

// this suite drives the full visitor journey against a real backend and a
// real browser - no mocked fetch, no stubbed websocket. Every simulation
// starts from the same DefaultGridConfig 6x6 grid regardless of seed
// (internal/city.GenerateGrid fixes rows/cols; only jitter varies with the
// seed), so node ids n-0-0..n-5-5 exist on every run and are safe to
// hardcode here.

test("landing, place an order, observe assignment, close a road, observe reroute, save and open a replay", async ({
  page,
}) => {
  await test.step("load the landing page", async () => {
    await page.goto("/");
    await expect(page).toHaveTitle("DispatchLab");
    await expect(page.getByText("DispatchLab").first()).toBeVisible();
    // the websocket has to actually connect - this is the one line the app
    // renders only once the stream is live.
    await expect(page.getByText("● Connected")).toBeVisible({ timeout: 10_000 });
  });

  // captured from the real order request rather than sessionStorage: in dev
  // (not production - vite build does not do this) React 18 StrictMode
  // deliberately double-invokes the mount effect, which can race two
  // simulation-creation calls and leave sessionStorage pointing at the one
  // the page abandoned rather than the one it is actually driving. Reading
  // the id and token off the wire ties this test to whichever simulation the
  // browser actually placed the order against, sidestepping that race.
  const orderRequest = page.waitForRequest(
    (req) => req.method() === "POST" && /\/api\/v1\/simulations\/[^/]+\/orders$/.test(req.url()),
  );

  await test.step("place an order across the whole grid", async () => {
    // driver-0 is always placed at the lexicographically-first sorted node
    // (internal/simulation.placeDrivers), which for a 6x6 grid is n-0-0 -
    // so pickup there gives a zero-distance, deterministically-won
    // assignment. The far corner destination means several seconds of
    // travel, which is the window the next two steps act inside.
    await page.locator("#node-n-0-0").click();
    await expect(page.getByText(/Pickup set at n-0-0/)).toBeVisible();
    await page.locator("#node-n-5-5").click();
  });

  await test.step("observe the assignment", async () => {
    // the assignment card names the driver and its pickup figures; the
    // activity feed reports the same assignment as a sentence. Both are
    // checked because they are populated by different code paths.
    await expect(page.getByText("Latest assignment")).toBeVisible();
    await expect(page.getByText(/assigned to the order/)).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(/Order placed/)).toBeVisible();
    await expect(page.getByText("Distance to pickup")).toBeVisible();
  });

  const targetEdgeId = await test.step("find the edge the assigned driver is about to cross", async () => {
    // rather than guess which edge is under the driver from timing alone,
    // ask the same endpoint the app itself polls: the snapshot's driver
    // payload carries the live route and routeIndex (added in Phase 5 so a
    // snapshot alone can resume a replay). Reading the *next* hop straight
    // from there is what makes the closure below hit unconditionally,
    // whatever tick the driver happens to be on by now.
    const request = await orderRequest;
    const simulationId = request.url().match(/\/simulations\/([^/]+)\/orders$/)?.[1];
    const token = (await request.headerValue("authorization"))?.replace(/^Bearer /, "");
    expect(simulationId).toBeTruthy();
    expect(token).toBeTruthy();

    const res = await page.request.get(`http://localhost:8080/api/v1/simulations/${simulationId}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(res.ok()).toBe(true);
    const snapshot = await res.json();
    type SnapshotDriver = { id: string; route: string[] | null; routeIndex: number; assignedOrder: string };
    const drivers: SnapshotDriver[] = snapshot.payload.drivers;
    const driver = drivers.find((d) => d.assignedOrder === "order-1");
    expect(driver, `no driver was carrying order-1; drivers were: ${JSON.stringify(drivers)}`).toBeTruthy();
    expect(driver!.route, `assigned driver had no route: ${JSON.stringify(driver)}`).not.toBeNull();
    expect(driver!.route!.length).toBeGreaterThan(driver!.routeIndex + 1);

    const from = driver!.route![driver!.routeIndex];
    const to = driver!.route![driver!.routeIndex + 1];
    return `e-${from}-${to}`;
  });

  await test.step("close the road the driver is actively using", async () => {
    // each edge is generated in both directions, so the target edge's
    // invisible wide hit-line perfectly overlaps its reverse sibling's -
    // whichever direction paints on top intercepts the click. Both send an
    // equivalent close (the backend shuts both directions together), so
    // this is a real click landing on real overlapping geometry, not a
    // broken locator.
    await page.locator(`#edge-${targetEdgeId}`).click({ force: true });
    await expect(page.locator(`#edge-${targetEdgeId} title`)).toHaveText("Road closed");
  });

  await test.step("observe the reroute", async () => {
    // the closed edge was on the driver's active route, so the count in
    // this line has to be non-zero: it is the backend reporting that it
    // actually invalidated and recomputed a route, not merely that a
    // closure was recorded.
    await expect(page.getByText(/Road closed.*[1-9]\d* routes? recalculated/)).toBeVisible({ timeout: 5_000 });
  });

  const replayHref = await test.step("save the replay", async () => {
    await page.getByRole("button", { name: "Save replay" }).click();
    const link = page.getByRole("link", { name: "Open saved replay" });
    await expect(link).toBeVisible({ timeout: 10_000 });
    return link.getAttribute("href");
  });

  await test.step("open the saved replay at its stable url", async () => {
    expect(replayHref).toMatch(/^\/replay\/.+/);
    await page.goto(replayHref!);

    await expect(page.getByText(/Replay ·/)).toBeVisible();
    const scrubber = page.getByLabel("Replay position");
    await expect(scrubber).toBeVisible();

    const total = Number(await scrubber.getAttribute("max"));
    expect(total).toBeGreaterThan(0);

    // scrub back to the start and confirm the map folds back to zero state,
    // then forward again - the property replay's fold has to hold in both
    // directions since a viewer can drag either way.
    await scrubber.fill("0");
    await expect(page.getByText(/^Event 0 of/)).toBeVisible();

    await scrubber.fill(String(total));
    await expect(page.getByText(new RegExp(`^Event ${total} of`))).toBeVisible();
    await expect(page.getByText(/Delivered/)).toBeVisible();
  });
});
