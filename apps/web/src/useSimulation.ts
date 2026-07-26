import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "./api";
import type { Assignment, CityEdge, CityNode, Driver, EventEnvelope, Order } from "./types";

const MAX_FEED_LENGTH = 30;
const SESSION_KEY = "dispatchlab.simulationId";
// caps reconnect backoff so a long backend outage doesn't leave the tab
// waiting minutes to notice the server is back.
const MAX_RECONNECT_DELAY_MS = 5000;

export interface Metrics {
  pending: number;
  delivered: number;
  unassignable: number;
  virtualTime: number;
}

export interface FeedEntry {
  id: number;
  text: string;
  kind: "order" | "assign" | "deliver" | "closure" | "system";
}

export interface SimulationState {
  simulationId: string | null;
  connected: boolean;
  nodes: CityNode[];
  edges: CityEdge[];
  drivers: Record<string, Driver>;
  orders: Record<string, Order>;
  assignment: Assignment | null;
  feed: FeedEntry[];
  metrics: Metrics;
  paused: boolean;
  speed: number;
  actionError: string | null;
  placeOrder: (pickup: string, destination: string) => Promise<void>;
  togglePaused: () => Promise<void>;
  reset: () => Promise<void>;
  changeSpeed: (multiplier: number) => Promise<void>;
  closeRoad: (edgeId: string) => Promise<void>;
}

// describe turns an event into a sentence a non-technical visitor can read.
// Events with no plain-language form return null and never reach the feed -
// a raw "driver.status.changed" in a visitor-facing activity list is noise,
// not information.
function describe(event: EventEnvelope): FeedEntry | null {
  const p = event.payload;
  const entry = (text: string, kind: FeedEntry["kind"]): FeedEntry => ({ id: event.sequence, text, kind });

  switch (event.type) {
    case "order.placed":
      return entry(`Order placed — pickup at ${p.pickupNodeId}`, "order");
    case "order.assigned":
      return entry(`${driverLabel(p.driverId as string)} assigned to the order`, "assign");
    case "order.unassignable":
      return entry(`No driver could reach that order (${p.reason})`, "system");
    case "order.delivered":
      return entry(`Delivered by ${driverLabel(p.driverId as string)}`, "deliver");
    case "road.closed": {
      const routes = p.affectedRoutes as number;
      const ms = (p.recalculationMs as number).toFixed(1);
      return routes > 0
        ? entry(`Road closed — ${routes} route${routes === 1 ? "" : "s"} recalculated in ${ms}ms`, "closure")
        : entry(`Road closed — no active routes were using it`, "closure");
    }
    case "road.reopened":
      return entry(`Road reopened`, "closure");
    case "simulation.paused":
      return entry(p.paused ? "Paused" : "Resumed", "system");
    case "simulation.speed.changed":
      return entry(`Speed set to ${p.multiplier}x`, "system");
    default:
      // route.computed, route.invalidated, driver.position.updated and
      // driver.status.changed are all visible on the map itself; repeating
      // them as text adds nothing a visitor can act on.
      return null;
  }
}

// driverLabel turns "driver-7" into "Driver 7" - the id is an implementation
// detail, the number is what a person reads off the map.
export function driverLabel(id: string): string {
  const n = id.replace(/^driver-/, "");
  return `Driver ${n}`;
}

// resolveSession finds a simulation to join: reuse a previously created one
// from this browser tab's session if it still exists on the server, so a
// page refresh doesn't spawn a fresh city and lose progress. Otherwise it
// creates a new one.
async function resolveSession(): Promise<string> {
  const existing = sessionStorage.getItem(SESSION_KEY);
  if (existing) {
    try {
      await api.getSimulation(existing);
      return existing;
    } catch {
      // fallen out of the server's tracked set (restarted, expired) - fall
      // through and create a new one.
    }
  }
  const created = await api.createSimulation();
  sessionStorage.setItem(SESSION_KEY, created.id);
  return created.id;
}

export function useSimulation(): SimulationState {
  const [simulationId, setSimulationId] = useState<string | null>(null);
  const [connected, setConnected] = useState(false);
  const [nodes, setNodes] = useState<CityNode[]>([]);
  const [edges, setEdges] = useState<CityEdge[]>([]);
  const [drivers, setDrivers] = useState<Record<string, Driver>>({});
  const [orders, setOrders] = useState<Record<string, Order>>({});
  const [assignment, setAssignment] = useState<Assignment | null>(null);
  const [feed, setFeed] = useState<FeedEntry[]>([]);
  const [metrics, setMetrics] = useState<Metrics>({ pending: 0, delivered: 0, unassignable: 0, virtualTime: 0 });
  const [paused, setPaused] = useState(false);
  const [speed, setSpeedState] = useState(1);
  const [actionError, setActionError] = useState<string | null>(null);

  const idRef = useRef<string | null>(null);
  const socketRef = useRef<WebSocket | null>(null);
  const closedByUsRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    let reconnectDelay = 500;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

    function applyEvent(event: EventEnvelope) {
      const p = event.payload;
      setMetrics((prev) => ({ ...prev, virtualTime: event.virtualTime }));

      switch (event.type) {
        case "simulation.snapshot": {
          setNodes(p.nodes as CityNode[]);
          setEdges(p.edges as CityEdge[]);
          const byId: Record<string, Driver> = {};
          for (const d of p.drivers as Driver[]) byId[d.id] = d;
          setDrivers(byId);
          const orderById: Record<string, Order> = {};
          for (const o of (p.orders as Order[]) ?? []) orderById[o.id] = o;
          setOrders(orderById);
          setPaused(p.paused as boolean);
          setSpeedState(p.speed as number);
          return; // hydration only: not a feed-worthy event
        }
        case "driver.position.updated": {
          const driverId = p.driverId as string;
          const nodeId = p.nodeId as string;
          const x = p.x as number | undefined;
          const y = p.y as number | undefined;
          setDrivers((prev) => {
            const existing = prev[driverId];
            return {
              ...prev,
              [driverId]: {
                ...existing,
                id: driverId,
                position: nodeId,
                x: x ?? existing?.x,
                y: y ?? existing?.y,
                routeIndex: (p.routeIndex as number | undefined) ?? existing?.routeIndex,
                routeProgress: (p.routeProgress as number | undefined) ?? existing?.routeProgress,
              },
            };
          });
          break;
        }
        case "route.computed": {
          const driverId = p.driverId as string;
          setDrivers((prev) => ({
            ...prev,
            [driverId]: { ...prev[driverId], id: driverId, route: p.nodeIds as string[], routeIndex: 0 },
          }));
          break;
        }
        case "route.invalidated": {
          const driverId = p.driverId as string;
          setDrivers((prev) => ({ ...prev, [driverId]: { ...prev[driverId], id: driverId, route: undefined } }));
          break;
        }
        case "driver.status.changed": {
          const driverId = p.driverId as string;
          const status = p.status as string;
          setDrivers((prev) => ({
            ...prev,
            [driverId]: {
              ...prev[driverId],
              id: driverId,
              status,
              // an idle driver has nothing left to draw
              route: status === "idle" ? undefined : prev[driverId]?.route,
            },
          }));
          break;
        }
        case "order.placed":
          setOrders((prev) => ({
            ...prev,
            [p.orderId as string]: {
              id: p.orderId as string,
              pickup: p.pickupNodeId as string,
              destination: p.destinationNodeId as string,
              status: "pending",
            },
          }));
          setMetrics((prev) => ({ ...prev, pending: prev.pending + 1 }));
          break;
        case "order.assigned":
          setAssignment(p as unknown as Assignment);
          setOrders((prev) => ({
            ...prev,
            [p.orderId as string]: {
              ...prev[p.orderId as string],
              status: "assigned",
              assignedDriver: p.driverId as string,
            },
          }));
          break;
        case "order.unassignable":
          setOrders((prev) => ({
            ...prev,
            [p.orderId as string]: { ...prev[p.orderId as string], status: "unassignable" },
          }));
          setMetrics((prev) => ({ ...prev, pending: prev.pending - 1, unassignable: prev.unassignable + 1 }));
          break;
        case "order.delivered":
          setOrders((prev) => {
            const next = { ...prev };
            delete next[p.orderId as string];
            return next;
          });
          setMetrics((prev) => ({ ...prev, pending: prev.pending - 1, delivered: prev.delivered + 1 }));
          break;
        case "simulation.paused":
          setPaused(p.paused as boolean);
          break;
        case "simulation.speed.changed":
          setSpeedState(p.multiplier as number);
          break;
        case "road.closed":
        case "road.reopened": {
          const ids = new Set(p.edgeIds as string[]);
          const closed = event.type === "road.closed";
          setEdges((prev) => prev.map((e) => (ids.has(e.id) ? { ...e, closed } : e)));
          break;
        }
      }

      const described = describe(event);
      if (described) setFeed((prev) => [described, ...prev].slice(0, MAX_FEED_LENGTH));
    }

    function connect(id: string) {
      const socket = new WebSocket(api.streamURL(id));
      socketRef.current = socket;

      socket.onopen = () => {
        setConnected(true);
        reconnectDelay = 500; // reset backoff after a successful connect
      };
      socket.onmessage = (raw) => applyEvent(JSON.parse(raw.data) as EventEnvelope);
      socket.onclose = () => {
        setConnected(false);
        if (cancelled || closedByUsRef.current) return;
        reconnectTimer = setTimeout(() => connect(id), reconnectDelay);
        reconnectDelay = Math.min(reconnectDelay * 2, MAX_RECONNECT_DELAY_MS);
      };
    }

    resolveSession().then((id) => {
      if (cancelled) return;
      idRef.current = id;
      setSimulationId(id);
      connect(id);
    });

    return () => {
      cancelled = true;
      closedByUsRef.current = true;
      clearTimeout(reconnectTimer);
      socketRef.current?.close();
    };
  }, []);

  const runAction = useCallback(async (fn: (id: string) => Promise<void>) => {
    const id = idRef.current;
    if (!id) return;
    setActionError(null);
    try {
      await fn(id);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "action failed");
    }
  }, []);

  const placeOrder = useCallback(
    (pickup: string, destination: string) => runAction((id) => api.placeOrder(id, pickup, destination)),
    [runAction],
  );
  const togglePaused = useCallback(
    () => runAction((id) => (paused ? api.resumeSimulation(id) : api.pauseSimulation(id))),
    [runAction, paused],
  );
  // order-derived counts (pending/delivered/unassignable) are inferred client
  // side from the event stream, not carried in the backend snapshot, so a
  // plain reconnect must NOT zero them - only an explicit reset should.
  const reset = useCallback(
    () =>
      runAction(async (id) => {
        await api.resetSimulation(id);
        setMetrics((prev) => ({ ...prev, pending: 0, delivered: 0, unassignable: 0 }));
        setAssignment(null);
        setOrders({});
        setFeed([]);
      }),
    [runAction],
  );
  const changeSpeed = useCallback(
    (multiplier: number) => runAction((id) => api.setSpeed(id, multiplier)),
    [runAction],
  );
  // reopening isn't supported by the backend yet, so closing an already
  // closed road is skipped client side rather than sent as a no-op request.
  const closeRoad = useCallback(
    (edgeId: string) => {
      if (edges.find((e) => e.id === edgeId)?.closed) return Promise.resolve();
      return runAction((id) => api.closeRoad(id, edgeId));
    },
    [runAction, edges],
  );

  return {
    simulationId,
    connected,
    nodes,
    edges,
    drivers,
    orders,
    assignment,
    feed,
    metrics,
    paused,
    speed,
    actionError,
    placeOrder,
    togglePaused,
    reset,
    changeSpeed,
    closeRoad,
  };
}
