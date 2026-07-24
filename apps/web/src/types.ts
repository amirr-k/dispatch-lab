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
}

export interface Assignment {
  orderId: string;
  driverId: string;
  pickupEtaVirtualTime: number;
  assignmentComputeMs: number;
}
