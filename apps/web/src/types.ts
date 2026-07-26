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
  // x and y are the interpolated screen coordinates; position is the node
  // id the driver has most recently reached. route and routeIndex say which
  // part of the path is still ahead, and routeProgress says how far along the
  // current edge the driver has travelled, so the marker can sit between
  // intersections instead of jumping from node to node.
  x?: number;
  y?: number;
  route?: string[];
  routeIndex?: number;
  routeProgress?: number;
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
