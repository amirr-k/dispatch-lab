import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "./api";
import type { Assignment, CityEdge, CityNode, Driver, EventEnvelope } from "./types";

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

export interface SimulationState {
  simulationId: string | null;
  connected: boolean;
  nodes: CityNode[];
  edges: CityEdge[];
  drivers: Record<string, Driver>;
  assignment: Assignment | null;
  feed: string[];
  metrics: Metrics;
  paused: boolean;
  speed: number;
  actionError: string | null;
  placeOrder: (pickup: string, destination: string) => Promise<void>;
  togglePaused: () => Promise<void>;
  reset: () => Promise<void>;
  changeSpeed: (multiplier: number) => Promise<void>;
}

function describe(event: EventEnvelope): string {
  const p = event.payload;
  switch (event.type) {
    case "order.placed":
      return `Order ${p.orderId} placed`;
    case "order.assigned":
      return `Driver ${p.driverId} assigned to ${p.orderId}`;
    case "order.unassignable":
      return `Order ${p.orderId} could not be assigned: ${p.reason}`;
    case "order.delivered":
      return `Order ${p.orderId} delivered`;
    case "road.closed":
      return `Road ${p.edgeId} closed`;
    case "road.reopened":
      return `Road ${p.edgeId} reopened`;
    case "simulation.paused":
      return p.paused ? "Simulation paused" : "Simulation resumed";
    case "simulation.speed.changed":
      return `Speed set to ${p.multiplier}x`;
    default:
      return event.type;
  }
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
  const [assignment, setAssignment] = useState<Assignment | null>(null);
  const [feed, setFeed] = useState<string[]>([]);
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
          setPaused(p.paused as boolean);
          setSpeedState(p.speed as number);
          return; // hydration only: not a feed-worthy event
        }
        case "driver.position.updated": {
          const driverId = p.driverId as string;
          const nodeId = p.nodeId as string;
          setDrivers((prev) => ({
            ...prev,
            [driverId]: { ...prev[driverId], id: driverId, position: nodeId },
          }));
          break;
        }
        case "driver.status.changed": {
          const driverId = p.driverId as string;
          const status = p.status as string;
          setDrivers((prev) => ({
            ...prev,
            [driverId]: { ...prev[driverId], id: driverId, status },
          }));
          break;
        }
        case "order.placed":
          setMetrics((prev) => ({ ...prev, pending: prev.pending + 1 }));
          break;
        case "order.assigned":
          setAssignment(p as unknown as Assignment);
          break;
        case "order.unassignable":
          setMetrics((prev) => ({ ...prev, pending: prev.pending - 1, unassignable: prev.unassignable + 1 }));
          break;
        case "order.delivered":
          setMetrics((prev) => ({ ...prev, pending: prev.pending - 1, delivered: prev.delivered + 1 }));
          break;
        case "simulation.paused":
          setPaused(p.paused as boolean);
          break;
        case "simulation.speed.changed":
          setSpeedState(p.multiplier as number);
          break;
      }

      setFeed((prev) => [describe(event), ...prev].slice(0, MAX_FEED_LENGTH));
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
      }),
    [runAction],
  );
  const changeSpeed = useCallback(
    (multiplier: number) => runAction((id) => api.setSpeed(id, multiplier)),
    [runAction],
  );

  return {
    simulationId,
    connected,
    nodes,
    edges,
    drivers,
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
  };
}
