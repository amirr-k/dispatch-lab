import { COLORS } from "./colors";
import { ICONS, type MapIcon } from "./icons";

// every entry pairs a symbol with a written label: the spec requires the map
// be readable without relying on color alone. The glyphs come from the same
// module the map draws from, so the two cannot disagree.
const ENTRIES: { label: string; icon: MapIcon; color: string; muted?: boolean }[] = [
  { label: "Idle driver", icon: ICONS.driver, color: COLORS.idle, muted: true },
  { label: "Driver heading to a pickup", icon: ICONS.driver, color: COLORS.toPickup },
  { label: "Driver carrying a delivery", icon: ICONS.driver, color: COLORS.delivering },
  { label: "Parcel waiting for collection", icon: ICONS.pickup, color: COLORS.pickup },
  { label: "Delivery address", icon: ICONS.destination, color: COLORS.destination },
  { label: "Closed road", icon: ICONS.closure, color: COLORS.closed },
];

export function Legend() {
  return (
    <div>
      <h2 className="card-title">Legend</h2>
      <ul className="legend">
        {ENTRIES.map(({ label, icon: Icon, color, muted }) => (
          <li key={label}>
            <span
              className="legend-badge"
              style={{ borderColor: color, color, opacity: muted ? 0.75 : 1 }}
              aria-hidden
            >
              <Icon size={13} strokeWidth={2.2} />
            </span>
            <span>{label}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
