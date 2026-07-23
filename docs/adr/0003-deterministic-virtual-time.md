# ADR 0003: Deterministic virtual clock, never wall time

## Status

Accepted

## Context

Comparison runs (baseline vs. optimized matching) and replay must produce identical event sequences given the same seed and command sequence. Wall-clock time is nondeterministic across runs and machines, and would make comparisons unreproducible and replay non-authoritative.

## Decision

Every simulation advances on an internal virtual clock driven by a discrete event queue ordered by (virtual timestamp, insertion sequence). Wall-clock time is used only for two purposes that never affect simulation outcomes: throttling how fast events are flushed to a live WebSocket viewer (visualization speed), and measuring actual compute duration for reported benchmark numbers (e.g. "assignment computed in Xms"). No simulation decision (routing, matching, event ordering) ever reads `time.Now()`.

## Consequences

- Same seed + same command sequence ⇒ byte-identical event sequence, verified by a determinism test in Phase 1.
- A "simulation speed" control in the UI only changes how fast virtual time is advanced and flushed, not what happens.
- Compute-time measurements (used in benchmarks and the assignment-time UI card) are wall-clock timings of a deterministic computation, not part of the deterministic state itself, and are kept clearly separate from replay data.
