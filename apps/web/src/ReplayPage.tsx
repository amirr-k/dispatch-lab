import { useCallback, useEffect, useRef, useState } from "react";
import { CityMap } from "./CityMap";
import { getReplay, type ReplayLog } from "./api";
import { emptyFrame, foldTo, type Frame } from "./replayState";

// playbackIntervalMs is how fast a playing scrubber advances one event. It is
// display pacing only — the run itself already happened, on the simulation's
// own clock.
const PLAYBACK_INTERVAL_MS = 120;

export function ReplayPage({ simulationId }: { simulationId: string }) {
  const [log, setLog] = useState<ReplayLog | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [index, setIndex] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [frame, setFrame] = useState<Frame>(emptyFrame);

  // the last folded frame, so scrubbing forward continues from here instead
  // of replaying the whole log on every step.
  const cache = useRef<{ frame: Frame; index: number }>({ frame: emptyFrame(), index: 0 });

  useEffect(() => {
    let cancelled = false;
    getReplay(simulationId)
      .then((loaded) => {
        if (cancelled) return;
        setLog(loaded);
        setIndex(loaded.events.length);
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      });
    return () => {
      cancelled = true;
    };
  }, [simulationId]);

  useEffect(() => {
    if (!log) return;
    const next = foldTo(log.events, index, cache.current);
    cache.current = { frame: next, index };
    setFrame(next);
  }, [log, index]);

  useEffect(() => {
    if (!playing || !log) return;
    if (index >= log.events.length) {
      setPlaying(false);
      return;
    }
    const timer = setTimeout(() => setIndex((i) => i + 1), PLAYBACK_INTERVAL_MS);
    return () => clearTimeout(timer);
  }, [playing, index, log]);

  const scrub = useCallback((next: number) => {
    setPlaying(false);
    setIndex(next);
  }, []);

  if (error) {
    return (
      <Shell>
        <p style={{ color: "#e5484d" }}>{error}</p>
        <p style={{ color: "#9aa4b2" }}>
          Replays exist for runs that were saved. Open the live demo and use “Save replay” to create one.
        </p>
      </Shell>
    );
  }

  if (!log) {
    return (
      <Shell>
        <p style={{ color: "#9aa4b2" }}>Loading replay…</p>
      </Shell>
    );
  }

  const total = log.events.length;
  const current = index > 0 ? log.events[index - 1] : null;
  const delivered = Object.values(frame.orders).filter((o) => o.status === "delivered").length;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh" }}>
      <header
        style={{
          display: "flex",
          alignItems: "center",
          gap: 20,
          padding: "12px 20px",
          borderBottom: "1px solid #242a38",
        }}
      >
        <strong>DispatchLab</strong>
        <span style={{ color: "#9aa4b2" }}>
          Replay · seed {log.simulation.seed} · {log.simulation.drivers} drivers
          {log.simulation.showcase ? " · saved" : ""}
        </span>
        <a href="/" style={{ color: "#9aa4b2", marginLeft: "auto" }}>
          Live demo
        </a>
      </header>

      <div
        style={{
          display: "flex",
          gap: 24,
          padding: "10px 20px",
          borderBottom: "1px solid #242a38",
          color: "#9aa4b2",
          fontVariantNumeric: "tabular-nums",
        }}
      >
        <span>
          Event <strong style={{ color: "#e6e9ef" }}>{index}</strong> of {total}
        </span>
        <span>
          Virtual time <strong style={{ color: "#e6e9ef" }}>{frame.virtualTime.toFixed(1)}</strong>
        </span>
        <span>
          Orders <strong style={{ color: "#e6e9ef" }}>{Object.keys(frame.orders).length}</strong>
        </span>
        <span>
          Delivered <strong style={{ color: "#e6e9ef" }}>{delivered}</strong>
        </span>
        {current && <span>Last event: {current.type}</span>}
      </div>

      <main style={{ flex: 1, minHeight: 0 }}>
        <CityMap nodes={frame.nodes} edges={frame.edges} drivers={frame.drivers} />
      </main>

      <footer style={{ display: "flex", alignItems: "center", gap: 16, padding: "12px 20px", borderTop: "1px solid #242a38" }}>
        <button onClick={() => setPlaying((p) => !p)} disabled={total === 0}>
          {playing ? "Pause" : "Play"}
        </button>
        <button onClick={() => scrub(0)} disabled={index === 0}>
          Restart
        </button>
        <input
          aria-label="Replay position"
          type="range"
          min={0}
          max={total}
          value={index}
          onChange={(e) => scrub(Number(e.target.value))}
          style={{ flex: 1 }}
        />
        <span style={{ color: "#9aa4b2", fontVariantNumeric: "tabular-nums" }}>
          {index}/{total}
        </span>
      </footer>
    </div>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ padding: 24 }}>
      <header style={{ display: "flex", gap: 20, marginBottom: 16 }}>
        <strong>DispatchLab</strong>
        <a href="/" style={{ color: "#9aa4b2" }}>
          Live demo
        </a>
      </header>
      {children}
    </div>
  );
}
