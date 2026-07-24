# Security and limits

This is a public demo with no login. Everything below exists because anyone on the internet can send it whatever they like.

## Identity

There is exactly one kind of identity: a **guest token**, issued by `POST /api/v1/guest-sessions`. It is 32 random bytes, base64url-encoded, and it is the only thing separating one visitor's runs from another's — so it is generated from a CSPRNG and never derived from anything guessable.

Clients send it as `Authorization: Bearer <token>`. The WebSocket stream takes it as a `token` query parameter instead, because a browser cannot set headers on a WebSocket handshake.

A session lasts an hour and is extended on use (at most once a minute, to avoid writing to the row on every request), so an active visitor is never cut off mid-demo while an abandoned session ages out.

## What a session may reach

A simulation belongs to the session that created it. The check lives inside the manager's `submit`, not in each handler, so a new command route cannot accidentally skip it.

Refusals are reported as **404, not 403**: a visitor should not be able to discover that another visitor's simulation id exists.

| Resource | Who can reach it |
|---|---|
| A run, its commands, and its stream | Only the session that created it |
| An unsaved run's replay | Only the session that created it |
| A saved (showcase) run's replay | Anyone with the link — that is what "stable replay URL" means |
| Server-provisioned showcase runs | Anyone; they belong to no session |

## Limits

| Limit | Value | Why |
|---|---|---|
| Requests per caller | 10/s, burst 30 | Keyed by token, or by client address when there is none, so an unauthenticated flood is bounded too |
| Simulations per session | 3 | One visitor cannot take more than their share |
| Simulations per process | 50 | Many visitors between them cannot exhaust the machine |
| Orders per run | 200 | Holding down the mouse cannot grow one run's state without limit |
| Drivers per run | 1–40, clamped | Server-side; the request's number is never trusted |
| Request body | 16 KiB | Every command here is a handful of fields |
| Idempotency key | 200 chars | It is client-supplied and used as a cache key |

Rate limiting is in-process and per-instance. A shared limiter would mean Redis, and the spec bans distributed infrastructure introduced without a measured requirement; the process-wide simulation cap bounds the damage either way.

## Retries

Any `POST` may carry an `Idempotency-Key`. A repeat of a key already seen **for that session and path** returns the original outcome and sets `Idempotent-Replay: true`, rather than applying the command twice. Keys are scoped to the session, so one visitor cannot replay or collide with another's command. Only successful outcomes are remembered — a failure the client can fix by retrying is not pinned to its first result.

The cache holds 4096 entries for 10 minutes, evicting expired entries and then the oldest. An unbounded cache keyed by client-supplied strings is a memory exhaustion vector.

## Origins

`ALLOWED_ORIGINS` is a comma-separated allowlist. A permitted origin is **reflected**, never answered with a wildcard, so the policy still holds once credentials or a real allowlist are in play. A disallowed origin gets 403 before any work is done. A request with no `Origin` is not a browser cross-origin request at all — curl, a health probe — and is allowed.

An empty allowlist is permissive. That is the right default for a clone-and-run demo where the frontend is a Vite dev server on another port, and deployment sets the variable.

The WebSocket upgrader defers to this same middleware rather than using gorilla's default origin check, which compares against the `Host` header and would reject the deployed frontend — it is served from a different origin than the API.

## Timeouts and buffers

- **Request headers**: 10s, so a slow-loris connection cannot hold a goroutine.
- **Database statements**: 10s, enforced by Postgres itself, so a query that escapes its context deadline still cannot pin a connection. The pool is capped at 10 connections, recycled hourly.
- **Command queue**: bounded per simulation; past capacity the API returns 503 rather than stalling a request on simulation progress.
- **Outbound stream queue**: bounded per client. A client that falls behind has events dropped and counted in `dispatchlab_dropped_updates_total`; the simulation is never blocked by a slow viewer.
- **Persistence queue**: bounded. If the database falls behind, events are dropped there and counted, rather than stalling the actor loop that feeds every live viewer.
- **Shutdown**: SIGINT/SIGTERM stops accepting, drains in-flight requests for up to 10s, then waits up to 5s for recorders to flush so the tail of an event log is not lost.

## Retention

Anonymous runs are kept for 2 hours, then deleted along with their events and snapshots; expired sessions go too. A sweep runs every 5 minutes and once at startup. Showcase runs are never swept — when their session expires, the foreign key sets the owner to null rather than cascading the delete, which is precisely why a saved replay URL survives its creator's session.

## Errors

Every failure is `{"error": {"code", "message"}}`. No stack traces, no driver errors, no internal paths — there is a test asserting the response body contains none of them. The `X-Trace-Id` response header is what makes a report about one bad request actionable without leaking anything.

## Not allowed, by construction

- No user-supplied maps. The city is generated from a seed; there is no upload path.
- No user-supplied code. Nothing in a request is ever evaluated.
- No unbounded counts. Driver and order counts are clamped server-side.
- No cross-session access, as above.
