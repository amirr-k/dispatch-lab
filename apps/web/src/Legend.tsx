import { COLORS } from "./colors";
import { ICONS, type MapIcon } from "./iconSet";

// every entry pairs a symbol with a written label so the map stays readable
// without relying on color alone. The glyphs come from the same module the
// map draws from, so the two cannot disagree.
const ENTRIES: { label: string; icon: MapIcon; color: string; muted?: boolean }[] = [
  { label: "Idle driver", icon: ICONS.driver, color: COLORS.idle, muted: true },
  { label: "Driver heading to a pickup", icon: ICONS.driver, color: COLORS.toPickup },
  { label: "Driver carrying a delivery", icon: ICONS.driver, color: COLORS.delivering },
  { label: "Parcel waiting for collection", icon: ICONS.pickup, color: COLORS.pickup },
  { label: "Delivery address", icon: ICONS.destination, color: COLORS.destination },
];

function ImageBadge({ src, color }: { src: string; color: string }) {
  return (
    <span className="legend-badge" style={{ borderColor: color, color }} aria-hidden>
      <img src={src} alt="" className="legend-image" />
    </span>
  );
}

function HazardBadge({ color }: { color: string }) {
  return (
    <span className="legend-badge" style={{ borderColor: color, color }} aria-hidden>
      <svg width={13} height={13} viewBox="0 0 20 18" fill="none" stroke={color} strokeWidth={2}>
        <polygon points="10,1 18,16 2,16" fill="none" />
        <line x1="10" y1="7" x2="10" y2="11" strokeLinecap="round" />
        <line x1="10" y1="13.5" x2="10" y2="14" strokeLinecap="round" />
      </svg>
    </span>
  );
}

export function Legend() {
  return (
    <div>
      <h2 className="card-title">Legend</h2>
      <ul className="legend">
        {ENTRIES.map(({ label, icon, color, muted }) => (
          <li key={label}>
            <ImageBadge src={icon} color={color} />
            <span style={{ opacity: muted ? 0.75 : 1 }}>{label}</span>
          </li>
        ))}
        <li>
          <HazardBadge color={COLORS.closed} />
          <span>Closed road</span>
        </li>
      </ul>
    </div>
  );
}
