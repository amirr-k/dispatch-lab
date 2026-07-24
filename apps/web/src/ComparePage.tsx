import { useState } from "react";
import * as api from "./api";
import type { ComparisonResult, Metrics } from "./api";

interface MetricRow {
  label: string;
  key: keyof Metrics;
  higherIsBetter: boolean;
  format: (v: number) => string;
}

const ROWS: MetricRow[] = [
  { label: "Completed deliveries", key: "completedDeliveries", higherIsBetter: true, format: (v) => String(v) },
  { label: "Unassigned orders", key: "unassignedOrders", higherIsBetter: false, format: (v) => String(v) },
  { label: "Average pickup time", key: "averagePickupTime", higherIsBetter: false, format: (v) => v.toFixed(2) },
  { label: "P95 pickup time", key: "p95PickupTime", higherIsBetter: false, format: (v) => v.toFixed(2) },
  { label: "Total travel distance", key: "totalDistance", higherIsBetter: false, format: (v) => v.toFixed(1) },
  { label: "Assignment compute time (ms)", key: "assignmentComputeMs", higherIsBetter: false, format: (v) => v.toFixed(3) },
];

function deltaLabel(row: MetricRow, baseline: Metrics, optimized: Metrics): { text: string; color: string } {
  const b = baseline[row.key] as number;
  const o = optimized[row.key] as number;
  const diff = o - b;
  if (diff === 0) return { text: "no change", color: "#9aa4b2" };

  const improved = row.higherIsBetter ? diff > 0 : diff < 0;
  const pct = b !== 0 ? Math.abs((diff / b) * 100) : 0;
  const arrow = diff > 0 ? "▲" : "▼";
  return {
    text: `${arrow} ${pct.toFixed(1)}%`,
    color: improved ? "#22c55e" : "#e5484d",
  };
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
  const [result, setResult] = useState<ComparisonResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    setLoading(true);
    setError(null);
    try {
      const parsedSeed = seed.trim() === "" ? undefined : Number(seed);
      const r = await api.createComparison(parsedSeed, drivers);
      setResult(r);
    } catch (err) {
      setError(err instanceof Error ? err.message : "comparison failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div style={{ maxWidth: 900, margin: "0 auto", padding: "24px 20px", color: "#e6e9ef" }}>
      <header style={{ display: "flex", alignItems: "baseline", gap: 16, marginBottom: 8 }}>
        <h1 style={{ margin: 0, fontSize: 22 }}>Compare Algorithms</h1>
        <a href="/" style={{ color: "#9aa4b2" }}>
          ← Back to demo
        </a>
      </header>
      <p style={{ color: "#9aa4b2", marginTop: 0 }}>
        Runs the identical deterministic scenario — same city, same driver positions, same orders, same demand
        timing — through the nearest-driver baseline and the batch cost-minimizing optimizer, and reports what
        actually happened. Neither algorithm is guaranteed to win.
      </p>

      <div style={{ display: "flex", gap: 12, alignItems: "flex-end", margin: "20px 0" }}>
        <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 13 }}>
          Seed (blank = random)
          <input
            value={seed}
            onChange={(e) => setSeed(e.target.value)}
            placeholder="e.g. 42"
            style={{ padding: 6, width: 140 }}
          />
        </label>
        <label style={{ display: "flex", flexDirection: "column", gap: 4, fontSize: 13 }}>
          Drivers
          <input
            type="number"
            min={1}
            max={40}
            value={drivers}
            onChange={(e) => setDrivers(Number(e.target.value))}
            style={{ padding: 6, width: 100 }}
          />
        </label>
        <button onClick={run} disabled={loading} style={{ padding: "8px 16px" }}>
          {loading ? "Running…" : "Run Comparison"}
        </button>
        {result && <button onClick={() => download(result)}>Download JSON</button>}
      </div>

      {error && <p style={{ color: "#e5484d" }}>{error}</p>}

      {result && (
        <>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 14 }}>
            <thead>
              <tr style={{ borderBottom: "1px solid #242a38", textAlign: "left" }}>
                <th style={{ padding: "8px 4px" }}>Metric</th>
                <th style={{ padding: "8px 4px" }}>Baseline</th>
                <th style={{ padding: "8px 4px" }}>Optimized</th>
                <th style={{ padding: "8px 4px" }}>Difference</th>
              </tr>
            </thead>
            <tbody>
              {ROWS.map((row) => {
                const delta = deltaLabel(row, result.baseline, result.optimized);
                return (
                  <tr key={row.key} style={{ borderBottom: "1px solid #1a1e28" }}>
                    <td style={{ padding: "8px 4px" }}>{row.label}</td>
                    <td style={{ padding: "8px 4px" }}>{row.format(result.baseline[row.key] as number)}</td>
                    <td style={{ padding: "8px 4px" }}>{row.format(result.optimized[row.key] as number)}</td>
                    <td style={{ padding: "8px 4px", color: delta.color }}>{delta.text}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>

          <p style={{ fontSize: 12, color: "#9aa4b2", marginTop: 24 }}>
            Scenario: seed {result.scenario.seed}, {result.scenario.drivers} drivers, {result.scenario.batchWindow}
            -virtual-time-unit batch window. Both runs replay the identical checked-in demand pattern deterministically
            generated from the seed, so this result reproduces exactly from the seed and driver count alone — see{" "}
            <code>internal/service/comparison.go</code> for how the scenario and metrics are computed.
          </p>
        </>
      )}
    </div>
  );
}
