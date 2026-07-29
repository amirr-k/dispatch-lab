// Command loadgen drives a running dispatchlab server the way many
// concurrent visitors would, to measure what the Go benchmarks in
// internal/*/bench_test.go cannot: WebSocket update throughput and how the
// system behaves under many simultaneous guest simulations. It is a client
// only - every number it reports comes from a real server's real responses,
// never a computed estimate.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "http://localhost:8080", "base URL of a running dispatchlab server")
	mode := flag.String("mode", "both", "concurrent | websocket | reconcile | closure-latency | both")
	simulations := flag.Int("simulations", 10, "number of concurrent guest visitors to simulate")
	drivers := flag.Int("drivers", 12, "drivers per simulation")
	duration := flag.Duration("duration", 15*time.Second, "how long to sustain load")
	think := flag.Duration("think", 0, "delay between requests per client; 0 sends as fast as possible (a burst/ceiling test rather than sustained load)")
	output := flag.String("output", "", "optional path to write the JSON report")

	closureSeed := flag.Int64("closure-base-seed", 1000, "first deterministic seed used by closure-latency mode; subsequent seeds increment from it")
	closureSeedCount := flag.Int("closure-seeds", 10, "number of distinct deterministic seeds for closure-latency mode")
	closureTrialsPerSeed := flag.Int("closure-trials-per-seed", 20, "measured trials per seed for closure-latency mode")
	closureWarmup := flag.Int("closure-warmup", 10, "warmup trials run and discarded before closure-latency mode starts recording")
	closureDrivers := flag.Int("closure-drivers", 20, "drivers per simulation for closure-latency mode")
	closureOrders := flag.Int("closure-orders", 12, "orders placed per simulation for closure-latency mode")
	closureConcurrency := flag.Int("closure-concurrency", 6, "concurrent trials in flight for closure-latency mode")
	closureTimeout := flag.Duration("closure-timeout", 5*time.Second, "per-trial wait for the causationId-matched road.closed event before it counts as a WS timeout")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := runConfig{addr: *addr, simulations: *simulations, drivers: *drivers, duration: *duration, think: *think}

	report := struct {
		Addr        string            `json:"addr"`
		Simulations int               `json:"simulations"`
		Drivers     int               `json:"drivers"`
		Duration    string            `json:"duration"`
		Think       string            `json:"think"`
		Concurrent  *concurrentReport `json:"concurrent,omitempty"`
		WebSocket   *webSocketReport  `json:"webSocket,omitempty"`
		Reconcile   *reconcileReport  `json:"reconcile,omitempty"`
	}{Addr: *addr, Simulations: *simulations, Drivers: *drivers, Duration: duration.String(), Think: think.String()}

	if *mode == "concurrent" || *mode == "both" {
		log.Printf("running concurrent-simulations load: %d visitors for %s", *simulations, *duration)
		r := runConcurrent(ctx, cfg)
		report.Concurrent = &r
		printConcurrentReport(r)
	}

	if *mode == "websocket" || *mode == "both" {
		log.Printf("running websocket-throughput load: %d streams for %s", *simulations, *duration)
		r := runWebSocket(ctx, cfg)
		report.WebSocket = &r
		printWebSocketReport(r)
	}

	if *mode == "closure-latency" {
		ccfg := closureLatencyConfig{
			addr:          *addr,
			baseSeed:      *closureSeed,
			seedCount:     *closureSeedCount,
			trialsPerSeed: *closureTrialsPerSeed,
			warmupTrials:  *closureWarmup,
			drivers:       *closureDrivers,
			orders:        *closureOrders,
			concurrency:   *closureConcurrency,
			timeout:       *closureTimeout,
		}
		log.Printf("running road-closure end-to-end latency: %d seeds x %d trials/seed (+%d warmup)",
			ccfg.seedCount, ccfg.trialsPerSeed, ccfg.warmupTrials)
		r := runClosureLatency(ctx, ccfg)
		printClosureLatencyReport(r)
		if *output != "" {
			writeReport(*output, r)
		}
		if r.FailedTrials > 0 || r.WSTimeouts > 0 || r.VerificationFailures > 0 {
			os.Exit(1)
		}
		return
	}

	if *mode == "reconcile" {
		log.Printf("running WS sequence ↔ persisted event reconcile")
		r := runReconcile(ctx, cfg)
		report.Reconcile = &r
		printReconcileReport(r)
		if !r.OK {
			if *output != "" {
				writeReport(*output, report)
			}
			os.Exit(1)
		}
	}

	if *output != "" {
		writeReport(*output, report)
	}
}

func writeReport(path string, report any) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("open output file: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		log.Fatalf("write report: %v", err)
	}
	log.Printf("wrote report to %s", path)
}

type runConfig struct {
	addr        string
	simulations int
	drivers     int
	duration    time.Duration
	// think paces requests per client. Zero sends back to back, which
	// measures the server's ceiling (and the rate limiter refusing most of
	// it) rather than what one visitor's clicking would actually generate.
	think time.Duration
}

// concurrentReport covers the "concurrent guest simulations" performance
// requirement: how many independent visitors the server can seat and serve
// orders to at once, each under its own guest session and quota.
type concurrentReport struct {
	SessionsIssued     int         `json:"sessionsIssued"`
	SessionFailures    int         `json:"sessionFailures"`
	SimulationsCreated int         `json:"simulationsCreated"`
	SimulationFailures int         `json:"simulationFailures"`
	OrdersAttempted    int         `json:"ordersAttempted"`
	OrderStatusCounts  map[int]int `json:"orderStatusCounts"`
	OrderLatency       summary     `json:"orderLatencySuccessful"`
	SessionLatency     summary     `json:"sessionIssueLatency"`
	SimCreateLatency   summary     `json:"simulationCreateLatency"`
	OrdersPerSecond    float64     `json:"ordersPerSecond"`
}

func runConcurrent(ctx context.Context, cfg runConfig) concurrentReport {
	var (
		sessionsOK, sessionsFail int64
		simsOK, simsFail         int64
		ordersAttempted          int64
	)
	statusCounts := newCounter()
	orderLatency := &latencies{}
	sessionLatency := &latencies{}
	simLatency := &latencies{}

	deadline := time.Now().Add(cfg.duration)
	var wg sync.WaitGroup
	for i := 0; i < cfg.simulations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			c := newClient(cfg.addr)
			elapsed, err := c.issueSession(ctx)
			sessionLatency.add(elapsed)
			if err != nil {
				atomic.AddInt64(&sessionsFail, 1)
				return
			}
			atomic.AddInt64(&sessionsOK, 1)

			simID, pickup, destination, elapsed, err := c.createSimulation(ctx, cfg.drivers)
			simLatency.add(elapsed)
			if err != nil {
				atomic.AddInt64(&simsFail, 1)
				return
			}
			atomic.AddInt64(&simsOK, 1)

			for time.Now().Before(deadline) {
				if ctx.Err() != nil {
					return
				}
				status, elapsed, err := c.placeOrder(ctx, simID, pickup, destination)
				atomic.AddInt64(&ordersAttempted, 1)
				if err != nil {
					continue
				}
				statusCounts.inc(status)
				if status == 202 {
					orderLatency.add(elapsed)
				}
				if cfg.think > 0 {
					time.Sleep(cfg.think)
				}
			}
		}()
	}
	wg.Wait()

	counts := statusCounts.snapshot()
	successful := counts[202]
	seconds := cfg.duration.Seconds()

	return concurrentReport{
		SessionsIssued:     int(sessionsOK),
		SessionFailures:    int(sessionsFail),
		SimulationsCreated: int(simsOK),
		SimulationFailures: int(simsFail),
		OrdersAttempted:    int(ordersAttempted),
		OrderStatusCounts:  counts,
		OrderLatency:       orderLatency.summarize(),
		SessionLatency:     sessionLatency.summarize(),
		SimCreateLatency:   simLatency.summarize(),
		OrdersPerSecond:    float64(successful) / seconds,
	}
}

// webSocketReport covers WebSocket update throughput: with a steady stream
// of orders landing on each simulation, how many events per second a
// connection actually receives.
type webSocketReport struct {
	StreamsConnected         int     `json:"streamsConnected"`
	StreamFailures           int     `json:"streamFailures"`
	ConnectLatency           summary `json:"connectLatency"`
	TotalEvents              int64   `json:"totalEvents"`
	EventsPerSecond          float64 `json:"eventsPerSecond"`
	EventsPerStreamPerSecond float64 `json:"eventsPerStreamPerSecond"`
}

func runWebSocket(ctx context.Context, cfg runConfig) webSocketReport {
	var (
		streamsOK, streamsFail int64
		totalEvents            int64
	)
	connectLatency := &latencies{}

	deadline := time.Now().Add(cfg.duration)
	var wg sync.WaitGroup
	for i := 0; i < cfg.simulations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			c := newClient(cfg.addr)
			if _, err := c.issueSession(ctx); err != nil {
				atomic.AddInt64(&streamsFail, 1)
				return
			}
			simID, pickup, destination, _, err := c.createSimulation(ctx, cfg.drivers)
			if err != nil {
				atomic.AddInt64(&streamsFail, 1)
				return
			}

			start := time.Now()
			conn, err := c.dialStream(ctx, simID)
			if err != nil {
				atomic.AddInt64(&streamsFail, 1)
				return
			}
			defer conn.Close()

			// the first frame is always the current snapshot - the moment
			// it arrives is the connection's real usable latency, not just
			// the TCP+TLS handshake.
			if _, _, err := conn.ReadMessage(); err != nil {
				atomic.AddInt64(&streamsFail, 1)
				return
			}
			connectLatency.add(time.Since(start))
			atomic.AddInt64(&streamsOK, 1)

			var streamEvents int64
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					atomic.AddInt64(&streamEvents, 1)
					atomic.AddInt64(&totalEvents, 1)
				}
			}()

			// keep a steady trickle of orders flowing so the stream has
			// something to report - an idle connection proves nothing about
			// throughput.
			ticker := time.NewTicker(150 * time.Millisecond)
			defer ticker.Stop()
			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return
				case <-done:
					return
				case <-ticker.C:
					c.placeOrder(ctx, simID, pickup, destination)
				}
			}
			conn.Close()
			<-done
		}()
	}
	wg.Wait()

	seconds := cfg.duration.Seconds()
	streams := int(streamsOK)
	report := webSocketReport{
		StreamsConnected: streams,
		StreamFailures:   int(streamsFail),
		ConnectLatency:   connectLatency.summarize(),
		TotalEvents:      totalEvents,
		EventsPerSecond:  float64(totalEvents) / seconds,
	}
	if streams > 0 {
		report.EventsPerStreamPerSecond = report.EventsPerSecond / float64(streams)
	}
	return report
}

func printConcurrentReport(r concurrentReport) {
	fmt.Println()
	fmt.Println("=== concurrent guest simulations ===")
	fmt.Printf("sessions issued:     %d (failed %d)\n", r.SessionsIssued, r.SessionFailures)
	fmt.Printf("simulations created: %d (failed %d)\n", r.SimulationsCreated, r.SimulationFailures)
	fmt.Printf("orders attempted:    %d\n", r.OrdersAttempted)
	fmt.Printf("order status codes:  %v\n", r.OrderStatusCounts)
	fmt.Printf("orders/sec (202):    %.1f\n", r.OrdersPerSecond)
	fmt.Printf("order latency:       p50=%s p95=%s p99=%s max=%s (n=%d)\n",
		r.OrderLatency.P50, r.OrderLatency.P95, r.OrderLatency.P99, r.OrderLatency.Max, r.OrderLatency.Count)
	fmt.Printf("session latency:     p50=%s p95=%s (n=%d)\n",
		r.SessionLatency.P50, r.SessionLatency.P95, r.SessionLatency.Count)
	fmt.Printf("sim-create latency:  p50=%s p95=%s (n=%d)\n",
		r.SimCreateLatency.P50, r.SimCreateLatency.P95, r.SimCreateLatency.Count)
}

func printWebSocketReport(r webSocketReport) {
	fmt.Println()
	fmt.Println("=== websocket update throughput ===")
	fmt.Printf("streams connected:      %d (failed %d)\n", r.StreamsConnected, r.StreamFailures)
	fmt.Printf("connect latency:        p50=%s p95=%s (n=%d)\n",
		r.ConnectLatency.P50, r.ConnectLatency.P95, r.ConnectLatency.Count)
	fmt.Printf("total events received:  %d\n", r.TotalEvents)
	fmt.Printf("events/sec (aggregate): %.1f\n", r.EventsPerSecond)
	fmt.Printf("events/sec (per stream):%.1f\n", r.EventsPerStreamPerSecond)
}

// reconcileReport checks that every sequenced WebSocket event eventually
// appears as a persisted replay row with the same sequence number. This is
// only meaningful against a Postgres-backed server — an in-memory store still
// passes, but the point of the check is durability across the recorder flush.
type reconcileReport struct {
	SimulationID       string `json:"simulationId"`
	WSSequences        int    `json:"wsSequences"`
	PersistedSequences int    `json:"persistedSequences"`
	MissingSequences   []int  `json:"missingSequences,omitempty"`
	OK                 bool   `json:"ok"`
	Error              string `json:"error,omitempty"`
}

func runReconcile(ctx context.Context, cfg runConfig) reconcileReport {
	c := newClient(cfg.addr)
	if _, err := c.issueSession(ctx); err != nil {
		return reconcileReport{OK: false, Error: err.Error()}
	}
	simID, pickup, destination, _, err := c.createSimulation(ctx, cfg.drivers)
	if err != nil {
		return reconcileReport{OK: false, Error: err.Error()}
	}

	conn, err := c.dialStream(ctx, simID)
	if err != nil {
		return reconcileReport{SimulationID: simID, OK: false, Error: err.Error()}
	}

	seen := map[int]bool{}
	var maxSeq int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame struct {
				Sequence int    `json:"sequence"`
				Type     string `json:"type"`
			}
			if json.Unmarshal(data, &frame) != nil {
				continue
			}
			// snapshots reuse the last sequence and are not unique log rows
			if frame.Type == "simulation.snapshot" {
				continue
			}
			if frame.Sequence > 0 {
				seen[frame.Sequence] = true
				if frame.Sequence > maxSeq {
					maxSeq = frame.Sequence
				}
			}
		}
	}()

	deadline := time.Now().Add(cfg.duration)
	if cfg.duration <= 0 {
		deadline = time.Now().Add(5 * time.Second)
	}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		c.placeOrder(ctx, simID, pickup, destination)
		if cfg.think > 0 {
			time.Sleep(cfg.think)
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// freeze the run so the recorder is not racing new ticks, then pin and
	// flush so the persisted log matches what the stream already saw.
	_ = c.setPaused(ctx, simID, true)
	time.Sleep(500 * time.Millisecond)
	if err := c.markShowcase(ctx, simID); err != nil {
		conn.Close()
		<-done
		return reconcileReport{SimulationID: simID, OK: false, Error: "showcase: " + err.Error()}
	}
	time.Sleep(500 * time.Millisecond)
	conn.Close()
	<-done

	persisted, err := c.fetchReplaySequences(ctx, simID)
	if err != nil {
		return reconcileReport{SimulationID: simID, WSSequences: len(seen), OK: false, Error: err.Error()}
	}

	var missing []int
	for seq := range seen {
		if !persisted[seq] {
			missing = append(missing, seq)
		}
	}
	sort.Ints(missing)

	return reconcileReport{
		SimulationID:       simID,
		WSSequences:        len(seen),
		PersistedSequences: len(persisted),
		MissingSequences:   missing,
		OK:                 len(missing) == 0 && len(seen) > 0,
	}
}

func printReconcileReport(r reconcileReport) {
	fmt.Println()
	fmt.Println("=== websocket ↔ persisted event reconcile ===")
	fmt.Printf("simulation:            %s\n", r.SimulationID)
	fmt.Printf("ws sequences:          %d\n", r.WSSequences)
	fmt.Printf("persisted sequences:   %d\n", r.PersistedSequences)
	if len(r.MissingSequences) > 0 {
		fmt.Printf("missing sequences:     %v\n", r.MissingSequences)
	}
	if r.Error != "" {
		fmt.Printf("error:                 %s\n", r.Error)
	}
	fmt.Printf("ok:                    %v\n", r.OK)
}
