import { COLORS, driverColor } from "./colors";
import { ICONS, type MapIcon } from "./iconSet";
import { Glyph } from "./icons";
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

// Marker is the shared shape of everything the map places at an intersection:
// a dark disc so the glyph reads against whatever is underneath it, a ring in
// the thing's own colour, and the icon itself. A lift floats the badge above
// the intersection so drivers standing on the same node do not hide it.
function Marker({ icon, x, y, color, radius, label, halo, lift, pulse, glow, rotation }: {
  icon: MapIcon;
  x: number;
  y: number;
  color: string;
  radius: number;
  label: string;
  halo?: boolean;
  lift?: boolean;
  pulse?: boolean;
  glow?: boolean;
  rotation?: number;
}) {
  const cy = lift ? y - BADGE_LIFT : y;
  return (
    <g
      transform={rotation != null ? `rotate(${rotation * (180 / Math.PI)}, ${x}, ${cy})` : undefined}
      filter="url(#drop)"
    >
      <title>{label}</title>
      {lift && <line x1={x} y1={y} x2={x} y2={cy} stroke={color} strokeWidth={1.5} opacity={0.55} />}
      {halo && <circle cx={x} cy={cy} r={radius * 1.8} fill={color} opacity={0.16} />}
      {pulse && <circle cx={x} cy={cy} r={radius * 2.2} fill={color} opacity={0.3} className="pulse-ring" />}
      {glow && <circle cx={x} cy={cy} r={radius * 2.2} fill={color} opacity={0.22} filter="url(#glow)" />}
      <circle cx={x} cy={cy} r={radius} fill={COLORS.markerFill} stroke={color} strokeWidth={2.5} />
      <Glyph icon={icon} x={x} y={cy} size={radius * 2.1} color={color} />
    </g>
  );
}

// Hazard is a cartoon closed-road sign: a bright triangle with a slash.
function Hazard({ x, y, color }: { x: number; y: number; color: string }) {
  const size = 32;
  return (
    <g style={{ pointerEvents: "none" }} filter="url(#drop)">
      <title>Road closed</title>
      <polygon
        points={`${x},${y - size} ${x - size * 0.9},${y + size * 0.7} ${x + size * 0.9},${y + size * 0.7}`}
        fill={COLORS.markerFill}
        stroke={color}
        strokeWidth={3}
      />
      <text x={x} y={y + size * 0.25} textAnchor="middle" fontSize={16} fontWeight={800} fill={color}>
        !
      </text>
    </g>
  );
}

// how far above an intersection an order badge floats. Bigger badges need
// more clearance so they don't completely cover the intersection below.
const BADGE_LIFT = 42;

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

  // edge thickness hints at road length: long roads are visibly thicker,
  // which is one of the ways the page shows that distance matters.
  const edgeLengths = new Map(edges.map((e) => {
    const from = byId.get(e.from);
    const to = byId.get(e.to);
    const len = from && to ? Math.hypot(to.x - from.x, to.y - from.y) : 0;
    return [e.id, len] as const;
  }));
  const maxLen = Math.max(1, ...edgeLengths.values());

  return (
    <svg
      viewBox={`${minX} ${minY} ${width} ${height}`}
      role="img"
      aria-label="City map showing drivers, orders, and roads"
      style={{ width: "100%", height: "100%", display: "block" }}
    >
      <defs>
        <filter id="glow" x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur stdDeviation="3" result="coloredBlur" />
          <feMerge>
            <feMergeNode in="coloredBlur" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
        <filter id="drop" x="-50%" y="-50%" width="200%" height="200%">
          <feDropShadow dx="0" dy="1" stdDeviation="2" floodColor="#000" floodOpacity="0.6" />
        </filter>
      </defs>

      {/* roads */}
      {edges.map((edge) => {
        const from = byId.get(edge.from);
        const to = byId.get(edge.to);
        if (!from || !to) return null;
        const len = edgeLengths.get(edge.id) ?? 0;
        // long roads are thicker and more saturated, so you can literally see
        // distance: a main road looks heavier than a short alley.
        const lengthRatio = len / maxLen;
        const thickness = 2.5 + lengthRatio * 5;
        const brightness = 0.35 + lengthRatio * 0.35;
        return (
          <g key={edge.id}>
            <line
              x1={from.x}
              y1={from.y}
              x2={to.x}
              y2={to.y}
              stroke={edge.closed ? COLORS.closed : COLORS.road}
              strokeWidth={edge.closed ? 5 : thickness}
              strokeLinecap="round"
              strokeDasharray={edge.closed ? "1 9" : undefined}
              opacity={edge.closed ? 0.85 : brightness}
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
                style={{ cursor: "pointer" }}
                onClick={() => onEdgeClick(edge.id)}
              >
                <title>
                  {edge.closed ? "Road closed — click to reopen" : `Road length ${len.toFixed(0)} — click to close`}
                </title>
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

      {/* closed-road hazard markers */}
      {[...closureMidpoints].map(([key, point]) => (
        <Hazard key={`closure-${key}`} x={point.x} y={point.y} color={COLORS.closed} />
      ))}

      {/* parcels waiting to be collected, and where they are headed */}
      <g style={{ pointerEvents: "none" }}>
        {destinationNodes.map((node) => (
          <Marker
            key={`destination-${node.id}`}
            icon={ICONS.destination}
            x={node.x}
            y={node.y}
            color={COLORS.destination}
            radius={28}
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
            radius={26}
            lift
            pulse={node.id === pickup}
            glow
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
          const pos = driver.x != null && driver.y != null ? { x: driver.x, y: driver.y } : byId.get(driver.position);
          if (!pos) return null;
          const busy = driver.status !== "idle";
          const rotation = driverRotation(driver, byId);
          return (
            <Marker
              key={driver.id}
              icon={ICONS.driver}
              x={pos.x}
              y={pos.y}
              color={driverColor[driver.status] ?? COLORS.idle}
              radius={busy ? 28 : 22}
              halo={busy}
              rotation={rotation}
              label={`${driver.id} — ${driver.status.replace(/_/g, " ")}`}
            />
          );
        })}
      </g>
    </svg>
  );
}

// driverRotation points the car icon along the road it is currently on, so a
// driver moving east looks east instead of every car facing the same direction.
function driverRotation(driver: Driver, byId: Map<string, CityNode>): number | undefined {
  if (!driver.route || driver.routeIndex == null || driver.routeIndex >= driver.route.length - 1) {
    return undefined;
  }
  const from = byId.get(driver.route[driver.routeIndex]);
  const to = byId.get(driver.route[driver.routeIndex + 1]);
  if (!from || !to) return undefined;
  return Math.atan2(to.y - from.y, to.x - from.x);
}
