import { useState } from "react";
import { CityMap } from "./CityMap";
import { HowItWorks } from "./HowItWorks";
import { Legend } from "./Legend";
import { PRESETS, type PresetId } from "./presets";
import { markShowcase } from "./api";
import { driverLabel, useSimulation, type FeedEntry } from "./useSimulation";

const SPEEDS = [1, 2, 4];

const feedColor: Record<FeedEntry["kind"], string> = {
  order: "#22c55e",
  assign: "#f5a524",
  deliver: "#7dd3fc",
  closure: "#ef4444",
  system: "#8b95a7",
};

function App() {
  const {
    simulationId,
    connected,
    nodes,
    edges,
    drivers,
    orders,
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
  const [replayUrl, setReplayUrl] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [showHowItWorks, setShowHowItWorks] = useState(false);
  const [runningPreset, setRunningPreset] = useState<PresetId | null>(null);

  const activeOrders = Object.keys(orders).length;

  // one sentence telling the visitor exactly what to do next. This is the
  // difference between a map you stare at and a map you can use, so it gets
  // the most prominent position on the page rather than a footnote.
  function nextStep(): { text: string; tone: "prompt" | "progress" } {
    if (!connected) return { text: "Connecting to the simulation…", tone: "progress" };
    if (runningPreset) return { text: "Running scenario…", tone: "progress" };
    if (pickup) return { text: `Pickup set at ${pickup} — now click where it should be delivered.`, tone: "prompt" };
    if (activeOrders > 0) {
      return {
        text: "Delivery in progress — click any road to close it and watch the driver reroute.",
        tone: "progress",
      };
    }
    return { text: "Click any intersection to set a pickup point.", tone: "prompt" };
  }

  const step = nextStep();

  async function saveReplay() {
    if (!simulationId) return;
    setSaveError(null);
    try {
      const saved = await markShowcase(simulationId);
      setReplayUrl(saved.replayUrl);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "could not save this run");
    }
  }

  async function runPreset(id: PresetId) {
    const preset = PRESETS.find((p) => p.id === id);
    if (!preset || !simulationId || nodes.length === 0) return;
    setPickup(null);
    setRunningPreset(id);
    try {
      await preset.run(simulationId, nodes);
    } finally {
      setRunningPreset(null);
    }
  }

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
    <div className="app">
      <HowItWorks open={showHowItWorks} onClose={() => setShowHowItWorks(false)} />

      <header style={styles.header}>
        <div style={{ display: "flex", alignItems: "baseline", gap: 12, flexWrap: "wrap" }}>
          <strong style={{ fontSize: 17, letterSpacing: -0.2 }}>DispatchLab</strong>
          <span style={{ color: "#8b95a7", fontSize: 13 }}>Real-time delivery assignment and routing simulator</span>
        </div>

        <nav style={{ display: "flex", alignItems: "center", gap: 8, marginLeft: "auto", flexWrap: "wrap" }}>
          <span
            style={{ ...styles.badge, color: connected ? "#22c55e" : "#ef4444" }}
            aria-live="polite"
          >
            {connected ? "● Connected" : "● Disconnected"}
          </span>
          <button onClick={() => setShowHowItWorks(true)} style={styles.linkButton}>
            How it works
          </button>
          <a href="/compare" style={styles.link}>
            Compare algorithms
          </a>
          <a
            href="https://github.com/amirr-k/dispatch-lab"
            target="_blank"
            rel="noreferrer"
            style={styles.link}
          >
            Source
          </a>
        </nav>
      </header>

      <section style={styles.hero}>
        <h1 style={styles.heroTitle}>Place an order. Watch the system assign and route a driver.</h1>
        <p style={styles.heroSub}>
          Click two points on the map to create a delivery, or start with a ready-made scenario.
        </p>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginTop: 14 }}>
          {PRESETS.map((preset) => (
            <button
              key={preset.id}
              onClick={() => runPreset(preset.id)}
              disabled={!connected || runningPreset !== null}
              title={preset.blurb}
              style={{
                ...styles.presetButton,
                ...(runningPreset === preset.id ? styles.presetButtonActive : null),
              }}
            >
              {runningPreset === preset.id ? "Running…" : preset.label}
            </button>
          ))}
        </div>
      </section>

      <div
        style={{
          ...styles.stepBar,
          borderLeftColor: step.tone === "prompt" ? "#7dd3fc" : "#3f4a63",
        }}
        aria-live="polite"
      >
        {step.text}
      </div>

      {(actionError || saveError) && (
        <div style={styles.errorBar} role="alert">
          {actionError ?? saveError}
        </div>
      )}

      <div className="app-body">
        <main className="map-pane">
          <CityMap
            nodes={nodes}
            edges={edges}
            drivers={drivers}
            orders={orders}
            pickup={pickup}
            onNodeClick={handleNodeClick}
            onEdgeClick={closeRoad}
          />
        </main>

        <aside className="sidebar">
          <section style={styles.card}>
            <h2 style={styles.cardTitle}>Latest assignment</h2>
            {assignment ? (
              <div style={{ display: "grid", gap: 5, fontSize: 14 }}>
                <div style={{ fontWeight: 600, color: "#f5a524" }}>{driverLabel(assignment.driverId)}</div>
                <Row label="Pickup ETA" value={`${assignment.pickupEtaVirtualTime.toFixed(1)} sim-time`} />
                <Row label="Distance to pickup" value={assignment.pickupDistance.toFixed(1)} />
              </div>
            ) : (
              <p style={styles.empty}>No orders yet — place one to see which driver the system picks and why.</p>
            )}
          </section>

          <section style={styles.card}>
            <Legend />
          </section>

          <section style={{ ...styles.card, flex: 1, minHeight: 140 }}>
            <h2 style={styles.cardTitle}>Activity</h2>
            {feed.length === 0 ? (
              <p style={styles.empty}>Events will appear here as deliveries progress.</p>
            ) : (
              <ul style={{ listStyle: "none", padding: 0, margin: 0, fontSize: 13 }}>
                {feed.map((entry, i) => (
                  <li
                    key={`${entry.id}-${i}`}
                    style={{
                      padding: "6px 0",
                      borderBottom: "1px solid #1a1e28",
                      display: "flex",
                      gap: 8,
                      alignItems: "flex-start",
                    }}
                  >
                    <span style={{ color: feedColor[entry.kind], lineHeight: "18px" }} aria-hidden>
                      ●
                    </span>
                    <span>{entry.text}</span>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </aside>
      </div>

      <footer style={styles.footer}>
        <div style={styles.metrics}>
          <Metric label="In progress" value={metrics.pending} />
          <Metric label="Delivered" value={metrics.delivered} />
          <Metric label="Unassignable" value={metrics.unassignable} />
          <Metric label="Sim time" value={metrics.virtualTime.toFixed(1)} />
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: 8, marginLeft: "auto", flexWrap: "wrap" }}>
          {replayUrl ? (
            <a href={replayUrl} style={{ ...styles.link, color: "#22c55e" }}>
              Open saved replay
            </a>
          ) : (
            <button onClick={saveReplay} disabled={!simulationId} style={styles.control}>
              Save replay
            </button>
          )}
          <button onClick={togglePaused} style={styles.control}>
            {paused ? "Resume" : "Pause"}
          </button>
          <button onClick={reset} style={styles.control}>
            Reset
          </button>
          <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13, color: "#8b95a7" }}>
            Speed
            <select
              aria-label="Playback speed"
              value={speed}
              onChange={(e) => changeSpeed(Number(e.target.value))}
              style={styles.control}
            >
              {SPEEDS.map((s) => (
                <option key={s} value={s}>
                  {s}x
                </option>
              ))}
            </select>
          </label>
        </div>
      </footer>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", gap: 12, color: "#9aa4b2" }}>
      <span>{label}</span>
      <span style={{ color: "#e6e9ef", fontVariantNumeric: "tabular-nums" }}>{value}</span>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number | string }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 1 }}>
      <span style={{ fontSize: 11, textTransform: "uppercase", letterSpacing: 0.5, color: "#8b95a7" }}>{label}</span>
      <span style={{ fontSize: 17, fontWeight: 600, fontVariantNumeric: "tabular-nums" }}>{value}</span>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: {
    display: "flex",
    alignItems: "center",
    gap: 16,
    padding: "12px 20px",
    borderBottom: "1px solid #242a38",
    flexWrap: "wrap",
  },
  badge: { fontSize: 13, fontWeight: 500 },
  link: { color: "#9aa4b2", fontSize: 13, textDecoration: "none", padding: "5px 8px" },
  linkButton: {
    background: "transparent",
    border: "none",
    color: "#9aa4b2",
    fontSize: 13,
    cursor: "pointer",
    padding: "5px 8px",
    font: "inherit",
    fontFamily: "inherit",
  },
  hero: { padding: "16px 20px 14px", borderBottom: "1px solid #1a1e28" },
  heroTitle: { margin: 0, fontSize: 21, fontWeight: 650, letterSpacing: -0.4, maxWidth: 720, lineHeight: 1.25 },
  heroSub: { margin: "6px 0 0", color: "#9aa4b2", fontSize: 13.5 },
  presetButton: {
    background: "#1a2033",
    // longhand so the active variant below can override the color alone
    // without React warning about a shorthand/longhand conflict.
    borderWidth: 1,
    borderStyle: "solid",
    borderColor: "#2f3850",
    color: "#e6e9ef",
    borderRadius: 7,
    padding: "8px 14px",
    fontSize: 13.5,
    cursor: "pointer",
    font: "inherit",
    fontFamily: "inherit",
  },
  presetButtonActive: { background: "#243050", borderColor: "#7dd3fc" },
  stepBar: {
    padding: "10px 20px",
    background: "#10141f",
    // kept as separate longhand properties: the tone override below sets
    // borderLeftColor, and React warns when that is mixed with a shorthand.
    borderLeftWidth: 3,
    borderLeftStyle: "solid",
    borderBottom: "1px solid #1a1e28",
    fontSize: 14,
  },
  errorBar: {
    padding: "9px 20px",
    background: "#2a1215",
    color: "#fca5a5",
    fontSize: 13,
    borderBottom: "1px solid #4c1d24",
  },
  card: { background: "#10141f", border: "1px solid #1e2534", borderRadius: 9, padding: 13 },
  cardTitle: {
    fontSize: 12,
    textTransform: "uppercase",
    letterSpacing: 0.6,
    color: "#8b95a7",
    margin: "0 0 10px",
  },
  // #6b7688 here only reached 4:1 against the card background, under the
  // 4.5:1 AA minimum; this passes at ~6:1.
  empty: { margin: 0, color: "#8b95a7", fontSize: 13, lineHeight: 1.5 },
  footer: {
    display: "flex",
    alignItems: "center",
    gap: 22,
    padding: "12px 20px",
    borderTop: "1px solid #242a38",
    flexWrap: "wrap",
  },
  metrics: { display: "flex", gap: 22, flexWrap: "wrap" },
  control: {
    background: "#1a2033",
    border: "1px solid #2f3850",
    color: "#e6e9ef",
    borderRadius: 6,
    padding: "6px 11px",
    fontSize: 13,
    cursor: "pointer",
    font: "inherit",
    fontFamily: "inherit",
  },
};

export default App;
