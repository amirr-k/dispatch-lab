import { useState } from "react";
import { CityMap } from "./CityMap";
import { useSimulation } from "./useSimulation";

const SPEEDS = [1, 2, 4];

function App() {
  const {
    connected,
    nodes,
    edges,
    drivers,
    assignment,
    feed,
    metrics,
    paused,
    speed,
    actionError,
    placeOrder,
    togglePaused,
    reset,
    changeSpeed,
    closeRoad,
  } = useSimulation();

  const [pickup, setPickup] = useState<string | null>(null);

  function handleNodeClick(nodeId: string) {
    if (!pickup) {
      setPickup(nodeId);
      return;
    }
    if (nodeId !== pickup) {
      placeOrder(pickup, nodeId);
    }
    setPickup(null);
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh" }}>
      <header style={{ display: "flex", alignItems: "center", gap: 20, padding: "12px 20px", borderBottom: "1px solid #242a38" }}>
        <strong>DispatchLab</strong>
        <span style={{ color: connected ? "#22c55e" : "#e5484d" }}>
          {connected ? "● Connected" : "● Disconnected"}
        </span>

        <div style={{ display: "flex", gap: 8, marginLeft: "auto" }}>
          <button onClick={togglePaused}>{paused ? "Resume" : "Pause"}</button>
          <button onClick={reset}>Reset</button>
          <select value={speed} onChange={(e) => changeSpeed(Number(e.target.value))}>
            {SPEEDS.map((s) => (
              <option key={s} value={s}>
                {s}x
              </option>
            ))}
          </select>
        </div>
      </header>

      <MetricsStrip metrics={metrics} pickup={pickup} actionError={actionError} />

      <div style={{ display: "flex", flex: 1, minHeight: 0 }}>
        <main style={{ flex: 1, position: "relative" }}>
          <CityMap
            nodes={nodes}
            edges={edges}
            drivers={drivers}
            pickup={pickup}
            onNodeClick={handleNodeClick}
            onEdgeClick={closeRoad}
          />
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

interface MetricsStripProps {
  metrics: { pending: number; delivered: number; unassignable: number; virtualTime: number };
  pickup: string | null;
  actionError: string | null;
}

function MetricsStrip({ metrics, pickup, actionError }: MetricsStripProps) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 24,
        padding: "8px 20px",
        borderBottom: "1px solid #242a38",
        fontSize: 13,
        color: "#9aa4b2",
      }}
    >
      <span>Virtual time: {metrics.virtualTime.toFixed(1)}</span>
      <span>Pending: {metrics.pending}</span>
      <span>Delivered: {metrics.delivered}</span>
      <span>Unassignable: {metrics.unassignable}</span>
      <span style={{ marginLeft: "auto" }}>
        {pickup ? `Pickup selected: ${pickup} — click a destination` : "Click a node to place an order, or a road to close it"}
      </span>
      {actionError && <span style={{ color: "#e5484d" }}>{actionError}</span>}
    </div>
  );
}

export default App;
