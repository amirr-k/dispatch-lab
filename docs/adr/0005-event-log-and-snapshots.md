# ADR 0005: Append-only event log with periodic snapshots

## Status

Accepted

## Context

Replay (`/replay/:id`) must reconstruct exact past state, and disconnected clients must be able to resync without the simulation goroutine resending its entire history on every reconnect. Storing only the latest state would make replay impossible; storing every event with no snapshots would make reconstructing a late point in a long-running simulation expensive.

## Decision

Every event a simulation emits is appended to a durable, ordered log keyed by `(simulation_id, sequence)`. At a regular sequence interval, the simulation also persists a full state snapshot. Reconstruction for replay or resync starts from the nearest snapshot at or before the target sequence, then replays only the events after it, rather than starting from sequence zero.

## Consequences

- Replay correctness depends only on events being applied in order from a consistent snapshot, which is guaranteed because a single goroutine owns and emits them in sequence.
- Storage grows linearly with simulation activity; guest (non-showcase) simulations are eligible for expiry/cleanup, while bundled showcase runs are retained permanently as required by the product spec.
- A resuming WebSocket client sends its last known sequence; the server can serve a snapshot-plus-delta instead of full replay, keeping resync cheap regardless of how long the simulation has run.
