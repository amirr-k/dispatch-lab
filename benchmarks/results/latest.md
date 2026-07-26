# Benchmark results

- Commit: `52a1ee8c6daa2aad2a671cb7bb80206f605ff1f4`
- Go: go1.26.5
- Machine: Apple M4 (darwin/arm64)
- Collected: 2026-07-26T21:09:05Z
- Summary method: nearest-rank percentiles over sorted samples; comparison metrics from RunComparison on the fair pickup metric (unassigned/pending scored at MaxVirtualTime-CreatedAt)

## Closure reroute (`recalculationMs`)

trials=50 p50=0.005ms p95=0.009ms p99=0.055ms mean=0.006ms avg affected routes=1.00

## Matching (40 drivers / 20 orders)

baseline p50=1.179ms p95=1.306ms; optimized p50=1.265ms p95=1.335ms (n=40)

## Routing short-hop

p50=2.0µs p95=3.0µs (n=500)

## Simulation throughput

2.88 events/tick with 40 drivers (n=100 ticks)

## Comparison suite (canonical 18 cells)

pickup times include MaxVirtualTime penalty for pending/unassignable orders; optimized uses adaptive min-batch/max-wait

| seed | demand | drivers | base avg pickup | opt avg pickup | base dist | opt dist | opt batch/imm |
|---|---|---|---|---|---|---|---|
| 42 | light | 4 | 23.58 | 20.68 | 9419 | 9000 | 18/17 |
| 42 | light | 12 | 5.55 | 5.55 | 5809 | 5809 | 0/12 |
| 42 | steady | 4 | 49.51 | 34.20 | 14448 | 12441 | 86/15 |
| 42 | steady | 12 | 6.15 | 6.15 | 9791 | 9791 | 0/20 |
| 42 | rush | 4 | 70.61 | 47.09 | 13873 | 10464 | 79/4 |
| 42 | rush | 12 | 20.98 | 19.69 | 15225 | 14579 | 30/13 |
| 7 | light | 4 | 12.42 | 12.42 | 7438 | 7438 | 4/25 |
| 7 | light | 12 | 4.23 | 4.23 | 5756 | 5756 | 0/12 |
| 7 | steady | 4 | 52.95 | 37.82 | 15867 | 13950 | 98/15 |
| 7 | steady | 12 | 9.98 | 9.98 | 12804 | 12804 | 0/20 |
| 7 | rush | 4 | 81.32 | 61.91 | 16624 | 13570 | 116/5 |
| 7 | rush | 12 | 26.93 | 22.02 | 18783 | 16313 | 29/18 |
| 99 | light | 4 | 18.03 | 18.66 | 8663 | 8850 | 25/15 |
| 99 | light | 12 | 4.86 | 4.86 | 6236 | 6236 | 0/12 |
| 99 | steady | 4 | 52.79 | 33.07 | 16837 | 13004 | 87/10 |
| 99 | steady | 12 | 8.09 | 8.09 | 12861 | 12861 | 0/20 |
| 99 | rush | 4 | 79.02 | 51.49 | 17150 | 12535 | 97/15 |
| 99 | rush | 12 | 22.76 | 18.57 | 16395 | 14258 | 35/12 |

## Loadgen (against a live server + real PostgreSQL)

WS sequence ↔ persisted event reconcile: `{
    "simulationId": "b8116fdcc4fe9d29",
    "wsSequences": 372,
    "persistedSequences": 372,
    "ok": true
  }`

Concurrent guest simulations: `{
    "sessionsIssued": 8,
    "sessionFailures": 0,
    "simulationsCreated": 8,
    "simulationFailures": 0,
    "ordersAttempted": 240,
    "orderStatusCounts": {
      "202": 240
    },
    "orderLatencySuccessful": {
      "count": 240,
      "min": 207833,
      "p50": 1935250,
      "p95": 3230917,
      "p99": 3663875,
      "max": 3787500
    },
    "sessionIssueLatency": {
      "count": 8,
      "min": 5331708,
      "p50": 14182416,
      "p95": 14809833,
      "p99": 14809833,
      "max": 17952584
    },
    "simulationCreateLatency": {
      "count": 8,
      "min": 5237750,
      "p50": 6244417,
      "p95": 7651583,
      "p99": 7651583,
      "max": 9137000
    },
    "ordersPerSecond": 16
  }`

WebSocket update throughput: `{
    "streamsConnected": 8,
    "streamFailures": 0,
    "connectLatency": {
      "count": 8,
      "min": 1842500,
      "p50": 2323083,
      "p95": 8090792,
      "p99": 8090792,
      "max": 8143292
    },
    "totalEvents": 3900,
    "eventsPerSecond": 260,
    "eventsPerStreamPerSecond": 32.5
  }`

