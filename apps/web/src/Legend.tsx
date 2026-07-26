import { COLORS } from "./colors";

// every entry pairs a symbol with a written label: the spec requires the map
// be readable without relying on color alone.
const ENTRIES: { label: string; swatch: React.ReactNode }[] = [
  {
    label: "Idle driver",
    swatch: <span style={{ ...dot(COLORS.idle), width: 11, height: 11 }} />,
  },
  {
    label: "Driver heading to pickup",
    swatch: <span style={dot(COLORS.toPickup)} />,
  },
  {
    label: "Driver delivering",
    swatch: <span style={dot(COLORS.delivering)} />,
  },
  {
    label: "Pickup (P)",
    swatch: (
      <span style={ring(COLORS.pickup)} aria-hidden>
        P
      </span>
    ),
  },
  {
    label: "Destination (D)",
    swatch: (
      <span style={{ ...ring(COLORS.destination), borderRadius: 4 }} aria-hidden>
        D
      </span>
    ),
  },
  {
    label: "Closed road",
    swatch: (
      <span
        style={{
          width: 18,
          height: 0,
          borderTop: `3px dotted ${COLORS.closed}`,
          display: "inline-block",
        }}
      />
    ),
  },
];

function dot(color: string): React.CSSProperties {
  return {
    width: 15,
    height: 15,
    borderRadius: "50%",
    background: color,
    display: "inline-block",
    flexShrink: 0,
  };
}

function ring(color: string): React.CSSProperties {
  return {
    width: 17,
    height: 17,
    borderRadius: "50%",
    border: `2px solid ${color}`,
    color,
    fontSize: 10,
    fontWeight: 700,
    lineHeight: "13px",
    textAlign: "center",
    display: "inline-block",
    flexShrink: 0,
  };
}

export function Legend() {
  return (
    <div>
      <h2 style={{ fontSize: 12, textTransform: "uppercase", letterSpacing: 0.6, color: "#8b95a7", margin: "0 0 10px" }}>
        Legend
      </h2>
      <ul style={{ listStyle: "none", padding: 0, margin: 0, display: "grid", gap: 8 }}>
        {ENTRIES.map((entry) => (
          <li key={entry.label} style={{ display: "flex", alignItems: "center", gap: 10, fontSize: 13 }}>
            {entry.swatch}
            <span>{entry.label}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
