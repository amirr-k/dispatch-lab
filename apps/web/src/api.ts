import type { EventEnvelope } from "./types";

const API_URL = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

const TOKEN_KEY = "dispatchlab.guestToken";

export interface GuestSession {
  token: string;
  expiresAt: string;
  simulationQuota: number;
}

// the token is kept in sessionStorage so a page refresh rejoins the same
// session and its simulations, while a new tab starts clean.
function storedToken(): string | null {
  return sessionStorage.getItem(TOKEN_KEY);
}

// pending deduplicates concurrent session requests: several API calls firing
// at once on first load must not each mint a token.
let pending: Promise<string> | null = null;

export function guestToken(): string | null {
  return storedToken();
}

export async function ensureSession(): Promise<string> {
  const existing = storedToken();
  if (existing) return existing;
  if (pending) return pending;

  pending = (async () => {
    const res = await fetch(`${API_URL}/api/v1/guest-sessions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
    });
    if (!res.ok) {
      throw new ApiError(res.status, "could not start a guest session");
    }
    const session = (await res.json()) as GuestSession;
    sessionStorage.setItem(TOKEN_KEY, session.token);
    return session.token;
  })();

  try {
    return await pending;
  } finally {
    pending = null;
  }
}

function clearSession() {
  sessionStorage.removeItem(TOKEN_KEY);
}

async function send(path: string, token: string, init?: RequestInit): Promise<Response> {
  return fetch(`${API_URL}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
      ...init?.headers,
    },
  });
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let token = await ensureSession();
  let res = await send(path, token, init);

  // an expired session is the one error worth retrying automatically: the
  // visitor did nothing wrong and a fresh token makes the request valid.
  if (res.status === 401) {
    clearSession();
    token = await ensureSession();
    res = await send(path, token, init);
  }

  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new ApiError(res.status, body?.error?.message ?? `request failed with status ${res.status}`);
  }
  if (res.status === 204 || res.status === 202) return undefined as T;
  return (await res.json()) as T;
}

export interface CreateSimulationResponse {
  id: string;
  seed: number;
  drivers: number;
}

export function createSimulation(drivers?: number): Promise<CreateSimulationResponse> {
  return request("/api/v1/simulations", {
    method: "POST",
    body: JSON.stringify(drivers ? { drivers } : {}),
  });
}

export function getSimulation(id: string): Promise<EventEnvelope> {
  return request(`/api/v1/simulations/${id}`);
}

export function placeOrder(id: string, pickup: string, destination: string): Promise<void> {
  return request(`/api/v1/simulations/${id}/orders`, {
    method: "POST",
    body: JSON.stringify({ pickup, destination }),
  });
}

export function closeRoad(id: string, edgeId: string): Promise<void> {
  return request(`/api/v1/simulations/${id}/closures`, {
    method: "POST",
    body: JSON.stringify({ edgeId }),
  });
}

export function reopenRoad(id: string, edgeId: string): Promise<void> {
  return request(`/api/v1/simulations/${id}/closures?edgeId=${encodeURIComponent(edgeId)}`, {
    method: "DELETE",
  });
}

export function pauseSimulation(id: string): Promise<void> {
  return request(`/api/v1/simulations/${id}/pause`, { method: "POST" });
}

export function resumeSimulation(id: string): Promise<void> {
  return request(`/api/v1/simulations/${id}/resume`, { method: "POST" });
}

export function resetSimulation(id: string): Promise<void> {
  return request(`/api/v1/simulations/${id}/reset`, { method: "POST" });
}

export function setSpeed(id: string, multiplier: number): Promise<void> {
  return request(`/api/v1/simulations/${id}/speed`, {
    method: "POST",
    body: JSON.stringify({ multiplier }),
  });
}

// the browser WebSocket API cannot set an Authorization header, so the stream
// takes the token as a query parameter instead.
export function streamURL(id: string): string {
  const base = `${API_URL.replace(/^http/, "ws")}/api/v1/simulations/${id}/stream`;
  const token = storedToken();
  return token ? `${base}?token=${encodeURIComponent(token)}` : base;
}

export interface Metrics {
  algorithm: string;
  completedDeliveries: number;
  unassignedOrders: number;
  servedFraction: number;
  averagePickupTime: number;
  p95PickupTime: number;
  totalDistance: number;
  batchDispatches: number;
  immediateDispatches: number;
}

export type DemandLevel = "light" | "steady" | "rush";

export interface ComparisonResult {
  id: string;
  scenario: {
    seed: number;
    drivers: number;
    demand: DemandLevel;
    arrivals: { virtualTime: number }[];
    batchWindow: number;
    minBatchSize: number;
    maxWaitVirtualTime: number;
    maxVirtualTime: number;
  };
  baseline: Metrics;
  optimized: Metrics;
}

export function createComparison(
  seed?: number,
  drivers?: number,
  demand?: DemandLevel,
): Promise<ComparisonResult> {
  const body: Record<string, number | string> = {};
  if (seed !== undefined) body.seed = seed;
  if (drivers !== undefined) body.drivers = drivers;
  if (demand !== undefined) body.demand = demand;
  return request("/api/v1/comparisons", { method: "POST", body: JSON.stringify(body) });
}

export function getComparison(id: string): Promise<ComparisonResult> {
  return request(`/api/v1/comparisons/${id}`);
}

export interface ShowcaseResponse {
  id: string;
  showcase: boolean;
  replayUrl: string;
}

// markShowcase retains a run permanently and returns the stable URL its
// replay lives at.
export function markShowcase(id: string): Promise<ShowcaseResponse> {
  return request(`/api/v1/simulations/${id}/showcase`, { method: "POST" });
}

export interface StoredSimulation {
  id: string;
  seed: number;
  drivers: number;
  strategy: string;
  createdAt: string;
  completedAt?: string;
  showcase: boolean;
}

export interface ReplayLog {
  simulation: StoredSimulation;
  events: EventEnvelope[];
  fromSequence: number;
  latestSequence: number;
  hasMore: boolean;
}

export function getReplay(id: string, fromSequence = 0, limit?: number): Promise<ReplayLog> {
  const params = new URLSearchParams({ fromSequence: String(fromSequence) });
  if (limit) params.set("limit", String(limit));
  return request(`/api/v1/simulations/${id}/replay?${params}`);
}
