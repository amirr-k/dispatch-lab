export interface EventEnvelope {
  schemaVersion: number;
  simulationId: string;
  sequence: number;
  virtualTime: number;
  type: string;
  payload: Record<string, unknown>;
}

export interface CityNode {
  id: string;
  x: number;
  y: number;
}

export interface CityEdge {
  id: string;
  from: string;
  to: string;
  closed: boolean;
}

export interface Driver {
  id: string;
  position: string;
  status: string;
  // route and routeIndex come from the snapshot and from route.computed;
  // together they say which part of the path is still ahead of the driver,
  // which is what the map draws.
  route?: string[];
  routeIndex?: number;
  assignedOrder?: string;
}

export interface Order {
  id: string;
  pickup: string;
  destination: string;
  status: string;
  assignedDriver?: string;
}

export interface Assignment {
  orderId: string;
  driverId: string;
  pickupEtaVirtualTime: number;
  pickupDistance: number;
}
