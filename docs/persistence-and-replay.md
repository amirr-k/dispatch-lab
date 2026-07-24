# Persistence and replay

## What gets stored

| Table | Holds |
|---|---|
| `simulations` | One row per run: seed, driver count, strategy, creation time, and whether it is a retained showcase. |
| `simulation_events` | The append-only event log, keyed by `(simulation_id, sequence)`. |
| `simulation_snapshots` | Periodic full-state snapshots, keyed the same way. |
| `comparisons` | Whole algorithm-comparison results, so a published number stays traceable to the run behind it. |

Event and snapshot payloads are `jsonb`. The store never interprets them, which means a new event type needs no migration.

Guest sessions are listed in the spec's persistence rules but have no table yet — they arrive with the guest tokens and quotas in phase 6, and a table with no writer would only be dead schema.

## Write policy

The rule that shapes this layer is that a database transaction per animation frame is not acceptable. So:

- **Events are batched.** Each simulation gets a recorder sitting between it and the WebSocket hub. It forwards every event onward unchanged and buffers a copy, flushing on whichever comes first: 200 buffered events, or 500ms. One flush is one round trip.
- **Position updates are stored, not summarised.** They are the run — a replay without driver movement is a slideshow. What makes them affordable is that the simulation emits at most one per driver per virtual tick, so their rate is bounded by the tick rate and driver count, not by the frame rate. Batching turns a tick's worth into a fraction of a round trip.
- **Snapshots are periodic.** After every 500 persisted events the recorder asks the simulation for full state and stores it. Marking a run as a showcase writes one final snapshot as well, so the stored history ends on the state the visitor actually saw.
- **Persistence never blocks the simulation.** The recorder's intake queue is bounded. If the database falls behind, events are dropped there and counted in `dispatchlab_dropped_updates_total` rather than allowed to stall the actor loop that feeds every live viewer. A failed write increments `dispatchlab_persistence_errors_total` and is logged; it is not retried, because the simulation is the source of truth for a live viewer and blocking on a retry would degrade the demo to protect the archive.

Appends are idempotent. `(simulation_id, sequence)` is the primary key and inserts use `on conflict do nothing`, so a retried flush containing already-written events cannot duplicate them.

## Reconstruction

`internal/replay` folds an event log onto a snapshot to produce state at any sequence:

1. Load the newest snapshot at or before the target sequence. If there is none, start empty.
2. Apply every event after it, up to the target.

That is the only reason snapshots exist: without them, scrubbing to the end of a long run would mean folding the entire log. The property that makes them safe to use is tested directly — reconstructing from a mid-log snapshot must produce the identical state to folding the whole log from zero, and a full fold must equal the state the live simulation itself holds.

A snapshot carries the simulation clock as of the moment it was taken. When a run is idling — no orders, every driver parked — virtual time advances without emitting events, so a snapshot's virtual time can be ahead of the last event's. It never moves backwards as the sequence advances.

## Replay URLs

`GET /api/v1/simulations/{id}/replay` returns the log; `?at=<sequence>` returns reconstructed state at one point instead. The `/replay/:id` page loads the log once and folds events in the browser, so scrubbing is instant and does not re-query.

A run is ephemeral until `POST /api/v1/simulations/{id}/showcase` marks it retained and returns its `/replay/:id` URL. Anonymous runs are expected to be pruned; showcase runs are kept.

## Running without a database

With no `DATABASE_URL`, the server falls back to an in-memory store and logs a warning. Everything works, including replay, but only for the life of the process. The Postgres store and the in-memory one pass the same conformance suite (`internal/store/storetest`), so they cannot quietly diverge.

## Migrations

SQL lives in `db/migrations/` as paired `.up.sql`/`.down.sql` files and is embedded in the binary, so the deployment image needs no migration tool. The server applies anything unapplied on start; each migration and its version row commit in a single transaction, so a failure halfway leaves the schema where it was. Every statement is written to be safe from an empty database, and the down files are exercised by a rollback-then-reapply test rather than assumed to work.
