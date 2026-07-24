package service

import (
	"math"
	"math/rand"
	"sort"
	"sync"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
	"dispatchlab/internal/matching"
	"dispatchlab/internal/simulation"
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
	Seed        int64                `json:"seed"`
	Drivers     int                  `json:"drivers"`
	Arrivals    []Arrival            `json:"arrivals"`
	BatchWindow float64              `json:"batchWindow"`
	Weights     matching.CostWeights `json:"weights"`
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
	AveragePickupTime   float64 `json:"averagePickupTime"`
	P95PickupTime       float64 `json:"p95PickupTime"`
	TotalDistance       float64 `json:"totalDistance"`
	AssignmentComputeMs float64 `json:"assignmentComputeMs"`
}

// ComparisonResult bundles both strategies' metrics for one scenario.
type ComparisonResult struct {
	ID        string   `json:"id"`
	Scenario  Scenario `json:"scenario"`
	Baseline  Metrics  `json:"baseline"`
	Optimized Metrics  `json:"optimized"`
}

// DefaultScenario deterministically generates a demand workload for a given
// seed and driver count: a fixed number of orders arriving at fixed
// intervals, with pickup/destination pairs chosen pseudo-randomly from the
// same seed, so the scenario (and therefore the comparison) is fully
// reproducible from just these two numbers.
func DefaultScenario(seed int64, drivers int) Scenario {
	if drivers <= 0 {
		drivers = 12
	}

	c := city.GenerateGrid(city.DefaultGridConfig(seed))
	nodeIDs := make([]domain.NodeID, 0, len(c.Nodes))
	for id := range c.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	const orderCount = 20
	const arrivalInterval = 3.0

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
		Seed:           seed,
		Drivers:        drivers,
		Arrivals:       arrivals,
		BatchWindow:    5,
		Weights:        matching.DefaultCostWeights(),
		MaxVirtualTime: 200,
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
		ID:          "comparison-" + label,
		Seed:        scenario.Seed,
		DriverCount: scenario.Drivers,
		Strategy:    strategy,
		BatchWindow: scenario.BatchWindow,
		CostWeights: scenario.Weights,
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

	return summarize(sim, assignedEvents, totalDistance, label)
}

func summarize(sim *simulation.Simulation, assignedEvents []domain.Event, totalDistance float64, label string) Metrics {
	createdAt := make(map[domain.OrderID]float64)
	for _, o := range sim.Orders() {
		createdAt[o.ID] = o.CreatedAtVirtualTime
	}

	pickupTimes := make([]float64, 0, len(assignedEvents))
	for _, e := range assignedEvents {
		p := e.Payload.(map[string]any)
		orderID := p["orderId"].(domain.OrderID)
		eta := p["pickupEtaVirtualTime"].(float64)
		pickupTimes = append(pickupTimes, eta-createdAt[orderID])
	}
	sort.Float64s(pickupTimes)

	completed, unassigned := 0, 0
	for _, o := range sim.Orders() {
		switch o.Status {
		case domain.OrderDelivered:
			completed++
		case domain.OrderUnassignable, domain.OrderPending:
			unassigned++
		}
	}

	return Metrics{
		Algorithm:           label,
		CompletedDeliveries: completed,
		UnassignedOrders:    unassigned,
		AveragePickupTime:   mean(pickupTimes),
		P95PickupTime:       percentile(pickupTimes, 0.95),
		TotalDistance:       totalDistance,
		AssignmentComputeMs: sim.TotalAssignmentComputeMs(),
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

// Comparisons stores comparison results in memory, keyed by id. Durable
// Postgres persistence is Phase 5; this is an interim in-memory store.
type Comparisons struct {
	mu      sync.Mutex
	results map[string]ComparisonResult
}

// NewComparisons returns an empty in-memory comparison result store.
func NewComparisons() *Comparisons {
	return &Comparisons{results: make(map[string]ComparisonResult)}
}

// Create runs a fresh DefaultScenario for the given seed and driver count
// and stores the result under a generated id.
func (c *Comparisons) Create(seed int64, drivers int) ComparisonResult {
	result := RunComparison(DefaultScenario(seed, drivers))
	result.ID = generateID()

	c.mu.Lock()
	c.results[result.ID] = result
	c.mu.Unlock()

	return result
}

// Get returns a previously created comparison result by id.
func (c *Comparisons) Get(id string) (ComparisonResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.results[id]
	return r, ok
}
