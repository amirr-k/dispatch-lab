import type { CityEdge, CityNode, Driver } from "./types";

interface CityMapProps {
  nodes: CityNode[];
  edges: CityEdge[];
  drivers: Record<string, Driver>;
  pickup?: string | null;
  onNodeClick?: (nodeId: string) => void;
}

const driverColor: Record<string, string> = {
  idle: "#6b7280",
  assigned: "#f5a524",
  en_route_to_pickup: "#f5a524",
  delivering: "#3b82f6",
  unavailable: "#374151",
};

export function CityMap({ nodes, edges, drivers, pickup, onNodeClick }: CityMapProps) {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const padding = 20;
  const maxX = Math.max(0, ...nodes.map((n) => n.x)) + padding;
  const maxY = Math.max(0, ...nodes.map((n) => n.y)) + padding;

  return (
    <svg
      viewBox={`-${padding} -${padding} ${maxX} ${maxY}`}
      style={{ width: "100%", height: "100%", background: "#0b0e14" }}
    >
      {edges.map((edge) => {
        const from = byId.get(edge.from);
        const to = byId.get(edge.to);
        if (!from || !to) return null;
        return (
          <line
            key={edge.id}
            x1={from.x}
            y1={from.y}
            x2={to.x}
            y2={to.y}
            stroke={edge.closed ? "#e5484d" : "#2a3040"}
            strokeWidth={3}
          />
        );
      })}
      {nodes.map((node) => (
        <circle
          key={node.id}
          cx={node.x}
          cy={node.y}
          r={onNodeClick ? 6 : 2}
          fill={node.id === pickup ? "#22c55e" : "#242a38"}
          style={onNodeClick ? { cursor: "pointer" } : undefined}
          onClick={onNodeClick ? () => onNodeClick(node.id) : undefined}
        >
          {onNodeClick && <title>{node.id}</title>}
        </circle>
      ))}
      {Object.values(drivers).map((driver) => {
        const pos = byId.get(driver.position);
        if (!pos) return null;
        return (
          <circle
            key={driver.id}
            cx={pos.x}
            cy={pos.y}
            r={6}
            fill={driverColor[driver.status] ?? "#6b7280"}
            stroke="#0b0e14"
            strokeWidth={1.5}
            // markers are display-only; without this a driver parked on a
            // node steals the click meant for the node underneath it
            style={{ pointerEvents: "none" }}
          >
            <title>{`${driver.id} — ${driver.status}`}</title>
          </circle>
        );
      })}
    </svg>
  );
}
