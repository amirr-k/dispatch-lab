# DispatchLab

Real-time delivery assignment and routing simulator.

Place an order, watch the system choose a driver, and close a road to make it reroute in real time. A comparison mode runs identical demand against a greedy baseline and a stronger batch assignment algorithm and reports the measured difference.

## Stack

- Go backend (REST + WebSocket), owns all simulation, routing, and matching state
- React, TypeScript, and Vite frontend
- PostgreSQL (Neon) for sessions, events, snapshots, and benchmark results, with an in-memory fallback for local development
- Render (backend) and GitHub Pages (frontend), deployed on every push to main via GitHub Actions

## Status

Deployed and working end to end: live simulation, road closures, replay, and the baseline-vs-optimized comparison mode. Architecture notes, the API schema, and measured evidence live outside this public repo.

## Local development

Backend (Go 1.26+):

```
go run ./cmd/server
```

Runs on `:8080` by default. With no `DATABASE_URL` set it falls back to an in-memory store, so nothing else needs to be running. To use Postgres instead, set `DATABASE_URL` to a connection string; migrations apply automatically on startup. Other environment variables: `ADDR`, `ALLOWED_ORIGINS`, `LOG_LEVEL`, `LOG_FORMAT`, `RATE_LIMIT_PER_SECOND`, `RATE_LIMIT_BURST`.

Frontend (Node 22+):

```
cd apps/web
npm install
npm run dev
```

Defaults to talking to a backend at `http://localhost:8080`; override with `VITE_API_URL`.

Tests: `go test ./...` for the backend, `npm test` and `npm run test:e2e` (Playwright) for the frontend.
