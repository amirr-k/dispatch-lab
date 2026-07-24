# Observability

Everything here is standard library only. A single binary does not need a metrics client or a trace collector, and adding either would be infrastructure without a measured requirement.

## Structured logs

`log/slog`, JSON by default and text when `LOG_FORMAT=text`. Level comes from `LOG_LEVEL` (default `info`).

Every request logs one line with method, path, status, duration, and trace id:

```json
{"time":"...","level":"INFO","msg":"handled request","method":"POST",
 "path":"/api/v1/simulations/bf44.../orders","status":202,
 "duration_ms":0.6,"trace_id":"318fff3fc8b5f4ef1a1f8be9c1fa5642"}
```

## Traces

Spans are emitted as log records rather than shipped to a collector. Field names match OpenTelemetry's (`trace_id`, `span_id`, `parent_span_id`, `duration_ms`), so a collector can be introduced later without touching a single call site.

A trace follows a command the whole way:

| Span | Where |
|---|---|
| `http.request` | The HTTP handler. Puts the trace on the request context and returns it in the `X-Trace-Id` response header. |
| `simulation.apply` | The simulation actor goroutine. The command carries the trace across the channel hop, so this span is a child of the request that sent it. |
| `store.append_events`, `store.save_snapshot` | The recorder, when it flushes. |
| `ws.publish` | Sending one event to one browser. |

Route and match computation are measured as metrics rather than spans — they run thousands of times per run, and a span each would be noise.

Events carry the trace of the command that caused them, in an optional `traceId` on the wire envelope and a `trace_id` column in the event log. Events the simulation clock produced on its own carry none, since they belong to no request. That distinction is what makes the field useful: filtering the log by one trace id shows the request, the state change it caused, and the moment each resulting event reached a browser, and nothing else.

## Metrics

`GET /metrics`, Prometheus text exposition format, served outside `/api/v1` since it is operational rather than public API.

| Metric | Type | Meaning |
|---|---|---|
| `dispatchlab_active_simulations` | gauge | Simulations running in this process. |
| `dispatchlab_websocket_clients` | gauge | Stream connections currently open. |
| `dispatchlab_route_compute_duration_ms` | histogram | One A* route computation, for routes the simulation computes directly (assignment legs and reroutes). |
| `dispatchlab_match_compute_duration_ms` | histogram | One matching call — an immediate assignment, or one batch solve. Route computations inside matching are counted here, not in the route histogram. |
| `dispatchlab_dropped_updates_total` | counter | Events dropped because a consumer could not keep up: a slow WebSocket client, or a database slower than the simulation. |
| `dispatchlab_persistence_errors_total` | counter | Failed writes to the event or snapshot store. |
| `dispatchlab_events_persisted_total` | counter | Events written to the store. |
| `dispatchlab_snapshots_written_total` | counter | Snapshots written to the store. |

Histogram buckets run from 0.1ms to 1s, which covers a sub-millisecond A* run on a small city through a slow batch solve.

## Health

- `GET /health/live` — the process is up.
- `GET /health/ready` — the process is up *and*, when a database is attached, that database answers. Returns 503 otherwise, so a deployment does not route traffic to an instance that cannot persist anything.
