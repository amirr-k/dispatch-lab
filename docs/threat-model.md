# Threat model

DispatchLab is a public, no-login demo. The main risks are resource exhaustion and data leakage across anonymous sessions, not account compromise.

## Assets

- Server compute and memory (each active simulation costs goroutines, channels, and memory)
- PostgreSQL storage (event logs, snapshots, benchmark results)
- Bundled showcase replays (must stay stable and unmodifiable by visitors)

## Actors

- Anonymous visitor via the public web UI
- Anonymous visitor calling the REST/WebSocket API directly
- Operator (deploy-time only, no runtime admin surface in scope for v1)

## Threats and mitigations

| Threat | Mitigation |
|---|---|
| A visitor opens unbounded simulations to exhaust memory/CPU | Guest token issued per session; per-token simulation quota; global concurrent-simulation cap |
| A visitor floods commands (place_order spam, rapid road closures) | Per-connection rate limiting on the command channel; bounded command queue with rejection past capacity |
| A slow or malicious client stalls simulation progress by not draining its WebSocket | Simulation goroutine never blocks on client send; per-client outbound queue is bounded with drop-oldest overflow |
| A visitor reads another visitor's private (non-showcase) simulation by guessing its ID | Simulation IDs are unguessable (UUID); private simulations are scoped to the issuing guest token, not just ID secrecy |
| A visitor mutates or deletes a bundled showcase replay | Showcase replays are read-only, seeded at deploy time, not writable through any visitor-facing endpoint |
| Arbitrary code execution via crafted input (city generation params, order coordinates) | All simulation inputs are validated against domain constraints server-side; no user-supplied code or expressions are ever evaluated |
| Data races under concurrent goroutines corrupt simulation state | Actor model — a single goroutine owns each simulation's mutable state; `-race` run in CI on every build |
| Dependency on an external mapping API introduces a third-party outage or key-leak risk | No external map API; city geometry is generated and rendered entirely in-house |

## Out of scope for v1

- User accounts, authentication beyond guest tokens, and authorization roles
- Multi-tenant billing or usage-based access control
- Admin dashboard / runtime configuration surface
