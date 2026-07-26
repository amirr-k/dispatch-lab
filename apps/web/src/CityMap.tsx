import { COLORS, driverColor } from "./colors";
import { ICONS, type MapIcon } from "./icons";
import type { CityEdge, CityNode, Driver, Order } from "./types";

// Glyph renders a lucide icon at a point in this SVG's own coordinate space.
// lucide draws into a 24-unit box, so a nested <svg> is what lets it be placed
// and scaled in city coordinates alongside the roads.
function Glyph({ icon: Icon, x, y, size, color }: {
  icon: MapIcon;
  x: number;
  y: number;
  size: number;
  color: string;
}) {
  return (
    <Icon
      x={x - size / 2}
      y={y - size / 2}
      width={size}
      height={size}
      color={color}
      // the map scales to fit its pane, so a glyph this small needs a heavier
      // stroke than lucide's default to survive being scaled down.
      strokeWidth={2.4}
    />
  );
}

interface CityMapProps {
  nodes: CityNode[];
  edges: CityEdge[];
  drivers: Record<string, Driver>;
  orders?: Record<string, Order>;
  pickup?: string | null;
  onNodeClick?: (nodeId: string) => void;
  onEdgeClick?: (edgeId: string) => void;
}

// how far above an intersection an order badge floats. Drivers sit on the
// intersection itself, so without this a driver parked on a pickup hides it
// and reads as a driver in the wrong colour.
const BADGE_LIFT = 25;

// Marker is the shared shape of everything the map places at an intersection:
// a dark disc so the glyph reads against whatever is underneath it, a ring in
// the thing's own colour, and the icon itself.
function Marker({ icon, x, y, color, radius, label, halo, lift }: {
  icon: MapIcon;
  x: number;
  y: number;
  color: string;
  radius: number;
  label: string;
  halo?: boolean;
  lift?: boolean;
}) {
  const cy = lift ? y - BADGE_LIFT : y;
  return (
    <g>
      <title>{label}</title>
      {lift && <line x1={x} y1={y} x2={x} y2={cy} stroke={color} strokeWidth={1.5} opacity={0.55} />}
      {halo && <circle cx={x} cy={cy} r={radius * 1.75} fill={color} opacity={0.14} />}
      <circle cx={x} cy={cy} r={radius} fill={COLORS.markerFill} stroke={color} strokeWidth={2.5} />
      <Glyph icon={icon} x={x} y={cy} size={radius * 1.5} color={color} />
    </g>
  );
}

export function CityMap({ nodes, edges, drivers, orders, pickup, onNodeClick, onEdgeClick }: CityMapProps) {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const interactive = Boolean(onNodeClick);

  // the viewBox must span min..max on each axis, not 0..max - using the max
  // alone as the width while also offsetting the origin by -padding clipped
  // the right and bottom edges of the graph.
  const xs = nodes.map((n) => n.x);
  const ys = nodes.map((n) => n.y);
  // enough room for a lifted order badge on a top-row intersection
  const pad = 44;
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

  // a parcel still waiting to be collected. Once its driver is delivering,
  // the parcel is aboard, so leaving a package sitting on the pickup would
  // say something that is no longer true.
  const awaitingPickup = Object.values(orders ?? {}).filter((order) => {
    const driver = order.assignedDriver ? drivers[order.assignedDriver] : undefined;
    return driver?.status !== "delivering";
  });

  // several orders can share an intersection; one badge per node says the
  // same thing without stacking identical glyphs on one point.
  const uniqueNodes = (ids: string[]) =>
    [...new Set(ids)].map((id) => byId.get(id)).filter((n): n is CityNode => Boolean(n));

  const pickupNodes = uniqueNodes(awaitingPickup.map((o) => o.pickup));
  const destinationNodes = uniqueNodes(Object.values(orders ?? {}).map((o) => o.destination));

  // both directions of a road are separate edges, so a closure marker keyed
  // by edge id would draw two cones on the same spot.
  const closureMidpoints = new Map<string, { x: number; y: number }>();
  for (const edge of edges) {
    if (!edge.closed) continue;
    const from = byId.get(edge.from);
    const to = byId.get(edge.to);
    if (!from || !to) continue;
    const key = [edge.from, edge.to].sort().join("|");
    closureMidpoints.set(key, { x: (from.x + to.x) / 2, y: (from.y + to.y) / 2 });
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

      {/* closed-road markers */}
      <g style={{ pointerEvents: "none" }}>
        {[...closureMidpoints].map(([key, point]) => (
          <Marker
            key={`closure-${key}`}
            icon={ICONS.closure}
            x={point.x}
            y={point.y}
            color={COLORS.closed}
            radius={13}
            label="Road closed"
          />
        ))}
      </g>

      {/* parcels waiting to be collected, and where they are headed */}
      <g style={{ pointerEvents: "none" }}>
        {destinationNodes.map((node) => (
          <Marker
            key={`destination-${node.id}`}
            icon={ICONS.destination}
            x={node.x}
            y={node.y}
            color={COLORS.destination}
            radius={13}
            lift
            label={`Delivery address — ${node.id}`}
          />
        ))}
        {pickupNodes.map((node) => (
          <Marker
            key={`pickup-${node.id}`}
            icon={ICONS.pickup}
            x={node.x}
            y={node.y}
            color={COLORS.pickup}
            radius={13}
            lift
            label={`Parcel waiting for collection — ${node.id}`}
          />
        ))}
      </g>

      {/* drivers, drawn last so they sit on top */}
      <g
        // markers are display-only; without this a driver parked on a node
        // steals the click meant for the node underneath it
        style={{ pointerEvents: "none" }}
      >
        {Object.values(drivers).map((driver) => {
          const pos = byId.get(driver.position);
          if (!pos) return null;
          const busy = driver.status !== "idle";
          return (
            <Marker
              key={driver.id}
              icon={ICONS.driver}
              x={pos.x}
              y={pos.y}
              color={driverColor[driver.status] ?? COLORS.idle}
              radius={busy ? 15 : 11}
              halo={busy}
              label={`${driver.id} — ${driver.status.replace(/_/g, " ")}`}
            />
          );
        })}
      </g>
    </svg>
  );
}
