import { COLORS, driverColor } from "./colors";
import type { CityEdge, CityNode, Driver, Order } from "./types";

interface CityMapProps {
  nodes: CityNode[];
  edges: CityEdge[];
  drivers: Record<string, Driver>;
  orders?: Record<string, Order>;
  pickup?: string | null;
  onNodeClick?: (nodeId: string) => void;
  onEdgeClick?: (edgeId: string) => void;
}

export function CityMap({ nodes, edges, drivers, orders, pickup, onNodeClick, onEdgeClick }: CityMapProps) {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const interactive = Boolean(onNodeClick);

  // the viewBox must span min..max on each axis, not 0..max - using the max
  // alone as the width while also offsetting the origin by -padding clipped
  // the right and bottom edges of the graph.
  const xs = nodes.map((n) => n.x);
  const ys = nodes.map((n) => n.y);
  const pad = 24;
  const minX = xs.length ? Math.min(...xs) - pad : 0;
  const minY = ys.length ? Math.min(...ys) - pad : 0;
  const width = xs.length ? Math.max(...xs) - Math.min(...xs) + pad * 2 : 100;
  const height = ys.length ? Math.max(...ys) - Math.min(...ys) + pad * 2 : 100;

  // a driver's remaining path, from where it is now to where it's headed.
  function remainingRoute(driver: Driver): CityNode[] {
    if (!driver.route || driver.route.length === 0) return [];
    const from = driver.routeIndex ?? 0;
    return driver.route
      .slice(from)
      .map((id) => byId.get(id))
      .filter((n): n is CityNode => Boolean(n));
  }

  return (
    <svg
      viewBox={`${minX} ${minY} ${width} ${height}`}
      role="img"
      aria-label="City map showing drivers, orders, and roads"
      style={{ width: "100%", height: "100%", display: "block" }}
    >
      {/* roads */}
      {edges.map((edge) => {
        const from = byId.get(edge.from);
        const to = byId.get(edge.to);
        if (!from || !to) return null;
        return (
          <g key={edge.id}>
            <line
              x1={from.x}
              y1={from.y}
              x2={to.x}
              y2={to.y}
              stroke={edge.closed ? COLORS.closed : COLORS.road}
              strokeWidth={edge.closed ? 5 : 3}
              strokeLinecap="round"
              strokeDasharray={edge.closed ? "1 9" : undefined}
            />
            {onEdgeClick && (
              // invisible wide stroke: a generous click target without
              // making the visible road look thick
              <line
                id={`edge-${edge.id}`}
                x1={from.x}
                y1={from.y}
                x2={to.x}
                y2={to.y}
                stroke="transparent"
                strokeWidth={14}
                style={{ cursor: edge.closed ? "not-allowed" : "pointer" }}
                onClick={() => onEdgeClick(edge.id)}
              >
                <title>{edge.closed ? "Road closed" : "Click to close this road"}</title>
              </line>
            )}
          </g>
        );
      })}

      {/* active routes, drawn under the markers so markers stay readable */}
      {Object.values(drivers).map((driver) => {
        const path = remainingRoute(driver);
        if (path.length < 2) return null;
        const heading = driver.status === "delivering" ? COLORS.route : COLORS.routeToPickup;
        return (
          <polyline
            key={`route-${driver.id}`}
            points={path.map((n) => `${n.x},${n.y}`).join(" ")}
            fill="none"
            stroke={heading}
            strokeWidth={4}
            strokeLinecap="round"
            strokeLinejoin="round"
            opacity={0.55}
            // a route is decoration drawn on top of the roads it follows -
            // without this it swallows clicks meant for the road beneath,
            // making exactly the roads worth closing the unclickable ones.
            style={{ pointerEvents: "none" }}
          />
        );
      })}

      {/* intersections */}
      {nodes.map((node) => (
        <circle
          key={node.id}
          id={`node-${node.id}`}
          cx={node.x}
          cy={node.y}
          r={interactive ? 7 : 3}
          fill={node.id === pickup ? COLORS.pickup : COLORS.node}
          stroke={node.id === pickup ? COLORS.pickup : "transparent"}
          strokeWidth={node.id === pickup ? 6 : 0}
          strokeOpacity={0.3}
          style={interactive ? { cursor: "pointer" } : undefined}
          onClick={onNodeClick ? () => onNodeClick(node.id) : undefined}
        >
          {interactive && <title>{node.id === pickup ? `${node.id} — pickup selected` : node.id}</title>}
        </circle>
      ))}

      {/* order pickup and destination markers */}
      {Object.values(orders ?? {}).map((order) => {
        const p = byId.get(order.pickup);
        const d = byId.get(order.destination);
        return (
          <g key={`order-${order.id}`} style={{ pointerEvents: "none" }}>
            {p && (
              <>
                <circle cx={p.x} cy={p.y} r={11} fill="none" stroke={COLORS.pickup} strokeWidth={2.5} />
                <text
                  x={p.x}
                  y={p.y + 4}
                  textAnchor="middle"
                  fontSize={11}
                  fontWeight={700}
                  fill={COLORS.pickup}
                >
                  P
                </text>
              </>
            )}
            {d && (
              <>
                <rect
                  x={d.x - 10}
                  y={d.y - 10}
                  width={20}
                  height={20}
                  rx={4}
                  fill="none"
                  stroke={COLORS.destination}
                  strokeWidth={2.5}
                />
                <text
                  x={d.x}
                  y={d.y + 4}
                  textAnchor="middle"
                  fontSize={11}
                  fontWeight={700}
                  fill={COLORS.destination}
                >
                  D
                </text>
              </>
            )}
          </g>
        );
      })}

      {/* drivers, drawn last so they sit on top */}
      {Object.values(drivers).map((driver) => {
        const pos = byId.get(driver.position);
        if (!pos) return null;
        const color = driverColor[driver.status] ?? COLORS.idle;
        const busy = driver.status !== "idle";
        return (
          <g
            key={driver.id}
            // markers are display-only; without this a driver parked on a
            // node steals the click meant for the node underneath it
            style={{ pointerEvents: "none" }}
          >
            {busy && <circle cx={pos.x} cy={pos.y} r={13} fill={color} opacity={0.18} />}
            <circle cx={pos.x} cy={pos.y} r={busy ? 8 : 5.5} fill={color} stroke="#0b0e14" strokeWidth={2}>
              <title>{`${driver.id} — ${driver.status.replace(/_/g, " ")}`}</title>
            </circle>
          </g>
        );
      })}
    </svg>
  );
}
