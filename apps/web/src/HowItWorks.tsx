import { withBase } from "./basePath";

// the spec is explicit that the landing page must not lead with Go,
// WebSockets, A*, or matching terminology - all of that lives here, behind a
// deliberate click, so the first screen stays readable to someone who does
// not know what any of those words mean.

interface HowItWorksProps {
  open: boolean;
  onClose: () => void;
}

const STEPS = [
  {
    title: "You place an order",
    body: "You pick two intersections. The browser sends them to the server as a command — it never decides anything itself.",
  },
  {
    title: "The server picks a driver",
    body: "It finds nearby idle drivers, works out how far each one really is by road (not straight-line distance), and assigns the best fit.",
  },
  {
    title: "It plans a route",
    body: "A shortest-path search runs over the road network, first to the pickup, then on to the destination.",
  },
  {
    title: "You watch it happen",
    body: "The driver's position streams back to your browser continuously as the simulation clock advances.",
  },
  {
    title: "Closing a road forces a rethink",
    body: "Any route crossing a closed road is thrown out and recomputed from where the driver currently stands. If nowhere is reachable, the order is reported as unassignable rather than silently dropped.",
  },
];

export function HowItWorks({ open, onClose }: HowItWorksProps) {
  if (!open) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="How DispatchLab works"
      onClick={onClose}
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(4,6,12,0.72)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        padding: 20,
        zIndex: 50,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          background: "#121724",
          border: "1px solid #2a3145",
          borderRadius: 12,
          maxWidth: 620,
          width: "100%",
          maxHeight: "85vh",
          overflowY: "auto",
          padding: 24,
        }}
      >
        <div style={{ display: "flex", alignItems: "baseline", justifyContent: "space-between", gap: 16 }}>
          <h2 style={{ margin: 0, fontSize: 20 }}>How it works</h2>
          <button onClick={onClose} aria-label="Close" style={closeButton}>
            ✕
          </button>
        </div>

        <ol style={{ margin: "18px 0 0", padding: 0, listStyle: "none", display: "grid", gap: 16 }}>
          {STEPS.map((step, i) => (
            <li key={step.title} style={{ display: "flex", gap: 12 }}>
              <span style={stepNumber}>{i + 1}</span>
              <div>
                <div style={{ fontWeight: 600, marginBottom: 3 }}>{step.title}</div>
                <div style={{ color: "#9aa4b2", fontSize: 14, lineHeight: 1.5 }}>{step.body}</div>
              </div>
            </li>
          ))}
        </ol>

        <div style={{ marginTop: 22, paddingTop: 18, borderTop: "1px solid #242a38", fontSize: 13, color: "#9aa4b2", lineHeight: 1.6 }}>
          <strong style={{ color: "#e6e9ef" }}>Under the hood:</strong> a Go backend owns all simulation, routing, and
          assignment state and streams events over a WebSocket. Routing is A*; the stronger of the two assignment
          strategies solves a batch min-cost matching over a spatially-indexed candidate set. Runs are deterministic
          from a seed, which is what makes the{" "}
          <a href={withBase("/compare")} style={{ color: "#7dd3fc" }}>
            algorithm comparison
          </a>{" "}
          reproducible.
        </div>
      </div>
    </div>
  );
}

const closeButton: React.CSSProperties = {
  background: "transparent",
  border: "1px solid #2a3145",
  color: "#9aa4b2",
  borderRadius: 6,
  cursor: "pointer",
  padding: "4px 9px",
  fontSize: 13,
};

const stepNumber: React.CSSProperties = {
  flexShrink: 0,
  width: 24,
  height: 24,
  borderRadius: "50%",
  background: "#1d2740",
  color: "#7dd3fc",
  fontSize: 12,
  fontWeight: 700,
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
};
