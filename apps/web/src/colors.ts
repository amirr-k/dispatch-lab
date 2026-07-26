// shared between the map and the legend, so the two can never drift apart -
// a legend that disagrees with the map is worse than no legend.
export const COLORS = {
  idle: "#6b7280",
  toPickup: "#f59e0b",
  delivering: "#3b82f6",
  route: "#3b82f6",
  routeToPickup: "#f59e0b",
  pickup: "#22c55e",
  destination: "#a855f7",
  closed: "#ef4444",
  road: "#475569",
  node: "#64748b",
  // markers sit on top of roads and routes, so they need an opaque backing
  // for their glyph to stay legible rather than blending into what's under it.
  markerFill: "#0f172a",
};

export const driverColor: Record<string, string> = {
  idle: COLORS.idle,
  assigned: COLORS.toPickup,
  en_route_to_pickup: COLORS.toPickup,
  delivering: COLORS.delivering,
  unavailable: "#374151",
};
