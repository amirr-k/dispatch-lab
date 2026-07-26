import { useState } from "react";
import {
  AlertTriangle,
  BarChart3,
  Car,
  CheckCircle2,
  Clock,
  Code2,
  HelpCircle,
  Info,
  MousePointerClick,
  Package,
  Pause,
  Play,
  RotateCcw,
  Save,
  Timer,
  TrafficCone,
  Truck,
  Wifi,
  WifiOff,
} from "lucide-react";
import { CityMap } from "./CityMap";
import { HowItWorks } from "./HowItWorks";
import { Legend } from "./Legend";
import { PRESETS, PRESET_ICONS, type PresetId } from "./presets";
import { markShowcase } from "./api";
import { driverLabel, useSimulation, type FeedEntry } from "./useSimulation";

const SPEEDS = [1, 2, 4];

// the feed already reads as sentences; the icon is there so a glance can tell
// order from delivery from closure without reading every line.
const FEED_ICONS: Record<FeedEntry["kind"], typeof Package> = {
  order: Package,
  assign: Car,
  deliver: CheckCircle2,
  closure: TrafficCone,
  system: Info,
};

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
    toggleRoad,
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

      <header className="app-header">
        <div className="brand">
          <strong>DispatchLab</strong>
          <span>Real-time delivery assignment and routing simulator</span>
        </div>

        <nav className="app-nav">
          <span className="status-badge" style={{ color: connected ? "#22c55e" : "#ef4444" }} aria-live="polite">
            {connected ? <Wifi size={14} aria-hidden /> : <WifiOff size={14} aria-hidden />}
            {connected ? "Connected" : "Disconnected"}
          </span>
          <button onClick={() => setShowHowItWorks(true)} className="nav-link">
            <HelpCircle size={14} aria-hidden />
            How it works
          </button>
          <a href="/compare" className="nav-link">
            <BarChart3 size={14} aria-hidden />
            Compare algorithms
          </a>
          <a href="https://github.com/amirr-k/dispatch-lab" target="_blank" rel="noreferrer" className="nav-link">
            <Code2 size={14} aria-hidden />
            Source
          </a>
        </nav>
      </header>

      <section className="hero">
        <h1>Place an order. Watch the system assign and route a driver.</h1>
        <p>Click two points on the map to create a delivery, or start with a ready-made scenario.</p>
        <div className="preset-row">
          {PRESETS.map((preset) => {
            const Icon = PRESET_ICONS[preset.id];
            return (
              <button
                key={preset.id}
                onClick={() => runPreset(preset.id)}
                disabled={!connected || runningPreset !== null}
                title={preset.blurb}
                className={`button${runningPreset === preset.id ? " is-running" : ""}`}
              >
                <Icon size={15} aria-hidden />
                {runningPreset === preset.id ? "Running…" : preset.label}
              </button>
            );
          })}
        </div>
      </section>

      <div className={`step-bar${step.tone === "prompt" ? " is-prompt" : ""}`} aria-live="polite">
        <MousePointerClick size={15} aria-hidden />
        {step.text}
      </div>

      {(actionError || saveError) && (
        <div className="error-bar" role="alert">
          <AlertTriangle size={15} aria-hidden />
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
            onEdgeClick={toggleRoad}
          />
        </main>

        <aside className="sidebar">
          <section className="card">
            <h2 className="card-title">Latest assignment</h2>
            {assignment ? (
              <div className="assignment">
                <div className="assignment-driver">
                  <Car size={16} aria-hidden />
                  {driverLabel(assignment.driverId)}
                </div>
                <Row label="Reaches pickup at" value={`${assignment.pickupEtaVirtualTime.toFixed(1)} sim-time`} />
                <Row label="Distance to pickup" value={assignment.pickupDistance.toFixed(1)} />
              </div>
            ) : (
              <p className="empty">No orders yet — place one to see which driver the system picks and why.</p>
            )}
          </section>

          <section className="card">
            <Legend />
          </section>

          <section className="card card-grow">
            <h2 className="card-title">Activity</h2>
            {feed.length === 0 ? (
              <p className="empty">Events will appear here as deliveries progress.</p>
            ) : (
              // the one part of the sidebar that grows without bound, so it
              // takes the scrolling instead of the whole sidebar - which
              // otherwise scrolls the legend off the screen. Focusable
              // because a scrollable region a keyboard cannot reach is a
              // WCAG 2.1.1 failure.
              <ul className="feed" tabIndex={0} aria-label="Activity feed">
                {feed.map((entry, i) => {
                  const Icon = FEED_ICONS[entry.kind];
                  return (
                    <li key={`${entry.id}-${i}`}>
                      <Icon size={14} color={feedColor[entry.kind]} aria-hidden />
                      <span>{entry.text}</span>
                    </li>
                  );
                })}
              </ul>
            )}
          </section>
        </aside>
      </div>

      <footer className="app-footer">
        <div className="metrics">
          <Metric label="In progress" value={metrics.pending} icon={Truck} />
          <Metric label="Delivered" value={metrics.delivered} icon={CheckCircle2} />
          <Metric label="Unassignable" value={metrics.unassignable} icon={AlertTriangle} />
          <Metric label="Sim time" value={metrics.virtualTime.toFixed(1)} icon={Clock} />
        </div>

        <div className="footer-controls">
          {replayUrl ? (
            <a href={replayUrl} className="nav-link is-success">
              <Play size={14} aria-hidden />
              Open saved replay
            </a>
          ) : (
            <button onClick={saveReplay} disabled={!simulationId} className="button button-sm">
              <Save size={14} aria-hidden />
              Save replay
            </button>
          )}
          <button onClick={togglePaused} className="button button-sm">
            {paused ? <Play size={14} aria-hidden /> : <Pause size={14} aria-hidden />}
            {paused ? "Resume" : "Pause"}
          </button>
          <button onClick={reset} className="button button-sm">
            <RotateCcw size={14} aria-hidden />
            Reset
          </button>
          <label className="speed-control">
            <Timer size={14} aria-hidden />
            <span>Speed</span>
            <select
              aria-label="Playback speed"
              value={speed}
              onChange={(e) => changeSpeed(Number(e.target.value))}
              className="button button-sm"
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
    <div className="assignment-row">
      <span>{label}</span>
      <span className="num">{value}</span>
    </div>
  );
}

function Metric({ label, value, icon: Icon }: { label: string; value: number | string; icon: typeof Truck }) {
  return (
    <div className="metric">
      <span className="metric-label">
        <Icon size={13} aria-hidden />
        {label}
      </span>
      <span className="metric-value">{value}</span>
    </div>
  );
}

export default App;
