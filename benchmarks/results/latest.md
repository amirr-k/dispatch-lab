# Benchmark results

- Commit: `d4c51473b45453d91462ddd5e0ec7a9d4aa2736e`
- Go: go1.26.5
- Machine: Apple M4 (darwin/arm64)
- Collected: 2026-07-26T06:32:21Z
- Summary method: nearest-rank percentiles over sorted samples; comparison metrics from RunComparison on the fair pickup metric (unassigned/pending scored at MaxVirtualTime-CreatedAt)

## Closure reroute (`recalculationMs`)

trials=50 p50=0.005ms p95=0.010ms p99=0.028ms mean=0.006ms avg affected routes=1.00

## Matching (40 drivers / 20 orders)

baseline p50=1.204ms p95=1.431ms; optimized p50=0.294ms p95=0.412ms (n=40)

## Routing short-hop

p50=2.0µs p95=3.0µs (n=500)

## Simulation throughput

2.88 events/tick with 40 drivers (n=100 ticks)

## Comparison suite (canonical 18 cells)

pickup times include MaxVirtualTime penalty for pending/unassignable orders; optimized uses adaptive min-batch/max-wait

| seed | demand | drivers | base avg pickup | opt avg pickup | base dist | opt dist | opt batch/imm |
|---|---|---|---|---|---|---|---|
| 42 | light | 4 | 23.58 | 22.68 | 9419 | 9000 | 22/15 |
| 42 | light | 12 | 5.55 | 7.55 | 5809 | 5809 | 0/12 |
| 42 | steady | 4 | 49.51 | 36.47 | 14448 | 12426 | 89/6 |
| 42 | steady | 12 | 6.15 | 8.15 | 9791 | 9791 | 0/20 |
| 42 | rush | 4 | 70.61 | 42.76 | 13873 | 10600 | 92/0 |
| 42 | rush | 12 | 20.98 | 16.51 | 15225 | 13120 | 25/1 |
| 7 | light | 4 | 12.42 | 14.42 | 7438 | 7438 | 8/29 |
| 7 | light | 12 | 4.23 | 6.23 | 5756 | 5756 | 0/12 |
| 7 | steady | 4 | 52.95 | 39.82 | 15867 | 13950 | 100/15 |
| 7 | steady | 12 | 9.98 | 11.98 | 12804 | 12804 | 0/20 |
| 7 | rush | 4 | 81.32 | 50.44 | 16624 | 12361 | 95/10 |
| 7 | rush | 12 | 26.93 | 16.86 | 18783 | 13514 | 25/0 |
| 99 | light | 4 | 18.03 | 20.56 | 8663 | 8823 | 28/17 |
| 99 | light | 12 | 4.86 | 6.86 | 6236 | 6236 | 0/12 |
| 99 | steady | 4 | 52.79 | 35.80 | 16837 | 13142 | 91/12 |
| 99 | steady | 12 | 8.09 | 10.09 | 12861 | 12861 | 0/20 |
| 99 | rush | 4 | 79.02 | 53.27 | 17150 | 13029 | 103/3 |
| 99 | rush | 12 | 22.76 | 15.36 | 16395 | 14107 | 21/2 |
