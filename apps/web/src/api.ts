import type { EventEnvelope } from "./types";

const API_URL = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_URL}${path}`, {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
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

export function streamURL(id: string): string {
  return `${API_URL.replace(/^http/, "ws")}/api/v1/simulations/${id}/stream`;
}
