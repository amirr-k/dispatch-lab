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

## Concurrency model

Each running simulation is owned by exactly one goroutine. Commands (place order, close road, pause, etc.) enter through a bounded channel and are applied sequentially against the virtual clock, never wall time. The simulation goroutine emits immutable events; WebSocket fanout and persistence are downstream consumers that never mutate simulation state directly. A slow or disconnected client cannot block the simulation — its queue has a bounded size and an explicit overflow policy (drop-oldest with a forced resync), and it can always request a fresh snapshot plus replay-from-sequence to catch up.

## WebSocket protocol

A single envelope type wraps every message: `{type, simulationId, sequence, payload}`. Server-to-client messages are simulation events (order placed, driver assigned, position tick, road closed, route recalculated) or control acks. Client-to-server messages are commands (place_order, close_road, pause, resume, set_speed, resync). Full contract: `spec/reference/api-and-persistence.md` in the planning repo.

## Persistence and replay

Every event a simulation emits is appended to an event log keyed by simulation ID and monotonic sequence number. Periodic snapshots let replay start near a target point instead of from sequence zero. A completed showcase run's events and snapshots are retained permanently for a stable `/replay/:id` URL; anonymous guest runs may expire.

## Benchmark methodology

Comparison runs replay an identical command sequence (same city, same seed, same order timing) through the baseline and optimized matchers. Because the simulation clock is deterministic, the only difference between two runs against the same scenario is the algorithm's own decisions and wall-clock compute time, which is measured, never estimated.
