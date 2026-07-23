# ADR 0004: Spatial grid for candidate-driver filtering

## Status

Accepted

## Context

Matching (both baseline and optimized) needs to find drivers near a pickup point. A naive scan of every driver on every order is O(n) per match and gets worse as the driver count and order rate grow, which matters for the batch optimized matcher evaluating many order-driver pairs at once.

## Decision

Maintain a uniform spatial grid over the city's coordinate space. Each cell holds the set of driver IDs currently located in it. A candidate query for a pickup point expands outward ring by ring from the pickup's cell until enough candidates are found or the city bound is reached, instead of scanning every driver. The grid is updated incrementally as drivers move (remove from old cell, insert into new cell) rather than rebuilt per query.

## Consequences

- Candidate lookup is close to O(1) average case for a roughly uniform driver distribution, instead of O(n).
- The grid only narrows the candidate set; actual route cost between a candidate driver and the pickup is still computed with A*, so the grid must never be the source of routing truth — just filtering.
- Cell size is a tunable constant chosen relative to city scale; too small increases per-move update churn, too large degrades toward a full scan.
