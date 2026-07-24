import type { CityEdge, CityNode, Driver, EventEnvelope } from "./types";

export interface ReplayOrder {
  id: string;
  pickup: string;
  destination: string;
  status: string;
  assignedDriver?: string;
}

// Frame is what the map renders at one point in a run. It mirrors the state
// the backend reconstructs server-side, so a frame folded here and a frame
// fetched from /replay?at= describe the same world.
export interface Frame {
  sequence: number;
  virtualTime: number;
  nodes: CityNode[];
  edges: CityEdge[];
  drivers: Record<string, Driver>;
  orders: Record<string, ReplayOrder>;
  paused: boolean;
  speed: number;
}

export function emptyFrame(): Frame {
  return {
    sequence: 0,
    virtualTime: 0,
    nodes: [],
    edges: [],
    drivers: {},
    orders: {},
    paused: false,
    speed: 1,
  };
}

interface SnapshotPayload {
  nodes?: CityNode[];
  edges?: CityEdge[];
  drivers?: (Driver & { assignedOrder?: string })[];
  orders?: ReplayOrder[];
  paused?: boolean;
  speed?: number;
}

// applyEvent folds one event into a frame, returning a new frame. It mirrors
// internal/replay's fold in Go; the two are checked against each other by the
// browser test, which compares a locally folded final frame with the state
// the server reconstructs for the same sequence.
export function applyEvent(frame: Frame, event: EventEnvelope): Frame {
  const next: Frame = {
    ...frame,
    sequence: event.sequence,
    virtualTime: event.virtualTime,
    drivers: { ...frame.drivers },
    orders: { ...frame.orders },
  };
  // the union of every field the event payloads carry, mirroring the narrow
  // structs internal/replay decodes into on the server.
  const payload = event.payload as {
    orderId: string;
    driverId: string;
    nodeId: string;
    pickupNodeId: string;
    destinationNodeId: string;
    status: string;
    edgeIds?: string[];
    paused?: boolean;
    multiplier?: number;
  };

  switch (event.type) {
    case "simulation.snapshot": {
      const snap = event.payload as SnapshotPayload;
      next.nodes = snap.nodes ?? [];
      next.edges = snap.edges ?? [];
      next.paused = snap.paused ?? false;
      next.speed = snap.speed ?? 1;
      next.drivers = {};
      for (const d of snap.drivers ?? []) next.drivers[d.id] = { ...d };
      next.orders = {};
      for (const o of snap.orders ?? []) next.orders[o.id] = { ...o };
      break;
    }

    case "order.placed":
      next.orders[payload.orderId] = {
        id: payload.orderId,
        pickup: payload.pickupNodeId,
        destination: payload.destinationNodeId,
        status: "pending",
      };
      break;

    case "order.assigned":
      next.orders[payload.orderId] = {
        ...next.orders[payload.orderId],
        status: "assigned",
        assignedDriver: payload.driverId,
      };
      break;

    case "order.unassignable":
      if (next.orders[payload.orderId]) {
        next.orders[payload.orderId] = { ...next.orders[payload.orderId], status: "unassignable" };
      }
      break;

    case "order.delivered":
      if (next.orders[payload.orderId]) {
        next.orders[payload.orderId] = { ...next.orders[payload.orderId], status: "delivered" };
      }
      break;

    case "driver.position.updated":
      next.drivers[payload.driverId] = {
        ...next.drivers[payload.driverId],
        id: payload.driverId,
        position: payload.nodeId,
      };
      break;

    case "driver.status.changed": {
      const driver = { ...next.drivers[payload.driverId], id: payload.driverId, status: payload.status };
      next.drivers[payload.driverId] = driver;
      // a driver switching to delivering is what puts its order en route;
      // the pickup has no event of its own.
      if (payload.status === "delivering") {
        const order = Object.values(next.orders).find(
          (o) => o.assignedDriver === payload.driverId && o.status === "assigned",
        );
        if (order) next.orders[order.id] = { ...order, status: "en_route" };
      }
      break;
    }

    case "road.closed":
    case "road.reopened": {
      const closed = event.type === "road.closed";
      const ids = new Set(payload.edgeIds ?? []);
      next.edges = next.edges.map((edge) => (ids.has(edge.id) ? { ...edge, closed } : edge));
      break;
    }

    case "simulation.paused":
      next.paused = Boolean(payload.paused);
      break;

    case "simulation.speed.changed":
      next.speed = Number(payload.multiplier);
      break;
  }

  return next;
}

// foldTo replays events[0..count) onto a starting frame. Passing a cached
// frame and the index it was built at lets a forward scrub continue from
// there instead of refolding the whole log on every step.
export function foldTo(events: EventEnvelope[], count: number, from?: { frame: Frame; index: number }): Frame {
  let frame = emptyFrame();
  let start = 0;
  if (from && from.index <= count) {
    frame = from.frame;
    start = from.index;
  }
  for (let i = start; i < count && i < events.length; i++) {
    frame = applyEvent(frame, events[i]);
  }
  return frame;
}
