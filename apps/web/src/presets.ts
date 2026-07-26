import { Package, TrafficCone, Zap } from "lucide-react";
import * as api from "./api";
import type { CityNode } from "./types";

// Guided presets: one click each, so a visitor who does not want to work out
// what to click still sees the three things worth seeing. Each one only
// sends the same commands a person clicking the map would send - no preset
// reaches past the API into simulation state.

export type PresetId = "one-order" | "rush-hour" | "road-closure";

export interface Preset {
  id: PresetId;
  label: string;
  blurb: string;
  run: (simulationId: string, nodes: CityNode[]) => Promise<void>;
}

// farApart picks two intersections with real distance between them, so the
// resulting delivery takes long enough to actually watch.
function farApart(nodes: CityNode[], rng: () => number): [string, string] {
  const a = nodes[Math.floor(rng() * nodes.length)];
  let best = nodes[0];
  let bestDist = -1;
  // sample rather than scan every node: a handful of candidates is plenty to
  // find something usefully far away, and keeps this O(1)-ish on big graphs.
  for (let i = 0; i < 12; i++) {
    const candidate = nodes[Math.floor(rng() * nodes.length)];
    const d = (candidate.x - a.x) ** 2 + (candidate.y - a.y) ** 2;
    if (d > bestDist) {
      bestDist = d;
      best = candidate;
    }
  }
  return [a.id, best.id];
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function placeOne(simulationId: string, nodes: CityNode[]): Promise<void> {
  const [pickup, destination] = farApart(nodes, Math.random);
  await api.placeOrder(simulationId, pickup, destination);
}

async function rushHour(simulationId: string, nodes: CityNode[]): Promise<void> {
  // staggered rather than all at once: orders arriving over time is what
  // makes drivers visibly compete for work, which is the point of the
  // scenario. Sequential awaits also stay well inside the rate limit.
  for (let i = 0; i < 6; i++) {
    const [pickup, destination] = farApart(nodes, Math.random);
    await api.placeOrder(simulationId, pickup, destination);
    await sleep(350);
  }
}

// roadClosure places an order, waits for the backend to actually assign and
// route a driver, then closes the very next road that driver intends to use.
// It reads the driver's live route from the snapshot rather than guessing,
// so the closure always lands on a road in use and the reroute is real.
async function roadClosure(simulationId: string, nodes: CityNode[]): Promise<void> {
  const [pickup, destination] = farApart(nodes, Math.random);
  await api.placeOrder(simulationId, pickup, destination);

  for (let attempt = 0; attempt < 20; attempt++) {
    await sleep(250);
    const snapshot = await api.getSimulation(simulationId);
    const drivers = (snapshot.payload as { drivers?: SnapshotDriver[] }).drivers ?? [];
    const busy = drivers.find((d) => d.route && d.route.length > (d.routeIndex ?? 0) + 1);
    if (!busy || !busy.route) continue;

    const from = busy.route[busy.routeIndex ?? 0];
    const to = busy.route[(busy.routeIndex ?? 0) + 1];
    await api.closeRoad(simulationId, `e-${from}-${to}`);
    return;
  }
}

interface SnapshotDriver {
  id: string;
  route?: string[];
  routeIndex?: number;
}

export const PRESET_ICONS: Record<PresetId, typeof Package> = {
  "one-order": Package,
  "rush-hour": Zap,
  "road-closure": TrafficCone,
};

export const PRESETS: Preset[] = [
  {
    id: "one-order",
    label: "Place one order",
    blurb: "Watch a single delivery from assignment to drop-off.",
    run: placeOne,
  },
  {
    id: "rush-hour",
    label: "Rush hour",
    blurb: "Six orders in quick succession, competing for the same drivers.",
    run: rushHour,
  },
  {
    id: "road-closure",
    label: "Road closure",
    blurb: "Place an order, then shut the road its driver is using.",
    run: roadClosure,
  },
];
