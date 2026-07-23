# ADR 0001: Modular monolith over microservices

## Status

Accepted

## Context

The spec explicitly bans unnecessary microservices, message brokers, and orchestration infrastructure unless a measured requirement justifies them. DispatchLab's backend responsibilities (simulation, routing, matching, persistence, transport) are tightly coupled through the simulation's virtual clock and event stream.

## Decision

Build one Go binary (`cmd/server`) with clearly separated internal packages (`internal/domain`, `internal/simulation`, `internal/routing`, `internal/matching`, `internal/spatial`, `internal/replay`, `internal/service`, `internal/transport`, `internal/store`, `internal/telemetry`). Package boundaries are enforced by Go's internal visibility rules and by convention (no package reaches into another's private state), not by network calls.

## Consequences

- Deployment is a single container plus PostgreSQL — no service mesh, no broker.
- Package boundaries must be kept honest through code review discipline, since there is no network boundary forcing separation.
- If a genuine scaling need emerges (e.g. matching becomes a measured bottleneck under real load), that component can be extracted later with evidence, not speculatively.
