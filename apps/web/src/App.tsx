import { CityMap } from "./CityMap";
import { useSimulation } from "./useSimulation";

function App() {
  const { connected, nodes, edges, drivers, assignment, feed } = useSimulation();

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh" }}>
      <header style={{ padding: "12px 20px", borderBottom: "1px solid #242a38" }}>
        <strong>DispatchLab</strong>
        <span style={{ marginLeft: 16, color: connected ? "#22c55e" : "#e5484d" }}>
          {connected ? "● Connected" : "● Disconnected"}
        </span>
      </header>

      <div style={{ display: "flex", flex: 1, minHeight: 0 }}>
        <main style={{ flex: 1, position: "relative" }}>
          <CityMap nodes={nodes} edges={edges} drivers={drivers} />
          {assignment && (
            <div
              style={{
                position: "absolute",
                top: 16,
                left: 16,
                background: "#131722",
                border: "1px solid #242a38",
                borderRadius: 8,
                padding: 12,
                fontSize: 14,
              }}
            >
              <div>Driver {assignment.driverId} assigned</div>
              <div>Pickup ETA: {assignment.pickupEtaVirtualTime.toFixed(1)}</div>
              <div>Assignment computed in: {assignment.assignmentComputeMs.toFixed(2)}ms</div>
            </div>
          )}
        </main>

        <aside
          style={{
            width: 280,
            borderLeft: "1px solid #242a38",
            padding: 12,
            overflowY: "auto",
          }}
        >
          <h3 style={{ marginTop: 0 }}>Event feed</h3>
          <ul style={{ listStyle: "none", padding: 0, margin: 0, fontSize: 13 }}>
            {feed.map((line, i) => (
              <li key={i} style={{ padding: "4px 0", borderBottom: "1px solid #1a1e28" }}>
                {line}
              </li>
            ))}
          </ul>
        </aside>
      </div>
    </div>
  );
}

export default App;
