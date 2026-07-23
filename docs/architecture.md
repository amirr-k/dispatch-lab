# Architecture

## Plain-language flow

A visitor opens the page and immediately sees a small city with drivers already moving. Placing an order tells the backend where a pickup and drop-off are. The backend picks a driver, computes a route with A*, and streams positions to the browser over a WebSocket as the simulation clock advances. Closing a road invalidates any route crossing it and forces a recompute. Everything the visitor sees is a rendering of state the Go backend owns; the browser never computes positions or outcomes on its own.

## Components

```text
apps/web            React/TS/Vite frontend. Renders city, drivers, routes, event feed, metrics.
cmd/server           Go entry point. Wires HTTP, WS, simulation manager, store.
cmd/loadgen           Deterministic command generator for benchmark scenarios.
internal/domain      Core entities: City, Driver, Order, Simulation, Event, Snapshot.
internal/city        Graph generation (grid + perturbation) and validation.
internal/simulation  Actor-style event loop owning one simulation's mutable state.
internal/routing     A* pathfinding and route invalidation on closures.
internal/spatial     Grid index for nearest-candidate-driver queries.
internal/matching    Baseline (nearest idle) and optimized (batch min-cost) assignment.
internal/replay      Event log persistence and snapshot-based reconstruction.
internal/service     Application-level commands/queries bridging transport and simulation.
internal/transport   REST handlers and WebSocket protocol/fanout.
internal/store       PostgreSQL persistence.
internal/telemetry   Metrics, structured logs, traces.
```

## Design decisions

**Modular monolith.** One Go binary (`cmd/server`) with separated internal packages instead of microservices. Simulation, routing, and matching are tightly coupled through the virtual clock and event stream, and the project has no measured need for independent scaling or deployment of any one piece.

**Actor-style simulation ownership.** Each active simulation is owned by exactly one goroutine. All external interaction (place order, close road, pause) is a command sent through a bounded channel into that goroutine, which is the sole writer of its state and sole producer of its event stream. This avoids locking around simulation state entirely — the channel serializes access — and is verified with `go test -race` in CI.

**Deterministic virtual time.** Every simulation advances on an internal virtual clock driven by a discrete event queue, never `time.Now()`. Wall-clock time is used only to pace how fast a live viewer receives events (visualization speed) and to measure actual compute duration for benchmark numbers. Same seed and command sequence always produce the same event sequence, which comparison runs and replay both depend on.

**Spatial candidate filtering.** Matching queries a uniform grid bucketing drivers by map cell, expanding outward ring by ring from the pickup, instead of scanning every driver. The grid only narrows the candidate set — actual cost between a candidate and the pickup is still computed with A*.

**Event log with periodic snapshots.** Every event is appended to an ordered log keyed by `(simulation_id, sequence)`. Periodic snapshots let replay or resync start near a target point instead of from sequence zero. Showcase replays are retained permanently; anonymous guest runs may expire.

## Public-demo security

| Risk | Mitigation |
|---|---|
| Unbounded simulations exhaust memory/CPU | Guest token per session, per-token quota, global concurrent-simulation cap |
| Command flooding | Per-connection rate limiting, bounded command queue with rejection past capacity |
| Slow client stalls simulation progress | Simulation goroutine never blocks on client send; bounded outbound queue with drop-oldest overflow |
| Guessing another visitor's simulation ID | Unguessable UUIDs, scoped to the issuing guest token |
| Arbitrary code execution via crafted input | All input validated server-side against domain constraints; no user-supplied code is ever evaluated |

## WebSocket protocol

`GET /api/v1/simulations/{id}/stream`. A single envelope wraps every message:

```json
{ "schemaVersion": 1, "simulationId": "string", "sequence": 123, "virtualTime": 42.5, "type": "driver.position.updated", "payload": {} }
```

Server-to-client messages are simulation events (order placed/assigned, position updates, road closed, route recomputed) or a full `simulation.snapshot` on connect. Client-to-server messages are commands (`place_order`, `close_road`, `pause`, `resume`, `set_speed`, `resync`) and omit `sequence`/`virtualTime`, which the server assigns once applied.

Each connection has a bounded outbound queue. A client that falls behind gets its oldest queued events dropped and the connection closed with a `resync_required` notice; it reconnects or sends `resync` with its last known sequence to get a fresh snapshot plus the events after it.

## Persistence and replay

Every event a simulation emits is appended to an event log keyed by simulation ID and monotonic sequence number. Periodic snapshots let replay start near a target point instead of from sequence zero. A completed showcase run's events and snapshots are retained permanently for a stable `/replay/:id` URL; anonymous guest runs may expire.
