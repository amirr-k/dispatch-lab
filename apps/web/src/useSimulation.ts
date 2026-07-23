import { useEffect, useRef, useState } from "react";
import type { Assignment, CityEdge, CityNode, Driver, EventEnvelope } from "./types";

const WS_URL = import.meta.env.VITE_WS_URL ?? "ws://localhost:8080/ws";
const MAX_FEED_LENGTH = 30;

export interface SimulationState {
  connected: boolean;
  nodes: CityNode[];
  edges: CityEdge[];
  drivers: Record<string, Driver>;
  assignment: Assignment | null;
  feed: string[];
}

function describe(event: EventEnvelope): string {
  const p = event.Payload;
  switch (event.Type) {
    case "order.placed":
      return `Order ${p.orderId} placed`;
    case "order.assigned":
      return `Driver ${p.driverId} assigned to ${p.orderId}`;
    case "order.delivered":
      return `Order ${p.orderId} delivered`;
    case "road.closed":
      return `Road ${p.edgeId} closed`;
    default:
      return event.Type;
  }
}

export function useSimulation(): SimulationState {
  const [connected, setConnected] = useState(false);
  const [nodes, setNodes] = useState<CityNode[]>([]);
  const [edges, setEdges] = useState<CityEdge[]>([]);
  const [drivers, setDrivers] = useState<Record<string, Driver>>({});
  const [assignment, setAssignment] = useState<Assignment | null>(null);
  const [feed, setFeed] = useState<string[]>([]);
  const socketRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    const socket = new WebSocket(WS_URL);
    socketRef.current = socket;

    socket.onopen = () => setConnected(true);
    socket.onclose = () => setConnected(false);

    socket.onmessage = (raw) => {
      const event = JSON.parse(raw.data) as EventEnvelope;
      const p = event.Payload;

      switch (event.Type) {
        case "simulation.snapshot": {
          setNodes(p.nodes as CityNode[]);
          setEdges(p.edges as CityEdge[]);
          const byId: Record<string, Driver> = {};
          for (const d of p.drivers as Driver[]) byId[d.id] = d;
          setDrivers(byId);
          return;
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
        case "order.assigned": {
          setAssignment(p as unknown as Assignment);
          break;
        }
      }

      setFeed((prev) => [describe(event), ...prev].slice(0, MAX_FEED_LENGTH));
    };

    return () => socket.close();
  }, []);

  return { connected, nodes, edges, drivers, assignment, feed };
}
