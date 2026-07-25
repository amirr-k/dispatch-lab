import { describe, expect, it } from "vitest";
import { applyEvent, emptyFrame, foldTo } from "./replayState";
import type { EventEnvelope } from "./types";

// event is a small builder so each test only spells out the fields it cares
// about, mirroring how sparse a real payload from a given event type is.
function event(sequence: number, type: string, payload: Record<string, unknown>, virtualTime = sequence): EventEnvelope {
  return { schemaVersion: 1, simulationId: "sim-1", sequence, virtualTime, type, payload };
}

describe("applyEvent", () => {
  it("hydrates nodes, edges, drivers, and orders from a snapshot", () => {
    const snapshot = event(1, "simulation.snapshot", {
      nodes: [{ id: "n-0-0", x: 0, y: 0 }],
      edges: [{ id: "e-1", from: "n-0-0", to: "n-0-1", closed: false }],
      drivers: [{ id: "d-1", position: "n-0-0", status: "idle" }],
      orders: [{ id: "o-1", pickup: "n-0-0", destination: "n-0-1", status: "pending" }],
      paused: true,
      speed: 2,
    });

    const frame = applyEvent(emptyFrame(), snapshot);

    expect(frame.nodes).toHaveLength(1);
    expect(frame.edges).toHaveLength(1);
    expect(frame.drivers["d-1"]).toEqual({ id: "d-1", position: "n-0-0", status: "idle" });
    expect(frame.orders["o-1"].status).toBe("pending");
    expect(frame.paused).toBe(true);
    expect(frame.speed).toBe(2);
    expect(frame.sequence).toBe(1);
  });

  it("defaults an empty snapshot's optional fields rather than carrying over the previous frame's", () => {
    const withData = applyEvent(
      emptyFrame(),
      event(1, "simulation.snapshot", {
        nodes: [{ id: "n-0-0", x: 0, y: 0 }],
        drivers: [{ id: "d-1", position: "n-0-0", status: "idle" }],
      }),
    );

    // a later snapshot with no drivers/orders means none exist now, not that
    // the field was omitted - the fold must clear, not merge.
    const cleared = applyEvent(withData, event(2, "simulation.snapshot", { nodes: [] }));

    expect(cleared.drivers).toEqual({});
    expect(cleared.orders).toEqual({});
    expect(cleared.paused).toBe(false);
    expect(cleared.speed).toBe(1);
  });

  it("adds a pending order on order.placed", () => {
    const frame = applyEvent(
      emptyFrame(),
      event(1, "order.placed", { orderId: "o-1", pickupNodeId: "n-0-0", destinationNodeId: "n-1-1" }),
    );
    expect(frame.orders["o-1"]).toEqual({
      id: "o-1",
      pickup: "n-0-0",
      destination: "n-1-1",
      status: "pending",
    });
  });

  it("moves an order to assigned and records the driver on order.assigned", () => {
    let frame = applyEvent(
      emptyFrame(),
      event(1, "order.placed", { orderId: "o-1", pickupNodeId: "n-0-0", destinationNodeId: "n-1-1" }),
    );
    frame = applyEvent(frame, event(2, "order.assigned", { orderId: "o-1", driverId: "d-1" }));

    expect(frame.orders["o-1"].status).toBe("assigned");
    expect(frame.orders["o-1"].assignedDriver).toBe("d-1");
  });

  it("does nothing to an order.assigned event for an order that was never placed", () => {
    // the real event stream never does this, but the fold should not throw
    // just because a scrub started mid-log with a partial order table.
    const frame = applyEvent(emptyFrame(), event(1, "order.assigned", { orderId: "ghost", driverId: "d-1" }));
    expect(frame.orders.ghost).toEqual({ status: "assigned", assignedDriver: "d-1" });
  });

  it("marks an order unassignable only if it is known, leaving an unknown order absent", () => {
    const known = applyEvent(
      applyEvent(emptyFrame(), event(1, "order.placed", { orderId: "o-1", pickupNodeId: "a", destinationNodeId: "b" })),
      event(2, "order.unassignable", { orderId: "o-1" }),
    );
    expect(known.orders["o-1"].status).toBe("unassignable");

    const unknown = applyEvent(emptyFrame(), event(1, "order.unassignable", { orderId: "ghost" }));
    expect(unknown.orders.ghost).toBeUndefined();
  });

  it("marks an order delivered only if it is known", () => {
    const known = applyEvent(
      applyEvent(emptyFrame(), event(1, "order.placed", { orderId: "o-1", pickupNodeId: "a", destinationNodeId: "b" })),
      event(2, "order.delivered", { orderId: "o-1" }),
    );
    expect(known.orders["o-1"].status).toBe("delivered");

    const unknown = applyEvent(emptyFrame(), event(1, "order.delivered", { orderId: "ghost" }));
    expect(unknown.orders.ghost).toBeUndefined();
  });

  it("moves a driver's position on driver.position.updated", () => {
    const frame = applyEvent(emptyFrame(), event(1, "driver.position.updated", { driverId: "d-1", nodeId: "n-2-2" }));
    expect(frame.drivers["d-1"]).toEqual({ id: "d-1", position: "n-2-2" });
  });

  it("moving an order's assigned driver to delivering flips the order to en_route", () => {
    let frame = applyEvent(
      emptyFrame(),
      event(1, "order.placed", { orderId: "o-1", pickupNodeId: "a", destinationNodeId: "b" }),
    );
    frame = applyEvent(frame, event(2, "order.assigned", { orderId: "o-1", driverId: "d-1" }));
    frame = applyEvent(frame, event(3, "driver.status.changed", { driverId: "d-1", status: "delivering" }));

    expect(frame.orders["o-1"].status).toBe("en_route");
  });

  it("a driver going idle does not touch any order (the pickup has no event of its own)", () => {
    let frame = applyEvent(
      emptyFrame(),
      event(1, "order.placed", { orderId: "o-1", pickupNodeId: "a", destinationNodeId: "b" }),
    );
    frame = applyEvent(frame, event(2, "order.assigned", { orderId: "o-1", driverId: "d-1" }));
    frame = applyEvent(frame, event(3, "driver.status.changed", { driverId: "d-1", status: "idle" }));

    expect(frame.orders["o-1"].status).toBe("assigned");
  });

  it("closes only the edges named in road.closed, leaving others untouched", () => {
    let frame = applyEvent(
      emptyFrame(),
      event(1, "simulation.snapshot", {
        edges: [
          { id: "e-1", from: "a", to: "b", closed: false },
          { id: "e-2", from: "c", to: "d", closed: false },
        ],
      }),
    );
    frame = applyEvent(frame, event(2, "road.closed", { edgeIds: ["e-1"] }));

    expect(frame.edges.find((e) => e.id === "e-1")?.closed).toBe(true);
    expect(frame.edges.find((e) => e.id === "e-2")?.closed).toBe(false);
  });

  it("road.reopened is road.closed's exact inverse on the same edge set", () => {
    let frame = applyEvent(
      emptyFrame(),
      event(1, "simulation.snapshot", { edges: [{ id: "e-1", from: "a", to: "b", closed: false }] }),
    );
    frame = applyEvent(frame, event(2, "road.closed", { edgeIds: ["e-1"] }));
    frame = applyEvent(frame, event(3, "road.reopened", { edgeIds: ["e-1"] }));

    expect(frame.edges.find((e) => e.id === "e-1")?.closed).toBe(false);
  });

  it("tracks paused and speed independently of each other", () => {
    let frame = applyEvent(emptyFrame(), event(1, "simulation.paused", { paused: true }));
    expect(frame.paused).toBe(true);
    expect(frame.speed).toBe(1);

    frame = applyEvent(frame, event(2, "simulation.speed.changed", { multiplier: 4 }));
    expect(frame.paused).toBe(true);
    expect(frame.speed).toBe(4);
  });

  it("carries sequence and virtualTime from every event, including ones it otherwise ignores", () => {
    const frame = applyEvent(emptyFrame(), event(7, "some.unhandled.type", {}, 12.5));
    expect(frame.sequence).toBe(7);
    expect(frame.virtualTime).toBe(12.5);
  });

  it("never mutates the frame it was given", () => {
    const before = applyEvent(
      emptyFrame(),
      event(1, "order.placed", { orderId: "o-1", pickupNodeId: "a", destinationNodeId: "b" }),
    );
    const beforeSnapshot = JSON.parse(JSON.stringify(before));

    applyEvent(before, event(2, "order.delivered", { orderId: "o-1" }));

    expect(before).toEqual(beforeSnapshot);
  });
});

describe("foldTo", () => {
  const events: EventEnvelope[] = [
    event(1, "order.placed", { orderId: "o-1", pickupNodeId: "a", destinationNodeId: "b" }),
    event(2, "order.assigned", { orderId: "o-1", driverId: "d-1" }),
    event(3, "order.delivered", { orderId: "o-1" }),
  ];

  it("replays from zero up to count", () => {
    const frame = foldTo(events, 2);
    expect(frame.orders["o-1"].status).toBe("assigned");
    expect(frame.sequence).toBe(2);
  });

  it("folding to 0 returns the empty frame untouched", () => {
    expect(foldTo(events, 0)).toEqual(emptyFrame());
  });

  it("folding past the end of the log stops at the last event, not out of bounds", () => {
    const frame = foldTo(events, 100);
    expect(frame.orders["o-1"].status).toBe("delivered");
    expect(frame.sequence).toBe(3);
  });

  it("continuing forward from a cached frame gives the same result as folding from zero", () => {
    const cached = foldTo(events, 2);
    const continued = foldTo(events, 3, { frame: cached, index: 2 });
    const fromScratch = foldTo(events, 3);

    expect(continued).toEqual(fromScratch);
  });

  it("ignores a cache whose index is already past the requested count", () => {
    // a scrub that jumps backward cannot resume from a forward cache; it
    // must refold from zero instead of skipping events it needs.
    const cached = foldTo(events, 3);
    const rewound = foldTo(events, 1, { frame: cached, index: 3 });

    expect(rewound).toEqual(foldTo(events, 1));
  });
});
