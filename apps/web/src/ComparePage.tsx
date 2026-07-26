import { useState } from "react";
import { withBase } from "./basePath";
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
  { label: "Served fraction", key: "servedFraction", better: "higher", format: (v) => `${(v * 100).toFixed(1)}%` },
  { label: "Average pickup time", key: "averagePickupTime", better: "lower", format: (v) => v.toFixed(2) },
  { label: "P95 pickup time", key: "p95PickupTime", better: "lower", format: (v) => v.toFixed(2) },
  { label: "Total travel distance", key: "totalDistance", better: "lower", format: (v) => v.toFixed(1) },
];

const DEMAND_LEVELS: {
  id: DemandLevel;
  label: string;
  Icon: typeof SignalLow;
  blurb: string;
}[] = [
  { id: "light", label: "Light", Icon: SignalLow, blurb: "12 orders, well spread out — more drivers than work." },
  { id: "steady", label: "Steady", Icon: SignalMedium, blurb: "20 orders at a moderate pace." },
  { id: "rush", label: "Rush", Icon: SignalHigh, blurb: "Orders released in a burst — they compete for the same drivers." },
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

function dispatchMix(m: Metrics): string {
  const batch = m.batchDispatches;
  const immediate = m.immediateDispatches;
  if (batch === 0 && immediate === 0) return "none (baseline assigns immediately)";
  if (batch > 0 && immediate === 0) return `batch only (${batch})`;
  if (batch === 0 && immediate > 0) return `immediate only (${immediate})`;
  return `mixed (${batch} batch, ${immediate} immediate)`;
}

// One plain sentence saying what actually happened, derived from the numbers
// in the table rather than written ahead of time — neither strategy is
// pre-declared the winner anywhere in this page.
function verdict(result: ComparisonResult): string {
  const b = result.baseline;
  const o = result.optimized;
  const pickupDiff = o.averagePickupTime - b.averagePickupTime;
  const pickupPct = b.averagePickupTime === 0 ? 0 : Math.abs((pickupDiff / b.averagePickupTime) * 100);
  const servedDiff = o.completedDeliveries - b.completedDeliveries;
  const distanceDiff = o.totalDistance - b.totalDistance;
  const unassignedDiff = o.unassignedOrders - b.unassignedOrders;

  const parts: string[] = [];

  if (servedDiff > 0) {
    parts.push(
      `Batch optimization completed ${servedDiff} more delivery${servedDiff === 1 ? "" : "ies"} than the nearest-driver baseline.`,
    );
  } else if (servedDiff < 0) {
    parts.push(
      `The nearest-driver baseline completed ${-servedDiff} more delivery${-servedDiff === 1 ? "" : "ies"} than batch optimization.`,
    );
  } else if (pickupDiff === 0 && distanceDiff === 0 && unassignedDiff === 0) {
    parts.push("Both strategies produced the same completions, pickup times, and distance.");
  } else if (pickupDiff === 0) {
    parts.push("Both strategies reached pickups equally fast on average.");
  }

  if (unassignedDiff < 0) {
    parts.push(`It left ${-unassignedDiff} fewer order${-unassignedDiff === 1 ? "" : "s"} unassigned.`);
  } else if (unassignedDiff > 0) {
    parts.push(`The baseline left ${unassignedDiff} fewer order${unassignedDiff === 1 ? "" : "s"} unassigned.`);
  }

  if (pickupDiff < 0) {
    parts.push(`It also reached pickups ${pickupPct.toFixed(1)}% sooner on average.`);
  } else if (pickupDiff > 0 && servedDiff === 0) {
    parts.push(`The baseline reached pickups ${pickupPct.toFixed(1)}% sooner on average.`);
  }

  if (distanceDiff < 0) {
    parts.push(`Batch optimization traveled ${Math.abs(distanceDiff).toFixed(1)} fewer distance units in total.`);
  } else if (distanceDiff > 0 && servedDiff <= 0) {
    parts.push(`The baseline traveled ${distanceDiff.toFixed(1)} fewer distance units in total.`);
  }

  return parts.length > 0 ? parts.join(" ") : "The two strategies produced identical results on every metric.";
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
  const [demand, setDemand] = useState<DemandLevel>("rush");
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
        <a href={withBase("/")} className="back-link">
          <ArrowLeft size={15} aria-hidden />
          Back to demo
        </a>
      </header>

      <p className="compare-intro">
        Both strategies run the identical scenario — same city, same starting driver positions, same orders arriving at
        the same times — so any difference in the results comes from the assignment decision alone. Neither is
        guaranteed to win: when orders compete, optimized batches them; when a single order has an idle driver, it
        assigns right away at intake (same as baseline) instead of waiting for a dispatcher tick.
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

          <p className="scenario-note" data-testid="dispatch-mix">
            Optimized dispatch mix: {dispatchMix(result.optimized)}.
          </p>

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
            {result.scenario.arrivals.length} orders), min batch {result.scenario.minBatchSize}, max wait{" "}
            {result.scenario.maxWaitVirtualTime} virtual-time units. Those inputs reproduce this table exactly — the
            scenario is generated from them in <code>internal/service/comparison.go</code>, which is also where every
            metric above is computed.
          </p>
        </>
      )}
    </div>
  );
}
