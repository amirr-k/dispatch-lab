// shared between the map and the legend, so the two can never drift apart -
// a legend that disagrees with the map is worse than no legend.
export const COLORS = {
  idle: "#7c8798",
  toPickup: "#f5a524",
  delivering: "#3b82f6",
  route: "#3b82f6",
  routeToPickup: "#f5a524",
  pickup: "#22c55e",
  destination: "#a855f7",
  closed: "#ef4444",
  road: "#39415a",
  node: "#4a5268",
};

export const driverColor: Record<string, string> = {
  idle: COLORS.idle,
  assigned: COLORS.toPickup,
  en_route_to_pickup: COLORS.toPickup,
  delivering: COLORS.delivering,
  unavailable: "#374151",
};
