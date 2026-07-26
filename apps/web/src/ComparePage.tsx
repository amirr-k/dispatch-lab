import { useState } from "react";
import { ArrowLeft, Download, FileText, Play, SignalHigh, SignalLow, SignalMedium } from "lucide-react";
import * as api from "./api";
import type { ComparisonResult, DemandLevel, Metrics } from "./api";

const SOURCE_URL = "https://github.com/amirr-k/dispatch-lab/blob/main/internal/service/comparison.go";

interface MetricRow {
  label: string;
  key: keyof Metrics;
  better: "lower" | "higher";
  format: (v: number) => string;
}

const ROWS: MetricRow[] = [
  { label: "Completed deliveries", key: "completedDeliveries", better: "higher", format: (v) => String(v) },
  { label: "Unassigned orders", key: "unassignedOrders", better: "lower", format: (v) => String(v) },
  { label: "Average pickup time", key: "averagePickupTime", better: "lower", format: (v) => v.toFixed(2) },
  { label: "P95 pickup time", key: "p95PickupTime", better: "lower", format: (v) => v.toFixed(2) },
  { label: "Total travel distance", key: "totalDistance", better: "lower", format: (v) => v.toFixed(1) },
  { label: "Assignment compute time", key: "assignmentComputeMs", better: "lower", format: (v) => `${v.toFixed(3)} ms` },
];

const DEMAND_LEVELS: {
  id: DemandLevel;
  label: string;
  Icon: typeof SignalLow;
  blurb: string;
}[] = [
  { id: "light", label: "Light", Icon: SignalLow, blurb: "12 orders, well spread out — more drivers than work." },
  { id: "steady", label: "Steady", Icon: SignalMedium, blurb: "20 orders at a moderate pace." },
  { id: "rush", label: "Rush", Icon: SignalHigh, blurb: "40 orders in a burst — orders compete for drivers." },
];

// delta is always optimized minus baseline, with no colour or arrow attached:
// whether a negative number is good depends entirely on the metric, so the
// direction that helps is stated per row instead of encoded in a hue.
function formatDelta(row: MetricRow, baseline: Metrics, optimized: Metrics): string {
  const b = baseline[row.key] as number;
  const o = optimized[row.key] as number;
  const diff = o - b;
  if (diff === 0) return "no change";

  const sign = diff > 0 ? "+" : "−";
  const magnitude = row.format(Math.abs(diff));
  if (b === 0) return `${sign}${magnitude}`;
  return `${sign}${magnitude} (${sign}${Math.abs((diff / b) * 100).toFixed(1)}%)`;
}

// One plain sentence saying what actually happened, derived from the numbers
// in the table rather than written ahead of time — neither strategy is
// pre-declared the winner anywhere in this page.
function verdict(result: ComparisonResult): string {
  const b = result.baseline.averagePickupTime;
  const o = result.optimized.averagePickupTime;
  const unserved = result.baseline.unassignedOrders - result.optimized.unassignedOrders;

  let sentence: string;
  if (o === b) {
    sentence = "Both strategies reached pickups equally fast on average.";
  } else {
    const pct = Math.abs(((o - b) / b) * 100).toFixed(1);
    sentence =
      o < b
        ? `Batch optimization reached pickups ${pct}% sooner on average than the nearest-driver baseline.`
        : `The nearest-driver baseline reached pickups ${pct}% sooner on average than batch optimization.`;
  }

  if (unserved > 0) {
    sentence += ` It also served ${unserved} order${unserved === 1 ? "" : "s"} the baseline left unassigned.`;
  } else if (unserved < 0) {
    sentence += ` The baseline served ${-unserved} order${unserved === -1 ? "" : "s"} the optimizer left unassigned.`;
  }
  return sentence;
}

function download(result: ComparisonResult) {
  const blob = new Blob([JSON.stringify(result, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `dispatchlab-comparison-${result.id}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

export function ComparePage() {
  const [seed, setSeed] = useState("");
  const [drivers, setDrivers] = useState(12);
  const [demand, setDemand] = useState<DemandLevel>("steady");
  const [result, setResult] = useState<ComparisonResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    setLoading(true);
    setError(null);
    try {
      const parsedSeed = seed.trim() === "" ? undefined : Number(seed);
      setResult(await api.createComparison(parsedSeed, drivers, demand));
    } catch (err) {
      setError(err instanceof Error ? err.message : "comparison failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="compare">
      <header className="compare-header">
        <h1>Compare Algorithms</h1>
        <a href="/" className="back-link">
          <ArrowLeft size={15} aria-hidden />
          Back to demo
        </a>
      </header>

      <p className="compare-intro">
        Both strategies run the identical scenario — same city, same starting driver positions, same orders arriving at
        the same times — so any difference in the results comes from the assignment decision alone. Neither is
        guaranteed to win: batch optimization only earns back the delay of waiting for a batch when orders actually
        compete for the same drivers.
      </p>

      <section className="control-panel" aria-label="Scenario controls">
        <div className="control-grid">
          <label className="field">
            <span className="field-label">Seed</span>
            <input value={seed} onChange={(e) => setSeed(e.target.value)} placeholder="e.g. 42" className="field-input" />
            <span className="field-hint">Generates the city and the orders. Blank picks one at random.</span>
          </label>

          <label className="field">
            <span className="field-label">Drivers</span>
            <input
              type="number"
              min={1}
              max={40}
              value={drivers}
              onChange={(e) => setDrivers(Number(e.target.value))}
              className="field-input"
            />
            <span className="field-hint">1–40. Fewer drivers means more competition per order.</span>
          </label>
        </div>

        <fieldset className="demand-picker">
          <legend className="field-label">Demand level</legend>
          <div className="demand-options">
            {DEMAND_LEVELS.map(({ id, label, Icon, blurb }) => (
              <button
                key={id}
                type="button"
                onClick={() => setDemand(id)}
                aria-pressed={demand === id}
                className={`demand-option${demand === id ? " is-selected" : ""}`}
              >
                <Icon size={17} aria-hidden />
                <span className="demand-option-label">{label}</span>
                <span className="demand-option-blurb">{blurb}</span>
              </button>
            ))}
          </div>
        </fieldset>

        <div className="control-actions">
          <button onClick={run} disabled={loading} className="button button-primary">
            <Play size={15} aria-hidden />
            {loading ? "Running…" : "Run Comparison"}
          </button>
          {result && (
            <button onClick={() => download(result)} className="button">
              <Download size={15} aria-hidden />
              Download JSON
            </button>
          )}
          <a href={SOURCE_URL} target="_blank" rel="noreferrer" className="button button-link">
            <FileText size={15} aria-hidden />
            Methodology
          </a>
        </div>
      </section>

      {error && (
        <p className="alert" role="alert">
          {error}
        </p>
      )}

      {result && (
        <>
          <p className="verdict">{verdict(result)}</p>

          <table className="metrics-table">
            <caption className="visually-hidden">
              Measured results for the nearest-driver baseline and batch optimizer on the same scenario
            </caption>
            <thead>
              <tr>
                <th scope="col">Metric</th>
                <th scope="col">Baseline</th>
                <th scope="col">Optimized</th>
                <th scope="col">
                  Delta
                  <span className="th-note">optimized − baseline</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {ROWS.map((row) => (
                <tr key={row.key}>
                  <th scope="row">
                    {row.label}
                    <span className="th-note">{row.better} is better</span>
                  </th>
                  <td className="num">{row.format(result.baseline[row.key] as number)}</td>
                  <td className="num">{row.format(result.optimized[row.key] as number)}</td>
                  <td className="num delta">{formatDelta(row, result.baseline, result.optimized)}</td>
                </tr>
              ))}
            </tbody>
          </table>

          <p className="scenario-note">
            Scenario: seed {result.scenario.seed}, {result.scenario.drivers} drivers, {result.scenario.demand} demand (
            {result.scenario.arrivals.length} orders), {result.scenario.batchWindow}-virtual-time-unit batch window.
            Those first three inputs reproduce this table exactly — the scenario is generated from them in{" "}
            <code>internal/service/comparison.go</code>, which is also where every metric above is computed.
          </p>
        </>
      )}
    </div>
  );
}
