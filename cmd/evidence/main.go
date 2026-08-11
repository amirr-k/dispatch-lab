// Command evidence collects benchmark measurements into benchmarks/results.
// Every published number must come from a file this command writes — never
// from a hand-edited claim.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
	"dispatchlab/internal/matching"
	"dispatchlab/internal/routing"
	"dispatchlab/internal/service"
	"dispatchlab/internal/simulation"
)

func main() {
	outDir := flag.String("out", "benchmarks/results", "directory for latest.json and latest.md")
	trials := flag.Int("closure-trials", 50, "road-closure recalculationMs samples")
	loadgenReconcile := flag.String("loadgen-reconcile", "", "path to a cmd/loadgen -mode=reconcile JSON report to fold in (optional)")
	loadgenThroughput := flag.String("loadgen-throughput", "", "path to a cmd/loadgen -mode=both/websocket JSON report to fold in (optional)")
	loadgenClosureLatency := flag.String("loadgen-closure-latency", "", "path to a cmd/loadgen -mode=closure-latency JSON report to fold in (optional)")
	flag.Parse()

	commit := gitSHA()
	goVersion := runtime.Version()
	machine := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	if brand := cpuBrand(); brand != "" {
		machine = brand + " (" + machine + ")"
	}

	result := Evidence{
		CommitSHA:   commit,
		GoVersion:   goVersion,
		Machine:     machine,
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Config: map[string]any{
			"closureTrials":      *trials,
			"closureSeeds":       []int64{1, 2, 3, 5, 8, 13, 21, 34, 42, 55},
			"comparisonSeeds":    []int64{42, 7, 99},
			"comparisonDrivers":  []int{4, 12},
			"minBatchSize":       2,
			"maxWaitVirtualTime": 2,
		},
		SummaryMethod: "nearest-rank percentiles over sorted samples; comparison metrics from RunComparison on the fair pickup metric (unassigned/pending scored at MaxVirtualTime-CreatedAt)",
	}

	result.ClosureReroute = measureClosureReroutes(*trials)
	result.Comparison = measureComparisonSuite()
	result.Matching = measureMatching()
	result.Routing = measureRouting()
	result.Simulation = measureSimulation()
	result.Loadgen = loadLoadgenEvidence(*loadgenReconcile, *loadgenThroughput, *loadgenClosureLatency)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatalf("mkdir: %v", err)
	}
	jsonPath := filepath.Join(*outDir, "latest.json")
	mdPath := filepath.Join(*outDir, "latest.md")
	if err := writeJSON(jsonPath, result); err != nil {
		fatalf("write json: %v", err)
	}
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(result)), 0o644); err != nil {
		fatalf("write md: %v", err)
	}
	fmt.Printf("wrote %s\nwrote %s\n", jsonPath, mdPath)
}

type Evidence struct {
	CommitSHA      string          `json:"commitSha"`
	GoVersion      string          `json:"goVersion"`
	Machine        string          `json:"machine"`
	CollectedAt    string          `json:"collectedAt"`
	Config         map[string]any  `json:"config"`
	SummaryMethod  string          `json:"summaryMethod"`
	ClosureReroute LatencyBlock    `json:"closureReroute"`
	Comparison     ComparisonBlock `json:"comparison"`
	Matching       MatchingBlock   `json:"matching"`
	Routing        RoutingBlock    `json:"routing"`
	Simulation     SimulationBlock `json:"simulation"`
	// Loadgen holds whatever cmd/loadgen JSON reports were pointed at via
	// -loadgen-reconcile / -loadgen-throughput, folded in verbatim rather
	// than reinterpreted - this command has no server to run loadgen
	// against on its own, so it never fabricates these numbers if the
	// flags are omitted.
	Loadgen *LoadgenEvidence `json:"loadgen,omitempty"`
}

// LoadgenEvidence mirrors the fields of cmd/loadgen's own report structs
// that matter for evidence, without importing that package - cmd/loadgen is
// a client for driving a live server, and cmd/evidence has no business
// depending on it.
type LoadgenEvidence struct {
	Reconcile      json.RawMessage `json:"reconcile,omitempty"`
	Concurrent     json.RawMessage `json:"concurrent,omitempty"`
	WebSocket      json.RawMessage `json:"webSocket,omitempty"`
	ClosureLatency json.RawMessage `json:"closureLatency,omitempty"`
}

type LatencyBlock struct {
	Unit        string    `json:"unit"`
	Trials      int       `json:"trials"`
	Seeds       []int64   `json:"seeds"`
	Raw         []float64 `json:"raw"`
	P50         float64   `json:"p50"`
	P95         float64   `json:"p95"`
	P99         float64   `json:"p99"`
	Mean        float64   `json:"mean"`
	AffectedAvg float64   `json:"affectedRoutesAvg"`
}

type ComparisonCell struct {
	Seed      int64           `json:"seed"`
	Demand    string          `json:"demand"`
	Drivers   int             `json:"drivers"`
	Baseline  service.Metrics `json:"baseline"`
	Optimized service.Metrics `json:"optimized"`
}

type ComparisonBlock struct {
	Cells []ComparisonCell `json:"cells"`
	Note  string           `json:"note"`
}

type MatchingBlock struct {
	BaselineP50Ms  float64 `json:"baselineP50Ms"`
	BaselineP95Ms  float64 `json:"baselineP95Ms"`
	OptimizedP50Ms float64 `json:"optimizedP50Ms"`
	OptimizedP95Ms float64 `json:"optimizedP95Ms"`
	Shape          string  `json:"shape"`
	Trials         int     `json:"trials"`
}

type RoutingBlock struct {
	ShortHopP50Us float64 `json:"shortHopP50Us"`
	ShortHopP95Us float64 `json:"shortHopP95Us"`
	Graph         string  `json:"graph"`
	Trials        int     `json:"trials"`
}

type SimulationBlock struct {
	EventsPerTick float64 `json:"eventsPerTick"`
	Drivers       int     `json:"drivers"`
	Trials        int     `json:"trials"`
}

func measureClosureReroutes(trials int) LatencyBlock {
	seeds := []int64{1, 2, 3, 5, 8, 13, 21, 34, 42, 55}
	raw := make([]float64, 0, trials)
	var affectedSum float64
	affectedN := 0

	for i := 0; i < trials; i++ {
		seed := seeds[i%len(seeds)]
		sim := simulation.New("closure-evidence", seed, 20)
		sim.Start()
		for o := 0; o < 12; o++ {
			nodes := nodePair(sim, o)
			sim.Apply(simulation.PlaceOrder{Pickup: nodes[0], Destination: nodes[1]})
		}
		for t := 0; t < 3; t++ {
			sim.Advance()
		}

		edgeID := activeRouteEdge(sim)
		if edgeID == "" {
			edgeID = anyEdge(sim)
		}
		evs := sim.Apply(simulation.CloseRoad{EdgeID: edgeID})
		for _, e := range evs {
			if e.Type != domain.EventRoadClosed {
				continue
			}
			p := e.Payload.(map[string]any)
			ms, _ := p["recalculationMs"].(float64)
			raw = append(raw, ms)
			switch n := p["affectedRoutes"].(type) {
			case int:
				affectedSum += float64(n)
				affectedN++
			case float64:
				affectedSum += n
				affectedN++
			}
		}
	}

	sort.Float64s(raw)
	block := LatencyBlock{
		Unit:   "ms",
		Trials: len(raw),
		Seeds:  seeds,
		Raw:    raw,
		P50:    percentile(raw, 0.50),
		P95:    percentile(raw, 0.95),
		P99:    percentile(raw, 0.99),
		Mean:   mean(raw),
	}
	if affectedN > 0 {
		block.AffectedAvg = affectedSum / float64(affectedN)
	}
	return block
}

func measureComparisonSuite() ComparisonBlock {
	var cells []ComparisonCell
	for _, seed := range []int64{42, 7, 99} {
		for _, demand := range []service.DemandLevel{service.DemandLight, service.DemandSteady, service.DemandRush} {
			for _, drivers := range []int{4, 12} {
				r := service.RunComparison(service.ScenarioFor(seed, drivers, demand))
				cells = append(cells, ComparisonCell{
					Seed: seed, Demand: string(demand), Drivers: drivers,
					Baseline: r.Baseline, Optimized: r.Optimized,
				})
			}
		}
	}
	return ComparisonBlock{
		Cells: cells,
		Note:  "pickup times include MaxVirtualTime penalty for pending/unassignable orders; optimized uses adaptive min-batch/max-wait",
	}
}

func measureMatching() MatchingBlock {
	const trials = 40
	baseline := make([]float64, 0, trials)
	optimized := make([]float64, 0, trials)
	for i := 0; i < trials; i++ {
		start := time.Now()
		sim := simulation.NewWithConfig(simulation.Config{
			ID: "match-base", Seed: int64(i + 1), DriverCount: 40, Strategy: simulation.StrategyBaseline,
		})
		sim.Start()
		for o := 0; o < 20; o++ {
			pair := nodePair(sim, o)
			sim.Apply(simulation.PlaceOrder{Pickup: pair[0], Destination: pair[1]})
		}
		baseline = append(baseline, float64(time.Since(start).Microseconds())/1000.0)

		start = time.Now()
		sim = simulation.NewWithConfig(simulation.Config{
			ID: "match-opt", Seed: int64(i + 1), DriverCount: 40, Strategy: simulation.StrategyOptimized,
			MinBatchSize: 2, MaxWaitVirtualTime: 2, CostWeights: matching.DefaultCostWeights(),
		})
		sim.Start()
		for o := 0; o < 20; o++ {
			pair := nodePair(sim, o)
			sim.Apply(simulation.PlaceOrder{Pickup: pair[0], Destination: pair[1]})
		}
		for t := 0; t < 5; t++ {
			sim.Advance()
		}
		optimized = append(optimized, float64(time.Since(start).Microseconds())/1000.0)
	}
	sort.Float64s(baseline)
	sort.Float64s(optimized)
	return MatchingBlock{
		BaselineP50Ms: percentile(baseline, 0.50), BaselineP95Ms: percentile(baseline, 0.95),
		OptimizedP50Ms: percentile(optimized, 0.50), OptimizedP95Ms: percentile(optimized, 0.95),
		Shape: "40 drivers / 20 orders", Trials: trials,
	}
}

func measureRouting() RoutingBlock {
	const trials = 500
	c := city.GenerateGrid(city.DefaultGridConfig(1))
	samples := make([]float64, 0, trials)
	for i := 0; i < trials; i++ {
		start := time.Now()
		if _, ok := routing.FindRoute(c, "n-0-0", "n-3-3"); !ok {
			fatalf("short hop unroutable")
		}
		samples = append(samples, float64(time.Since(start).Microseconds()))
	}
	sort.Float64s(samples)
	return RoutingBlock{
		ShortHopP50Us: percentile(samples, 0.50),
		ShortHopP95Us: percentile(samples, 0.95),
		Graph:         "default 6x6 grid, n-0-0 → n-3-3",
		Trials:        trials,
	}
}

func measureSimulation() SimulationBlock {
	sim := simulation.New("sim-evidence", 1, 40)
	sim.Start()
	for o := 0; o < 20; o++ {
		pair := nodePair(sim, o)
		sim.Apply(simulation.PlaceOrder{Pickup: pair[0], Destination: pair[1]})
	}
	const trials = 100
	var events int
	for i := 0; i < trials; i++ {
		events += len(sim.Advance())
	}
	return SimulationBlock{
		EventsPerTick: float64(events) / float64(trials),
		Drivers:       40,
		Trials:        trials,
	}
}

func nodePair(sim *simulation.Simulation, i int) [2]domain.NodeID {
	ids := make([]domain.NodeID, 0, len(sim.City.Nodes))
	for id := range sim.City.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	if len(ids) < 2 {
		return [2]domain.NodeID{"n-0-0", "n-1-1"}
	}
	return [2]domain.NodeID{ids[i%len(ids)], ids[(i+len(ids)/2)%len(ids)]}
}

func activeRouteEdge(sim *simulation.Simulation) domain.EdgeID {
	payload := sim.Snapshot().Payload.(map[string]any)
	drivers, _ := payload["drivers"].([]map[string]any)
	for _, d := range drivers {
		route, _ := d["route"].([]domain.NodeID)
		idx, _ := d["routeIndex"].(int)
		if len(route) < idx+2 {
			continue
		}
		from, to := route[idx], route[idx+1]
		for _, edges := range sim.City.Edges {
			for _, e := range edges {
				if e.From == from && e.To == to {
					return e.ID
				}
			}
		}
	}
	return ""
}

func anyEdge(sim *simulation.Simulation) domain.EdgeID {
	for _, edges := range sim.City.Edges {
		if len(edges) > 0 {
			return edges[0].ID
		}
	}
	return ""
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// loadLoadgenEvidence folds in cmd/loadgen JSON reports if paths were given.
// Both are optional and independent, since a reconcile run and a throughput
// run are separate invocations of cmd/loadgen against a live server.
func loadLoadgenEvidence(reconcilePath, throughputPath, closureLatencyPath string) *LoadgenEvidence {
	if reconcilePath == "" && throughputPath == "" && closureLatencyPath == "" {
		return nil
	}
	out := &LoadgenEvidence{}
	if reconcilePath != "" {
		var report struct {
			Reconcile json.RawMessage `json:"reconcile"`
		}
		if err := readJSON(reconcilePath, &report); err != nil {
			fatalf("read loadgen reconcile report: %v", err)
		}
		out.Reconcile = report.Reconcile
	}
	if throughputPath != "" {
		var report struct {
			Concurrent json.RawMessage `json:"concurrent"`
			WebSocket  json.RawMessage `json:"webSocket"`
		}
		if err := readJSON(throughputPath, &report); err != nil {
			fatalf("read loadgen throughput report: %v", err)
		}
		out.Concurrent = report.Concurrent
		out.WebSocket = report.WebSocket
	}
	if closureLatencyPath != "" {
		// the raw file (with its full per-trial array) is the timestamped
		// evidence artifact under benchmarks/results/ - what gets folded in
		// here is the summary only, with "trials" stripped, so latest.md
		// stays a summary rather than reproducing hundreds of trial records.
		var report map[string]any
		if err := readJSON(closureLatencyPath, &report); err != nil {
			fatalf("read loadgen closure-latency report: %v", err)
		}
		delete(report, "trials")
		report["rawTrialsFile"] = closureLatencyPath
		summarized, err := json.Marshal(report)
		if err != nil {
			fatalf("re-marshal loadgen closure-latency summary: %v", err)
		}
		out.ClosureLatency = summarized
	}
	return out
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(v)
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func renderMarkdown(e Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmark results\n\n")
	fmt.Fprintf(&b, "- Commit: `%s`\n", e.CommitSHA)
	fmt.Fprintf(&b, "- Go: %s\n", e.GoVersion)
	fmt.Fprintf(&b, "- Machine: %s\n", e.Machine)
	fmt.Fprintf(&b, "- Collected: %s\n", e.CollectedAt)
	fmt.Fprintf(&b, "- Summary method: %s\n\n", e.SummaryMethod)
	fmt.Fprintf(&b, "## Closure reroute (`recalculationMs`)\n\n")
	fmt.Fprintf(&b, "trials=%d p50=%.3fms p95=%.3fms p99=%.3fms mean=%.3fms avg affected routes=%.2f\n\n",
		e.ClosureReroute.Trials, e.ClosureReroute.P50, e.ClosureReroute.P95, e.ClosureReroute.P99, e.ClosureReroute.Mean, e.ClosureReroute.AffectedAvg)
	fmt.Fprintf(&b, "## Matching (40 drivers / 20 orders)\n\n")
	fmt.Fprintf(&b, "baseline p50=%.3fms p95=%.3fms; optimized p50=%.3fms p95=%.3fms (n=%d)\n\n",
		e.Matching.BaselineP50Ms, e.Matching.BaselineP95Ms, e.Matching.OptimizedP50Ms, e.Matching.OptimizedP95Ms, e.Matching.Trials)
	fmt.Fprintf(&b, "## Routing short-hop\n\n")
	fmt.Fprintf(&b, "p50=%.1fµs p95=%.1fµs (n=%d)\n\n", e.Routing.ShortHopP50Us, e.Routing.ShortHopP95Us, e.Routing.Trials)
	fmt.Fprintf(&b, "## Simulation throughput\n\n")
	fmt.Fprintf(&b, "%.2f events/tick with %d drivers (n=%d ticks)\n\n", e.Simulation.EventsPerTick, e.Simulation.Drivers, e.Simulation.Trials)
	fmt.Fprintf(&b, "## Comparison suite (canonical 18 cells)\n\n")
	fmt.Fprintf(&b, "%s\n\n", e.Comparison.Note)
	fmt.Fprintf(&b, "| seed | demand | drivers | base avg pickup | opt avg pickup | base dist | opt dist | opt batch/imm |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|\n")
	for _, c := range e.Comparison.Cells {
		fmt.Fprintf(&b, "| %d | %s | %d | %.2f | %.2f | %.0f | %.0f | %d/%d |\n",
			c.Seed, c.Demand, c.Drivers,
			c.Baseline.AveragePickupTime, c.Optimized.AveragePickupTime,
			c.Baseline.TotalDistance, c.Optimized.TotalDistance,
			c.Optimized.BatchDispatches, c.Optimized.ImmediateDispatches)
	}
	if e.Loadgen != nil {
		fmt.Fprintf(&b, "\n## Loadgen (against a live server + real PostgreSQL)\n\n")
		if e.Loadgen.Reconcile != nil {
			fmt.Fprintf(&b, "WS sequence ↔ persisted event reconcile: `%s`\n\n", string(e.Loadgen.Reconcile))
		}
		if e.Loadgen.Concurrent != nil {
			fmt.Fprintf(&b, "Concurrent guest simulations: `%s`\n\n", string(e.Loadgen.Concurrent))
		}
		if e.Loadgen.WebSocket != nil {
			fmt.Fprintf(&b, "WebSocket update throughput: `%s`\n\n", string(e.Loadgen.WebSocket))
		}
		if e.Loadgen.ClosureLatency != nil {
			fmt.Fprintf(&b, "### Road-closure end-to-end rerouting latency (real server, real PostgreSQL, real WebSocket)\n\n")
			fmt.Fprintf(&b, "One client-side monotonic timer per trial: immediately before `POST .../closures` to the moment the causationId-matched `road.closed` frame is fully received over the WebSocket connection. Includes HTTP transport/parsing, the simulation's command-queue wait, affected-driver detection, A* recomputation, event creation/serialization, and WebSocket delivery. PostgreSQL persistence is asynchronous and off this critical path - verified separately per trial, not timed.\n\n")
			fmt.Fprintf(&b, "```json\n%s\n```\n\n", string(e.Loadgen.ClosureLatency))
		}
	}
	return b.String()
}

func gitSHA() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func cpuBrand() string {
	out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
