# DispatchLab

Real-time delivery assignment and routing simulator.

Place an order, watch the system choose a driver, and close a road to make it reroute in real time. A comparison mode runs identical demand against a greedy baseline and a stronger batch assignment algorithm and reports the measured difference.

## Stack

- Go backend (REST + WebSocket), owns all simulation, routing, and matching state
- React, TypeScript, and Vite frontend
- PostgreSQL for sessions, events, snapshots, and benchmark results
- Docker Compose for the local stack

## Status

In active development. See the project board / implementation status doc for current phase.

## Local development

Setup and run instructions will land here once the first working slice of the app exists.
