# ADR 0002: Actor-style ownership per simulation

## Status

Accepted

## Context

Multiple visitors can run simulations concurrently. Simulation state (driver positions, routes, order queues) is mutable and updated at high frequency. Sharing this state across goroutines with locks invites data races and makes the "no data races" non-negotiable hard to guarantee under review.

## Decision

Each active simulation is owned by exactly one goroutine. All external interaction — placing an order, closing a road, pausing, changing speed — is a command sent through a bounded channel into that goroutine. The goroutine is the only writer of that simulation's state and is the sole producer of the event stream describing what happened. WebSocket fanout, persistence, and telemetry are downstream readers of the event stream, never direct mutators.

## Consequences

- No mutex is needed around core simulation state; the channel serializes access.
- Slow consumers (a laggy WebSocket client) cannot block the simulation goroutine because they read from a separate bounded, drop-oldest queue fed by the event stream.
- Horizontal scaling is per-simulation (one goroutine per simulation, bounded by a global concurrency cap), not per-request.
- `go test -race` is run in CI to catch any accidental cross-goroutine mutation.
