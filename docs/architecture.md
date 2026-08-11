# Architecture

## Flow

A visitor opens the page and sees a small city with drivers already moving. Placing an order tells the backend where a pickup and drop-off are. The backend picks a driver, computes a route with A*, and streams positions to the browser over a WebSocket as the simulation clock advances. Closing a road invalidates any route crossing it and forces a recompute. Everything the visitor sees is a rendering of state the Go backend owns; the browser never computes positions or outcomes on its own.

## Components

```text
apps/web             React/TS/Vite frontend. Renders city, drivers, routes, event feed, metrics.
cmd/server            Go entry point. Wires HTTP, WS, simulation manager, store.
cmd/loadgen           Deterministic command generator for benchmark scenarios.
cmd/evidence          Collects loadgen/benchmark output into benchmarks/results.
internal/domain       Core entities: City, Driver, Order, Simulation, Event, Snapshot.
internal/city         Graph generation (grid + perturbation) and validation.
internal/simulation   Actor-style event loop owning one simulation's mutable state.
internal/routing      A* pathfinding and route invalidation on closures.
internal/spatial      Grid index for nearest-candidate-driver queries.
internal/matching     Baseline (nearest idle) and optimized (batch min-cost) assignment.
internal/replay       Event log persistence and snapshot-based reconstruction.
internal/service      Application-level commands/queries bridging transport and simulation.
internal/transport    REST handlers and WebSocket protocol/fanout.
internal/store        PostgreSQL persistence, with an in-memory implementation of the same interface.
internal/telemetry    Metrics, structured logs, traces.
```

## Design decisions

**Modular monolith.** One Go binary (`cmd/server`) with separated internal packages instead of microservices. Simulation, routing, and matching are tightly coupled through the virtual clock and event stream, and nothing here needs independent scaling or deployment of any one piece.

**Actor-style simulation ownership.** Each active simulation is owned by exactly one goroutine. All external interaction (place order, close road, pause) is a command sent through a bounded channel into that goroutine, which is the sole writer of its state and sole producer of its event stream. This avoids locking around simulation state entirely — the channel serializes access — and is verified with `go test -race` in CI.

**Deterministic virtual time.** Every simulation advances on an internal virtual clock driven by a discrete event queue, never `time.Now()`. Wall-clock time is used only to pace how fast a live viewer receives events and to measure actual compute duration for benchmark numbers. The same seed and command sequence always produce the same event sequence, which comparison runs and replay both depend on.

**Spatial candidate filtering.** Matching queries a uniform grid bucketing drivers by map cell, expanding outward ring by ring from the pickup, instead of scanning every driver. The grid only narrows the candidate set — actual cost between a candidate and the pickup is still computed with A*.

**Event log with periodic snapshots.** Every event is appended to an ordered log keyed by `(simulation_id, sequence)`. Periodic snapshots let replay or resync start near a target point instead of from sequence zero. Showcase replays are retained permanently; anonymous guest runs expire after two hours.

**A recorder between the simulation and the hub.** Persistence is not another hub subscriber. The hub drops events for subscribers that fall behind — correct for a browser, wrong for an archive — so the recorder sits upstream of it, forwarding every event onward while batching a copy to the store. That way the event log is complete whenever the database can keep up, and when it cannot, the drop is counted rather than silent.

**Telemetry with no external dependency.** Metrics are exposed in Prometheus text format and traces are emitted as structured log records with OpenTelemetry-compatible field names, all from the standard library. A collector or metrics client could be swapped in later without changing a call site.

## WebSocket protocol

`GET /api/v1/simulations/{id}/stream`. A single envelope wraps every message:

```json
{ "schemaVersion": 1, "simulationId": "string", "sequence": 123, "virtualTime": 42.5, "type": "driver.position.updated", "payload": {}, "traceId": "optional" }
```

`traceId` is present only on events caused by a request, and identifies that request. Events the simulation clock produced on its own omit it.

Server-to-client messages are simulation events (order placed/assigned, position updates, road closed, route recomputed) or a full `simulation.snapshot` on connect. Client-to-server messages are commands (`place_order`, `close_road`, `pause`, `resume`, `set_speed`, `resync`) and omit `sequence`/`virtualTime`, which the server assigns once applied.

Each connection has a bounded outbound queue. A client that falls behind gets its oldest queued events dropped and the connection closed with a `resync_required` notice; it reconnects or sends `resync` with its last known sequence to get a fresh snapshot plus the events after it.

## Persistence and replay

Every event a simulation emits is appended to an event log keyed by simulation ID and monotonic sequence number, batched by a recorder that flushes on whichever comes first: 200 buffered events, or 500ms — a database transaction per animation frame isn't acceptable. Position updates are stored in full rather than summarized, since a replay without driver movement is a slideshow; what makes that affordable is that the simulation emits at most one position update per driver per virtual tick, so the rate is bounded by tick rate and driver count, not by render frame rate.

After every 500 persisted events the recorder asks the simulation for full state and stores a snapshot. `internal/replay` reconstructs state at any sequence by loading the newest snapshot at or before it and folding events forward — the property that makes this safe is tested directly: reconstructing from a mid-log snapshot must match folding the whole log from zero.

Persistence never blocks the simulation. The recorder's intake queue is bounded; if the database falls behind, events are dropped there and counted in `dispatchlab_dropped_updates_total` rather than allowed to stall the actor loop that feeds every live viewer. Appends are idempotent (`(simulation_id, sequence)` is the primary key with `on conflict do nothing`), so a retried flush can't duplicate events.

A run is ephemeral until `POST /api/v1/simulations/{id}/showcase` marks it retained and returns its `/replay/:id` URL. Anonymous runs are swept after two hours; showcase runs are kept, and the foreign key from a run to its session is `on delete set null` rather than cascade, which is why a saved replay outlives the session that created it. With no `DATABASE_URL` set, the server falls back to an in-memory store — everything still works, including replay, but only for the life of the process. The Postgres store and the in-memory one pass the same conformance suite (`internal/store/storetest`), so they can't quietly diverge.

## Observability

Structured logs (`log/slog`, JSON by default) carry method, path, status, duration, and a trace id on every request. Traces are spans emitted as log records rather than shipped to a collector, with OpenTelemetry-compatible field names (`trace_id`, `span_id`, `parent_span_id`, `duration_ms`) so a real collector could be introduced later without touching a call site. A trace follows one command from the HTTP handler (`http.request`), across the channel hop into the simulation actor (`simulation.apply`), through persistence (`store.append_events`, `store.save_snapshot`), to delivery (`ws.publish`).

`GET /metrics` serves Prometheus text exposition: active simulations, open WebSocket connections, route/match compute duration histograms, and counters for dropped updates, persistence errors, events persisted, and snapshots written. `GET /health/live` reports the process is up; `GET /health/ready` additionally checks the database when one is attached, so a deployment won't route traffic to an instance that can't persist anything.

## Security and limits

This is a public demo with no login. Identity is a guest token — 32 random bytes from a CSPRNG, issued by `POST /api/v1/guest-sessions` and sent as `Authorization: Bearer <token>` (or a `token` query parameter on the WebSocket handshake, since a browser can't set headers there). A simulation belongs to the session that created it, checked once inside the manager rather than per-handler so a new route can't skip it; refusals are 404, not 403, so a visitor can't discover that another visitor's simulation id exists.

| Limit | Value |
|---|---|
| Requests per caller | 10/s, burst 30, keyed by token or address |
| Simulations per session | 3 |
| Simulations per process | 50 |
| Orders per run | 200 |
| Drivers per run | 1–40, clamped server-side |
| Request body | 16 KiB |

Rate limiting is in-process and per-instance rather than backed by something like Redis, since the deployment is a single instance and a shared limiter would be infrastructure with no measured need; the process-wide simulation cap bounds the damage either way.

Any `POST` may carry an `Idempotency-Key`; a repeat of a key already seen for that session and path returns the original outcome instead of applying the command twice. `ALLOWED_ORIGINS` is a comma-separated allowlist — a permitted origin is reflected, never answered with a wildcard, and a disallowed one gets 403 before any work happens. Every error response is `{"error": {"code", "message"}}` with no stack traces or internal paths.

## Configuration

| Variable | Default | Effect |
|---|---|---|
| `ADDR` | `:8080` | Listen address. |
| `DATABASE_URL` | *(unset)* | Postgres connection string. Unset falls back to in-memory storage, and replays then last only as long as the process. |
| `ALLOWED_ORIGINS` | *(unset)* | Comma-separated browser origin allowlist. Unset is permissive, which is what lets a local dev server work against a clean clone. |
| `RATE_LIMIT_PER_SECOND` | `10` | Sustained request rate per caller. Non-positive disables limiting. |
| `RATE_LIMIT_BURST` | `30` | How much a caller may spend at once. |
| `LOG_FORMAT` | `json` | `text` for readable local output. |
| `LOG_LEVEL` | `info` | Standard slog levels. |
