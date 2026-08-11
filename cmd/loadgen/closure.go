package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsFrame is one decoded event frame off a simulation's WebSocket stream.
// causationId is what lets this benchmark determine exactly which events one
// road-closure command produced, rather than guessing from timing or type.
type wsFrame struct {
	Sequence    int             `json:"sequence"`
	Type        string          `json:"type"`
	CausationID string          `json:"causationId"`
	Payload     json.RawMessage `json:"payload"`
}

// streamReader continuously drains one WebSocket connection onto a buffered
// channel and separately tracks whether the sequence stream it saw was
// strictly increasing (ignoring snapshot frames, which intentionally reuse
// the last sequence). This connection-wide check is what "duplicate or
// out-of-order" events means for this benchmark: a real protocol violation,
// not a per-trial timing artifact.
type streamReader struct {
	frames chan wsFrame
	done   chan struct{}

	mu          sync.Mutex
	lastSeq     int
	sequenceErr string
}

func newStreamReader(conn *websocket.Conn) *streamReader {
	r := &streamReader{frames: make(chan wsFrame, 4096), done: make(chan struct{})}
	go r.run(conn)
	return r
}

func (r *streamReader) run(conn *websocket.Conn) {
	defer close(r.done)
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var f wsFrame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.Type != "simulation.snapshot" {
			r.mu.Lock()
			if f.Sequence <= r.lastSeq && r.sequenceErr == "" {
				r.sequenceErr = fmt.Sprintf("sequence %d arrived after/at last seen sequence %d (type=%s)", f.Sequence, r.lastSeq, f.Type)
			}
			if f.Sequence > r.lastSeq {
				r.lastSeq = f.Sequence
			}
			r.mu.Unlock()
		}
		select {
		case r.frames <- f:
		default:
			// the channel is generously sized for one trial's traffic; a
			// full buffer means frames are backing up, not that any should
			// be silently dropped, so block instead of discarding.
			r.frames <- f
		}
	}
}

func (r *streamReader) sequenceError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sequenceErr
}

// closureLatencyConfig configures the end-to-end road-closure benchmark.
type closureLatencyConfig struct {
	addr          string
	baseSeed      int64
	seedCount     int
	trialsPerSeed int
	warmupTrials  int
	drivers       int
	orders        int
	concurrency   int
	timeout       time.Duration
}

func (cfg closureLatencyConfig) seeds() []int64 {
	seeds := make([]int64, cfg.seedCount)
	for i := range seeds {
		seeds[i] = cfg.baseSeed + int64(i)
	}
	return seeds
}

// closureTrialResult is one trial's raw measurement, kept in full in the
// report so results are never summarized without the underlying samples
// being available to check them against.
type closureTrialResult struct {
	Seed           int64   `json:"seed"`
	Success        bool    `json:"success"`
	NoOp           bool    `json:"noOp"`
	WSTimeout      bool    `json:"wsTimeout"`
	Error          string  `json:"error,omitempty"`
	AffectedRoutes int     `json:"affectedRoutes"`
	TotalMs        float64 `json:"totalMs"`
	HTTPAckMs      float64 `json:"httpAckMs"`
	RecalcMs       float64 `json:"recalcMs"`
	ResidualMs     float64 `json:"residualMs"`
	Verified       bool    `json:"persistedInPostgres"`
	VerifyError    string  `json:"verifyError,omitempty"`
	SequenceError  bool    `json:"sequenceProtocolError"`
}

// closureLatencyReport is the full result of the end-to-end road-closure
// rerouting benchmark: one client-side timer spanning HTTP transport and
// parsing, the simulation's command-queue wait, affected-driver detection,
// A* recomputation, event creation/serialization, and WebSocket delivery -
// everything up to and including the moment the causationId-matched
// road.closed frame is fully received. Postgres persistence happens on a
// separate, asynchronous path and is deliberately not part of this timer;
// PersistenceVerified below confirms it happens, not how long it takes.
type closureLatencyReport struct {
	Addr                   string  `json:"addr"`
	Seeds                  []int64 `json:"seeds"`
	TrialsPerSeed          int     `json:"trialsPerSeed"`
	WarmupTrials           int     `json:"warmupTrials"`
	Drivers                int     `json:"drivers"`
	Orders                 int     `json:"orders"`
	TotalTrials            int     `json:"totalTrials"`
	SuccessfulTrials       int     `json:"successfulTrials"`
	FailedTrials           int     `json:"failedTrials"`
	NoOpTrials             int     `json:"noOpTrials"`
	WSTimeouts             int     `json:"wsTimeouts"`
	VerificationFailures   int     `json:"verificationFailures"`
	SequenceProtocolErrors int     `json:"sequenceProtocolErrors"`

	// EndToEnd covers only successful, non-no-op, non-timeout trials - the
	// number that actually represents rerouting latency.
	EndToEnd summary `json:"endToEndLatency"`
	HTTPAck  summary `json:"httpAckLatency"`
	Recalc   summary `json:"affectedRouteDetectionAndRoutingLatency"`
	Residual summary `json:"queueEventEmissionAndDeliveryLatencyResidual"`

	AffectedDriversPerClosure intSummary `json:"affectedDriversPerClosure"`

	FailureSamples []string             `json:"failureSamples,omitempty"`
	Trials         []closureTrialResult `json:"trials"`
}

func runClosureLatency(ctx context.Context, cfg closureLatencyConfig) closureLatencyReport {
	seeds := cfg.seeds()

	if cfg.warmupTrials > 0 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, cfg.concurrency)
		for i := 0; i < cfg.warmupTrials; i++ {
			seed := seeds[i%len(seeds)]
			wg.Add(1)
			sem <- struct{}{}
			go func(seed int64) {
				defer wg.Done()
				defer func() { <-sem }()
				runClosureTrial(ctx, cfg, seed, false)
			}(seed)
		}
		wg.Wait()
	}

	type task struct{ seed int64 }
	var tasks []task
	for _, seed := range seeds {
		for i := 0; i < cfg.trialsPerSeed; i++ {
			tasks = append(tasks, task{seed: seed})
		}
	}

	results := make([]closureTrialResult, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.concurrency)
	for i, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, seed int64) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runClosureTrial(ctx, cfg, seed, true)
		}(i, t.seed)
	}
	wg.Wait()

	return summarizeClosureLatency(cfg, results)
}

// runClosureTrial exercises exactly one road closure end to end against a
// freshly created simulation: fresh so an earlier trial's closure can never
// bias this one's routes or graph state. verifyPersistence is skipped for
// warmup trials to keep warmup fast; it never affects the timed measurement
// either way.
func runClosureTrial(ctx context.Context, cfg closureLatencyConfig, seed int64, verifyPersistence bool) closureTrialResult {
	result := closureTrialResult{Seed: seed}

	c := newClient(cfg.addr)
	if _, err := c.issueSession(ctx); err != nil {
		result.Error = "session: " + err.Error()
		return result
	}

	simID, _, err := c.createSimulationSeeded(ctx, seed, cfg.drivers)
	if err != nil {
		result.Error = "create: " + err.Error()
		return result
	}

	conn, err := c.dialStream(ctx, simID)
	if err != nil {
		result.Error = "stream: " + err.Error()
		return result
	}
	defer conn.Close()

	// the first frame is always the current snapshot.
	_, data, err := conn.ReadMessage()
	if err != nil {
		result.Error = "initial snapshot: " + err.Error()
		return result
	}
	var initial wsFrame
	if err := json.Unmarshal(data, &initial); err != nil {
		result.Error = "decode initial snapshot: " + err.Error()
		return result
	}
	var initialPayload snapshotPayload
	if err := json.Unmarshal(initial.Payload, &initialPayload); err != nil {
		result.Error = "decode initial snapshot payload: " + err.Error()
		return result
	}
	if len(initialPayload.Nodes) < 2 {
		result.Error = "simulation has too few nodes to place orders"
		return result
	}

	reader := newStreamReader(conn)

	nodeIDs := make([]string, len(initialPayload.Nodes))
	for i, n := range initialPayload.Nodes {
		nodeIDs[i] = n.ID
	}
	half := len(nodeIDs) / 2
	for i := 0; i < cfg.orders; i++ {
		pickup := nodeIDs[i%len(nodeIDs)]
		destination := nodeIDs[(i+half)%len(nodeIDs)]
		if pickup == destination {
			destination = nodeIDs[(i+half+1)%len(nodeIDs)]
		}
		if _, _, err := c.placeOrder(ctx, simID, pickup, destination); err != nil {
			result.Error = "place order: " + err.Error()
			return result
		}
	}

	// orders are matched asynchronously on the simulation's own goroutine;
	// poll briefly for routes to appear rather than guessing a fixed delay.
	var snap snapshotPayload
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, _, err := c.getSnapshot(ctx, simID)
		if err != nil {
			result.Error = "poll snapshot: " + err.Error()
			return result
		}
		snap = s.Payload
		if activeDriverEdge(snap) != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	target := activeDriverEdge(snap)
	if target == nil {
		result.Error = "no active driver route found before timing out"
		return result
	}
	edgeID, ok := snap.edgeID(target.from, target.to)
	if !ok {
		result.Error = fmt.Sprintf("no edge found for %s->%s", target.from, target.to)
		return result
	}

	// --- timed section: the metric this benchmark exists to produce ---
	t0 := time.Now()
	commandID, httpAck, err := c.closeRoad(ctx, simID, edgeID)
	if err != nil {
		result.Error = "close road: " + err.Error()
		return result
	}
	result.HTTPAckMs = msOf(httpAck)

	var related []wsFrame
	var closedPayload struct {
		AffectedRoutes int     `json:"affectedRoutes"`
		RecalcMs       float64 `json:"recalculationMs"`
	}
	found := false
	timeout := time.After(cfg.timeout)
waitLoop:
	for {
		select {
		case f := <-reader.frames:
			if f.CausationID != "" && f.CausationID != commandID {
				result.Error = fmt.Sprintf("observed causationId %q that belongs to no command this trial issued", f.CausationID)
				return result
			}
			if f.CausationID == commandID {
				related = append(related, f)
				if f.Type == "road.closed" {
					if err := json.Unmarshal(f.Payload, &closedPayload); err != nil {
						result.Error = "decode road.closed payload: " + err.Error()
						return result
					}
					found = true
					break waitLoop
				}
			}
		case <-timeout:
			result.WSTimeout = true
			break waitLoop
		case <-ctx.Done():
			result.Error = "context canceled while waiting for road.closed"
			return result
		}
	}
	t1 := time.Now()
	// --- end timed section ---

	if !found {
		if result.Error == "" {
			result.Error = "timed out waiting for a causationId-matched road.closed event"
		}
		return result
	}

	result.TotalMs = msOf(t1.Sub(t0))
	result.RecalcMs = closedPayload.RecalcMs
	result.ResidualMs = result.TotalMs - result.HTTPAckMs - result.RecalcMs
	result.AffectedRoutes = closedPayload.AffectedRoutes
	result.NoOp = closedPayload.AffectedRoutes == 0

	invalidated := 0
	for _, f := range related {
		if f.Type == "route.invalidated" {
			invalidated++
		}
	}
	if invalidated != closedPayload.AffectedRoutes {
		result.Error = fmt.Sprintf("affectedRoutes=%d but observed %d route.invalidated events with this causationId",
			closedPayload.AffectedRoutes, invalidated)
		return result
	}

	if seqErr := reader.sequenceError(); seqErr != "" {
		result.SequenceError = true
		result.Error = "sequence protocol violation: " + seqErr
		return result
	}

	result.Success = true

	if verifyPersistence {
		result.Verified, result.VerifyError = verifyClosurePersisted(ctx, c, simID, initial.Sequence, related)
	}

	return result
}

type edgeRef struct{ from, to string }

// activeDriverEdge picks the driver with the most remaining route hops -
// the one least likely to have already finished by the time the closure
// lands - and returns the next edge on its path, mirroring the live demo's
// own road-closure preset, which reads a driver's real next edge rather than
// guessing one from timing.
func activeDriverEdge(p snapshotPayload) *edgeRef {
	bestRemaining := 0
	var best *edgeRef
	for _, d := range p.Drivers {
		remaining := len(d.Route) - d.RouteIndex - 1
		if remaining < 1 {
			continue
		}
		if remaining > bestRemaining {
			bestRemaining = remaining
			best = &edgeRef{from: d.Route[d.RouteIndex], to: d.Route[d.RouteIndex+1]}
		}
	}
	return best
}

// verifyClosurePersisted confirms the road.closed event's sequence number -
// and every causationId-matched event's sequence number - eventually
// appears in the persisted event log. This runs after the timed section, so
// polling delay here never contaminates the latency measurement; it exists
// purely to confirm persistence happens, since it happens asynchronously and
// is not part of the critical path being measured.
func verifyClosurePersisted(ctx context.Context, c *client, simID string, snapshotSeq int, related []wsFrame) (bool, string) {
	want := make(map[int]bool, len(related))
	for _, f := range related {
		want[f.Sequence] = true
	}
	if len(want) == 0 {
		return false, "no related events to verify"
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		persisted, err := c.fetchReplaySequences(ctx, simID)
		if err != nil {
			if time.Now().After(deadline) {
				return false, "fetch replay: " + err.Error()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		missing := 0
		for seq := range want {
			if !persisted[seq] {
				missing++
			}
		}
		if missing == 0 {
			return true, ""
		}
		if time.Now().After(deadline) {
			return false, fmt.Sprintf("%d of %d causationId-matched sequences never appeared in the persisted log", missing, len(want))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func msOf(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func summarizeClosureLatency(cfg closureLatencyConfig, results []closureTrialResult) closureLatencyReport {
	report := closureLatencyReport{
		Addr:          cfg.addr,
		Seeds:         cfg.seeds(),
		TrialsPerSeed: cfg.trialsPerSeed,
		WarmupTrials:  cfg.warmupTrials,
		Drivers:       cfg.drivers,
		Orders:        cfg.orders,
		TotalTrials:   len(results),
		Trials:        results,
	}

	e2e := &latencies{}
	httpAck := &latencies{}
	recalc := &latencies{}
	residual := &latencies{}
	var affected []int

	for _, r := range results {
		if r.WSTimeout {
			report.WSTimeouts++
		}
		if r.SequenceError {
			report.SequenceProtocolErrors++
		}
		if !r.Success {
			report.FailedTrials++
			if r.Error != "" && len(report.FailureSamples) < 10 {
				report.FailureSamples = append(report.FailureSamples, fmt.Sprintf("seed=%d: %s", r.Seed, r.Error))
			}
			continue
		}
		report.SuccessfulTrials++
		if r.NoOp {
			report.NoOpTrials++
			continue
		}
		affected = append(affected, r.AffectedRoutes)
		e2e.add(time.Duration(r.TotalMs * float64(time.Millisecond)))
		httpAck.add(time.Duration(r.HTTPAckMs * float64(time.Millisecond)))
		recalc.add(time.Duration(r.RecalcMs * float64(time.Millisecond)))
		residual.add(time.Duration(r.ResidualMs * float64(time.Millisecond)))
		if !r.Verified {
			report.VerificationFailures++
			if r.VerifyError != "" && len(report.FailureSamples) < 10 {
				report.FailureSamples = append(report.FailureSamples, fmt.Sprintf("seed=%d verify: %s", r.Seed, r.VerifyError))
			}
		}
	}

	report.EndToEnd = e2e.summarize()
	report.HTTPAck = httpAck.summarize()
	report.Recalc = recalc.summarize()
	report.Residual = residual.summarize()
	report.AffectedDriversPerClosure = summarizeInts(affected)

	return report
}

// intSummary mirrors summary but for small integer counts (affected drivers
// per closure), which do not belong in a time.Duration-based type.
type intSummary struct {
	Count int     `json:"count"`
	Min   int     `json:"min"`
	Mean  float64 `json:"mean"`
	P50   int     `json:"p50"`
	P95   int     `json:"p95"`
	Max   int     `json:"max"`
}

func summarizeInts(values []int) intSummary {
	if len(values) == 0 {
		return intSummary{}
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	pick := func(p float64) int { return sorted[int(p*float64(len(sorted)-1))] }
	sum := 0
	for _, v := range sorted {
		sum += v
	}
	return intSummary{
		Count: len(sorted),
		Min:   sorted[0],
		Mean:  float64(sum) / float64(len(sorted)),
		P50:   pick(0.50),
		P95:   pick(0.95),
		Max:   sorted[len(sorted)-1],
	}
}

func printClosureLatencyReport(r closureLatencyReport) {
	fmt.Println()
	fmt.Println("=== road-closure end-to-end rerouting latency ===")
	fmt.Printf("seeds:                 %d (base %d..%d), %d trials/seed, %d warmup\n",
		len(r.Seeds), r.Seeds[0], r.Seeds[len(r.Seeds)-1], r.TrialsPerSeed, r.WarmupTrials)
	fmt.Printf("config:                %d drivers, %d orders, 6x6 grid\n", r.Drivers, r.Orders)
	fmt.Printf("total trials:          %d\n", r.TotalTrials)
	fmt.Printf("successful:            %d\n", r.SuccessfulTrials)
	fmt.Printf("failed:                %d\n", r.FailedTrials)
	fmt.Printf("no-op (0 affected):    %d\n", r.NoOpTrials)
	fmt.Printf("ws timeouts:           %d\n", r.WSTimeouts)
	fmt.Printf("persistence unverified:%d\n", r.VerificationFailures)
	fmt.Printf("affected drivers:      min=%d p50=%d p95=%d max=%d mean=%.2f (n=%d)\n",
		r.AffectedDriversPerClosure.Min, r.AffectedDriversPerClosure.P50, r.AffectedDriversPerClosure.P95,
		r.AffectedDriversPerClosure.Max, r.AffectedDriversPerClosure.Mean, r.AffectedDriversPerClosure.Count)
	fmt.Println()
	fmt.Println("end-to-end (POST send -> causationId-matched road.closed received over WS):")
	fmt.Printf("  p50=%s p95=%s p99=%s mean=%s max=%s min=%s (n=%d)\n",
		r.EndToEnd.P50, r.EndToEnd.P95, r.EndToEnd.P99, r.EndToEnd.Mean, r.EndToEnd.Max, r.EndToEnd.Min, r.EndToEnd.Count)
	fmt.Println("diagnostic breakdown (best-effort, not perfectly isolated - see closureLatencyReport doc comment):")
	fmt.Printf("  http ack:            p50=%s p95=%s (n=%d)\n", r.HTTPAck.P50, r.HTTPAck.P95, r.HTTPAck.Count)
	fmt.Printf("  affected-route detect + A*: p50=%s p95=%s (n=%d)\n", r.Recalc.P50, r.Recalc.P95, r.Recalc.Count)
	fmt.Printf("  residual (queue+emit+ws):   p50=%s p95=%s (n=%d)\n", r.Residual.P50, r.Residual.P95, r.Residual.Count)
	if len(r.FailureSamples) > 0 {
		fmt.Println("sample failures:")
		for _, s := range r.FailureSamples {
			fmt.Println("  " + s)
		}
	}
}
