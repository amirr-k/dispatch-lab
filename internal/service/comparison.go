package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
	"dispatchlab/internal/matching"
	"dispatchlab/internal/simulation"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

// Arrival is one deterministic order placement within a comparison scenario.
type Arrival struct {
	VirtualTime float64       `json:"virtualTime"`
	Pickup      domain.NodeID `json:"pickup"`
	Destination domain.NodeID `json:"destination"`
}

// Scenario is a fully deterministic workload: the same scenario replayed
// against a matching strategy always produces the same outcome, which is
// what makes comparison results reproducible from a checked-in scenario and
// command, per the phase 4 exit gate.
type Scenario struct {
	Seed        int64       `json:"seed"`
	Drivers     int         `json:"drivers"`
	Demand      DemandLevel `json:"demand"`
	Arrivals    []Arrival   `json:"arrivals"`
	BatchWindow float64     `json:"batchWindow"`
	// MinBatchSize / MaxWaitVirtualTime drive adaptive optimized dispatch.
	// When unset (<=0), the simulation applies its own defaults.
	MinBatchSize       int                  `json:"minBatchSize"`
	MaxWaitVirtualTime float64              `json:"maxWaitVirtualTime"`
	Weights            matching.CostWeights `json:"weights"`
	// MaxVirtualTime bounds how long each run is simulated, guaranteeing
	// termination even if some order can never be served; whatever's still
	// unresolved at the cutoff counts toward unassignedOrders.
	MaxVirtualTime float64 `json:"maxVirtualTime"`
}

// Metrics summarizes one matching strategy's run over a scenario. Field
// names and set match the "Report actual results" list in the product
// spec's algorithm comparison demo.
type Metrics struct {
	Algorithm           string  `json:"algorithm"`
	CompletedDeliveries int     `json:"completedDeliveries"`
	UnassignedOrders    int     `json:"unassignedOrders"`
	ServedFraction      float64 `json:"servedFraction"`
	AveragePickupTime   float64 `json:"averagePickupTime"`
	P95PickupTime       float64 `json:"p95PickupTime"`
	TotalDistance       float64 `json:"totalDistance"`
	// BatchDispatches / ImmediateDispatches count how optimized matching
	// fired under adaptive dispatch (min-batch vs max-wait single-order).
	// Baseline always reports zeros.
	BatchDispatches     int `json:"batchDispatches"`
	ImmediateDispatches int `json:"immediateDispatches"`
}

// ComparisonResult bundles both strategies' metrics for one scenario.
type ComparisonResult struct {
	ID        string   `json:"id"`
	Scenario  Scenario `json:"scenario"`
	Baseline  Metrics  `json:"baseline"`
	Optimized Metrics  `json:"optimized"`
}

// DemandLevel selects how much work arrives over the run. It is the control
// that decides which algorithm can win: batch optimization only beats greedy
// nearest-driver when orders actually compete for the same drivers, and with
// enough idle drivers to go around the two make near-identical assignments
// while batching still pays its window delay.
type DemandLevel string

const (
	DemandLight  DemandLevel = "light"
	DemandSteady DemandLevel = "steady"
	DemandRush   DemandLevel = "rush"
)

type demandShape struct {
	orderCount      int
	arrivalInterval float64
	maxVirtualTime  float64
}

// the cutoff scales with the workload: it only exists to guarantee
// termination, so it has to sit far enough past the last arrival that a run
// is not truncated mid-delivery and scored as unassigned.
var demandShapes = map[DemandLevel]demandShape{
	DemandLight:  {orderCount: 12, arrivalInterval: 6, maxVirtualTime: 400},
	DemandSteady: {orderCount: 20, arrivalInterval: 3, maxVirtualTime: 400},
	// rush releases a burst of orders at once so they actually compete for the
	// same drivers. This is the regime where batch optimization is structurally
	// better than greedy nearest-driver: it serves orders the baseline cannot
	// reach before the run ends.
	DemandRush: {orderCount: 0, arrivalInterval: 0, maxVirtualTime: 1000},
}

// rushOrderCount is the number of orders in a rush workload. It scales with
// driver count so the burst stays meaningfully above the available fleet.
func rushOrderCount(drivers int) int {
	n := drivers * 2
	if n < 20 {
		return 20
	}
	if n > 30 {
		return 30
	}
	return n
}

// NormalizeDemand maps arbitrary input onto a known level, defaulting to
// steady so an unset or unrecognized value still produces a valid scenario.
func NormalizeDemand(raw string) DemandLevel {
	level := DemandLevel(raw)
	if _, ok := demandShapes[level]; ok {
		return level
	}
	return DemandSteady
}

// DefaultScenario is ScenarioFor at steady demand, kept so existing callers
// and stored comparisons keep reproducing exactly what they did before.
func DefaultScenario(seed int64, drivers int) Scenario {
	return ScenarioFor(seed, drivers, DemandSteady)
}

// ScenarioFor deterministically generates a demand workload for a given seed,
// driver count and demand level: a fixed number of orders arriving at fixed
// intervals, with pickup/destination pairs chosen pseudo-randomly from the
// same seed, so the scenario (and therefore the comparison) is fully
// reproducible from just those three inputs.
func ScenarioFor(seed int64, drivers int, demand DemandLevel) Scenario {
	if drivers <= 0 {
		drivers = 12
	}
	shape, ok := demandShapes[demand]
	if !ok {
		demand, shape = DemandSteady, demandShapes[DemandSteady]
	}

	orderCount := shape.orderCount
	arrivalInterval := shape.arrivalInterval
	if demand == DemandRush {
		orderCount = rushOrderCount(drivers)
		arrivalInterval = 0
	}

	c := city.GenerateGrid(city.DefaultGridConfig(seed))
	nodeIDs := make([]domain.NodeID, 0, len(c.Nodes))
	for id := range c.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	rng := rand.New(rand.NewSource(seed))
	arrivals := make([]Arrival, orderCount)
	for i := 0; i < orderCount; i++ {
		pickup := nodeIDs[rng.Intn(len(nodeIDs))]
		destination := nodeIDs[rng.Intn(len(nodeIDs))]
		for destination == pickup {
			destination = nodeIDs[rng.Intn(len(nodeIDs))]
		}
		arrivals[i] = Arrival{VirtualTime: float64(i) * arrivalInterval, Pickup: pickup, Destination: destination}
	}

	return Scenario{
		Seed:     seed,
		Drivers:  drivers,
		Demand:   demand,
		Arrivals: arrivals,
		// kept for stored-result / JSON compat; optimized tick logic no
		// longer uses a fixed BatchWindow timer (see MinBatchSize / MaxWait).
		BatchWindow:        2,
		MinBatchSize:       2,
		MaxWaitVirtualTime: 2,
		Weights:            matching.DefaultCostWeights(),
		MaxVirtualTime:     shape.maxVirtualTime,
	}
}

// RunComparison replays scenario once with each matching strategy and
// returns both sets of metrics. Deterministic: the same scenario always
// produces the same result.
func RunComparison(scenario Scenario) ComparisonResult {
	return ComparisonResult{
		Scenario:  scenario,
		Baseline:  runScenario(scenario, simulation.StrategyBaseline, "baseline"),
		Optimized: runScenario(scenario, simulation.StrategyOptimized, "optimized"),
	}
}

func runScenario(scenario Scenario, strategy simulation.MatchingStrategy, label string) Metrics {
	sim := simulation.NewWithConfig(simulation.Config{
		ID:                 "comparison-" + label,
		Seed:               scenario.Seed,
		DriverCount:        scenario.Drivers,
		Strategy:           strategy,
		BatchWindow:        scenario.BatchWindow,
		MinBatchSize:       scenario.MinBatchSize,
		MaxWaitVirtualTime: scenario.MaxWaitVirtualTime,
		CostWeights:        scenario.Weights,
	})
	sim.Start()

	maxTime := scenario.MaxVirtualTime
	if maxTime <= 0 {
		maxTime = 200
	}

	arrivals := make([]Arrival, len(scenario.Arrivals))
	copy(arrivals, scenario.Arrivals)
	sort.Slice(arrivals, func(i, j int) bool { return arrivals[i].VirtualTime < arrivals[j].VirtualTime })

	var assignedEvents []domain.Event
	var totalDistance float64
	// record must be called on every event this run produces: under
	// StrategyBaseline, PlaceOrder assigns immediately inside Apply itself,
	// while under StrategyOptimized the real assignment only surfaces later
	// from Advance (once a batch window fires) - both paths funnel through
	// here so neither strategy's events go uncounted.
	record := func(events []domain.Event) {
		for _, e := range events {
			switch e.Type {
			case domain.EventOrderAssigned:
				assignedEvents = append(assignedEvents, e)
			case domain.EventRouteComputed:
				totalDistance += e.Payload.(map[string]any)["distance"].(float64)
			}
		}
	}

	arrivalIdx := 0
	applyDue := func(now float64) {
		for arrivalIdx < len(arrivals) && arrivals[arrivalIdx].VirtualTime <= now {
			a := arrivals[arrivalIdx]
			record(sim.Apply(simulation.PlaceOrder{Pickup: a.Pickup, Destination: a.Destination}))
			arrivalIdx++
		}
	}

	// t=0 arrivals must land before the first tick, since Start() leaves
	// virtual time at 0.
	applyDue(0)
	for tick := 0; float64(tick) < maxTime; tick++ {
		record(sim.Advance())
		applyDue(float64(tick + 1))
	}

	return summarize(sim, assignedEvents, totalDistance, maxTime, label)
}

func summarize(sim *simulation.Simulation, assignedEvents []domain.Event, totalDistance, maxVirtualTime float64, label string) Metrics {
	assignedETA := make(map[domain.OrderID]float64, len(assignedEvents))
	for _, e := range assignedEvents {
		p := e.Payload.(map[string]any)
		orderID := p["orderId"].(domain.OrderID)
		assignedETA[orderID] = p["pickupEtaVirtualTime"].(float64)
	}

	orders := sim.Orders()
	pickupTimes := make([]float64, 0, len(orders))
	completed, unassigned := 0, 0
	for _, o := range orders {
		switch o.Status {
		case domain.OrderDelivered:
			completed++
		case domain.OrderUnassignable, domain.OrderPending:
			unassigned++
		}
		if eta, ok := assignedETA[o.ID]; ok {
			pickupTimes = append(pickupTimes, eta-o.CreatedAtVirtualTime)
		} else {
			// never assigned by cutoff: score the full remaining horizon so
			// pending/unassignable orders cannot inflate "avg pickup" by
			// simply being left out of the sample.
			pickupTimes = append(pickupTimes, maxVirtualTime-o.CreatedAtVirtualTime)
		}
	}
	sort.Float64s(pickupTimes)

	servedFraction := 0.0
	if n := len(orders); n > 0 {
		servedFraction = float64(completed) / float64(n)
	}

	batch, immediate := sim.DispatchCounts()
	return Metrics{
		Algorithm:           label,
		CompletedDeliveries: completed,
		UnassignedOrders:    unassigned,
		ServedFraction:      servedFraction,
		AveragePickupTime:   mean(pickupTimes),
		P95PickupTime:       percentile(pickupTimes, 0.95),
		TotalDistance:       totalDistance,
		BatchDispatches:     batch,
		ImmediateDispatches: immediate,
	}
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

// percentile uses the nearest-rank method over an already-sorted slice.
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

// Comparisons runs and retains comparison results. Results are held in memory
// for the life of the process and, when a store is attached, written through
// to it so a published number stays traceable to the run that produced it.
type Comparisons struct {
	mu      sync.Mutex
	results map[string]ComparisonResult

	store   store.Store
	metrics *telemetry.Metrics
	logger  *slog.Logger
}

// NewComparisons returns a comparison store with no persistence attached.
func NewComparisons() *Comparisons {
	return NewComparisonsWithStore(nil, nil, nil)
}

// NewComparisonsWithStore returns a comparison store that persists results.
func NewComparisonsWithStore(s store.Store, metrics *telemetry.Metrics, logger *slog.Logger) *Comparisons {
	if logger == nil {
		logger = slog.Default()
	}
	return &Comparisons{
		results: make(map[string]ComparisonResult),
		store:   s,
		metrics: metrics,
		logger:  logger,
	}
}

// Create runs a fresh DefaultScenario for the given seed and driver count
// and stores the result under a generated id.
func (c *Comparisons) Create(ctx context.Context, seed int64, drivers int, demand DemandLevel) ComparisonResult {
	start := time.Now()
	result := RunComparison(ScenarioFor(seed, drivers, demand))
	result.ID = generateID()

	c.mu.Lock()
	c.results[result.ID] = result
	c.mu.Unlock()

	c.logger.Info("ran an algorithm comparison",
		"comparison_id", result.ID, "seed", seed, "drivers", drivers, "demand", string(demand),
		"duration_ms", telemetry.DurationMs(time.Since(start)))

	c.persist(ctx, result)
	return result
}

func (c *Comparisons) persist(ctx context.Context, result ComparisonResult) {
	if c.store == nil {
		return
	}

	payload, err := json.Marshal(result)
	if err != nil {
		c.metrics.PersistenceErrors().Inc()
		c.logger.Error("could not encode a comparison", "comparison_id", result.ID, "error", err)
		return
	}

	record := store.Comparison{
		ID:        result.ID,
		Seed:      result.Scenario.Seed,
		Drivers:   result.Scenario.Drivers,
		Result:    payload,
		CreatedAt: time.Now().UTC(),
	}
	if err := c.store.SaveComparison(ctx, record); err != nil {
		c.metrics.PersistenceErrors().Inc()
		c.logger.Error("could not persist a comparison", "comparison_id", result.ID, "error", err)
	}
}

// Get returns a previously created comparison result by id, falling back to
// the store for a result this process did not run itself.
func (c *Comparisons) Get(ctx context.Context, id string) (ComparisonResult, bool) {
	c.mu.Lock()
	result, ok := c.results[id]
	c.mu.Unlock()
	if ok {
		return result, true
	}

	if c.store == nil {
		return ComparisonResult{}, false
	}
	record, err := c.store.GetComparison(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.logger.Error("could not read a comparison", "comparison_id", id, "error", err)
		}
		return ComparisonResult{}, false
	}
	if err := json.Unmarshal(record.Result, &result); err != nil {
		c.logger.Error("could not decode a stored comparison", "comparison_id", id, "error", err)
		return ComparisonResult{}, false
	}
	return result, true
}
