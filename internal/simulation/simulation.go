// Package simulation runs one deterministic simulation as an actor: a single
// goroutine owns all mutable state, commands enter through a bounded
// channel, and immutable events are the only output.
package simulation

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
	"dispatchlab/internal/matching"
	"dispatchlab/internal/routing"
	"dispatchlab/internal/spatial"
	"dispatchlab/internal/telemetry"
)

// tickInterval is wall-clock only: it paces how fast virtual time is
// advanced and flushed for a live viewer. It never affects what happens.
const tickInterval = 500 * time.Millisecond

// virtualStepPerTick is the deterministic amount of virtual time each tick
// advances, independent of actual wall-clock elapsed since the last tick.
const virtualStepPerTick = 1.0

// driverSpeed is how fast drivers move through the city in distance units per
// virtual-time unit. With a default cell spacing of 100, a speed of 25 makes a
// typical edge take 3-4 ticks, so longer roads are visibly slower than short
// ones and the batch window is small relative to a trip.
const driverSpeed = 25.0

// MatchingStrategy selects how a simulation matches pending orders to
// drivers. The comparison runner replays an identical scenario once under
// each strategy; the live public demo always uses StrategyBaseline so order
// placement stays immediate rather than waiting on a batch window.
type MatchingStrategy string

const (
	StrategyBaseline  MatchingStrategy = "baseline"
	StrategyOptimized MatchingStrategy = "optimized"
)

// Config configures a simulation beyond what New's simpler signature takes.
// Zero-value fields fall back to sane defaults, so existing callers using
// New are unaffected.
type Config struct {
	ID          string
	Seed        int64
	DriverCount int
	// Strategy defaults to StrategyBaseline (immediate nearest-driver
	// assignment) when empty.
	Strategy MatchingStrategy
	// BatchWindow is retained for API/compat; optimized dispatch no longer
	// fires on a fixed timer once MinBatchSize / MaxWaitVirtualTime are set.
	// Ignored under StrategyBaseline. Defaults to 5 if <= 0.
	BatchWindow float64
	// MinBatchSize is how many pending orders trigger a full optimized
	// batch. Defaults to 2 if <= 0.
	MinBatchSize int
	// MaxWaitVirtualTime is how long the oldest pending order may wait
	// before a forced dispatch when a batch has not filled. Defaults to 2
	// if <= 0. A lone pending order with an idle driver skips this wait.
	MaxWaitVirtualTime float64
	// CandidatesPerOrder bounds how many nearby drivers the spatial index
	// returns per order for optimized matching. Defaults to 8 if <= 0.
	CandidatesPerOrder int
	// CostWeights configures optimized matching's cost function. Defaults
	// to matching.DefaultCostWeights() if left zero-valued.
	CostWeights matching.CostWeights
	// Metrics and Logger are optional. A nil Metrics records nothing and a
	// nil Logger falls back to the default one, so headless callers like the
	// comparison runner need neither.
	Metrics *telemetry.Metrics
	Logger  *slog.Logger
}

// Command is a message applied to a simulation on its owning goroutine.
type Command interface{ isCommand() }

// PlaceOrder creates an order and triggers assignment.
type PlaceOrder struct {
	Pickup      domain.NodeID
	Destination domain.NodeID
}

// SetPaused halts or resumes virtual-time advancement. Commands are still
// accepted while paused.
type SetPaused struct{ Paused bool }

// Reset returns drivers and orders to their initial seeded state.
type Reset struct{}

// SetSpeed changes how fast wall-clock ticks advance virtual time for a live
// viewer. It only affects playback rate, never simulation outcomes.
type SetSpeed struct{ Multiplier float64 }

// CloseRoad closes both directions of the road segment the given edge
// belongs to, invalidating any driver route that crosses it. CommandID, when
// set, is stamped as CausationID on every event this command produces (the
// road.closed event itself and any route.invalidated/route.computed/
// order.unassignable/driver.status.changed events the resulting reroutes
// emit), so a caller can determine exactly which events one closure caused.
type CloseRoad struct {
	EdgeID    domain.EdgeID
	CommandID string
}

// ReopenRoad reopens both directions of a previously closed road segment. It
// never needs to recalculate any route: every currently active route was
// necessarily computed while this edge was closed (or never crossed it at
// all), so reopening only ever adds a path a future route might use - it
// cannot invalidate one that already exists.
type ReopenRoad struct{ EdgeID domain.EdgeID }

// envelope carries a command together with the trace it belongs to, so a
// request's trace survives the hop onto the simulation's own goroutine.
type envelope struct {
	cmd   Command
	trace telemetry.SpanContext
}

func (PlaceOrder) isCommand() {}
func (SetPaused) isCommand()  {}
func (Reset) isCommand()      {}
func (SetSpeed) isCommand()   {}
func (CloseRoad) isCommand()  {}
func (ReopenRoad) isCommand() {}

// Simulation owns one simulation's state and runs its actor loop.
type Simulation struct {
	ID   string
	Seed int64
	City *domain.City

	driverCount int
	paused      bool
	speed       float64

	strategy           MatchingStrategy
	batchWindow        float64
	nextBatchAt        float64
	minBatchSize       int
	maxWaitVirtualTime float64
	candidatesPerOrder int
	costWeights        matching.CostWeights
	// pendingOrders holds orders placed but not yet matched, only populated
	// under StrategyOptimized (StrategyBaseline still assigns immediately).
	pendingOrders []domain.OrderID
	// driverIndex tracks idle drivers' positions for optimized matching's
	// candidate lookups. Idle drivers don't move, so it only needs updating
	// at idle-transition points, never on every tick. Nil under
	// StrategyBaseline, which never consults it.
	driverIndex  *spatial.Grid
	gridCellSize float64
	// totalAssignmentComputeMs sums the real wall-clock time spent inside
	// matching calls (once per immediate assignment or per batch, never
	// double-counted per resulting pairing) - retained for telemetry/benches,
	// not published in comparison metrics.
	totalAssignmentComputeMs float64
	batchDispatchCount       int
	immediateDispatchCount   int

	drivers map[domain.DriverID]*domain.Driver
	orders  map[domain.OrderID]*domain.Order

	virtualTime float64
	sequence    int

	metrics *telemetry.Metrics
	logger  *slog.Logger
	// currentTrace is the trace of the command being handled right now. Every
	// event emitted while it is set carries it, which is what connects an
	// HTTP request to the events a browser eventually renders. Only ever
	// touched on the actor goroutine.
	currentTrace string
	// currentCausationID is the command id of the command being handled right
	// now, when that command carries one (currently only CloseRoad). Every
	// event emitted while it is set carries it as CausationID. Only ever
	// touched on the actor goroutine.
	currentCausationID string

	commands chan envelope
	events   chan domain.Event
	// queries lets other goroutines request a current-state snapshot without
	// touching simulation state directly; the reply is built on this loop.
	queries chan chan domain.Event

	// pending collects events emitted during a single step before they are
	// either returned (headless stepping) or published to the channel (Run).
	pending []domain.Event

	nextOrderID int
}

// New builds a simulation with a deterministically generated small city and
// driverCount drivers placed at deterministic starting nodes, using the
// immediate-assignment baseline strategy.
func New(id string, seed int64, driverCount int) *Simulation {
	return NewWithConfig(Config{ID: id, Seed: seed, DriverCount: driverCount})
}

// NewWithConfig builds a simulation with explicit matching-strategy and
// batching configuration, used by the comparison runner to replay an
// identical scenario under both strategies.
func NewWithConfig(cfg Config) *Simulation {
	if cfg.Strategy == "" {
		cfg.Strategy = StrategyBaseline
	}
	if cfg.BatchWindow <= 0 {
		cfg.BatchWindow = 5
	}
	if cfg.MinBatchSize <= 0 {
		cfg.MinBatchSize = 2
	}
	if cfg.MaxWaitVirtualTime <= 0 {
		cfg.MaxWaitVirtualTime = 2
	}
	if cfg.CandidatesPerOrder <= 0 {
		cfg.CandidatesPerOrder = 8
	}
	if cfg.CostWeights == (matching.CostWeights{}) {
		cfg.CostWeights = matching.DefaultCostWeights()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	gridCfg := city.DefaultGridConfig(cfg.Seed)
	c := city.GenerateGrid(gridCfg)
	drivers := placeDrivers(c, cfg.DriverCount)

	var index *spatial.Grid
	if cfg.Strategy == StrategyOptimized {
		index = spatial.NewGrid(gridCfg.CellSpacing)
		for id, d := range drivers {
			pos := c.Nodes[d.Position]
			index.Set(string(id), spatial.Point{X: pos.X, Y: pos.Y})
		}
	}

	return &Simulation{
		ID:                 cfg.ID,
		Seed:               cfg.Seed,
		City:               c,
		driverCount:        cfg.DriverCount,
		speed:              1,
		strategy:           cfg.Strategy,
		batchWindow:        cfg.BatchWindow,
		nextBatchAt:        cfg.BatchWindow,
		minBatchSize:       cfg.MinBatchSize,
		maxWaitVirtualTime: cfg.MaxWaitVirtualTime,
		candidatesPerOrder: cfg.CandidatesPerOrder,
		costWeights:        cfg.CostWeights,
		driverIndex:        index,
		gridCellSize:       gridCfg.CellSpacing,
		drivers:            drivers,
		orders:             make(map[domain.OrderID]*domain.Order),
		metrics:            cfg.Metrics,
		logger:             cfg.Logger,
		commands:           make(chan envelope, 32),
		events:             make(chan domain.Event, 256),
		queries:            make(chan chan domain.Event, 8),
	}
}

// placeDrivers spreads driverCount idle drivers across sorted node positions
// so the same seed and count always yield the same starting layout.
func placeDrivers(c *domain.City, driverCount int) map[domain.DriverID]*domain.Driver {
	nodeIDs := make([]domain.NodeID, 0, len(c.Nodes))
	for nid := range c.Nodes {
		nodeIDs = append(nodeIDs, nid)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	drivers := make(map[domain.DriverID]*domain.Driver, driverCount)
	for i := 0; i < driverCount && i < len(nodeIDs); i++ {
		did := domain.DriverID(shortID("driver", i))
		drivers[did] = &domain.Driver{
			ID:       did,
			Position: nodeIDs[i*len(nodeIDs)/driverCount],
			Status:   domain.DriverIdle,
		}
	}
	return drivers
}

func shortID(prefix string, i int) string {
	return fmt.Sprintf("%s-%d", prefix, i)
}

// Events returns the read-only stream of events this simulation emits.
func (s *Simulation) Events() <-chan domain.Event {
	return s.events
}

// Submit enqueues a command for the simulation's actor loop. It never
// blocks the caller on simulation progress beyond the channel's capacity.
func (s *Simulation) Submit(cmd Command) {
	s.commands <- envelope{cmd: cmd}
}

// SubmitCtx enqueues a command and carries ctx's trace onto the actor
// goroutine, so the events the command produces can be tied back to the
// request that sent it.
func (s *Simulation) SubmitCtx(ctx context.Context, cmd Command) {
	s.commands <- envelope{cmd: cmd, trace: telemetry.SpanContextFrom(ctx)}
}

// CurrentSnapshot returns a snapshot of live state built on the actor loop,
// so it never races with command handling. Only valid while Run is active.
func (s *Simulation) CurrentSnapshot() domain.Event {
	reply := make(chan domain.Event, 1)
	s.queries <- reply
	return <-reply
}

// Snapshot returns current state without going through the query channel.
// Like Orders, it is only safe while driving a simulation headlessly in the
// same goroutine; a live simulation must use CurrentSnapshot instead.
func (s *Simulation) Snapshot() domain.Event {
	return s.buildSnapshotEvent()
}

// OrderSummary is read-only info about one order's final outcome, used by
// the comparison runner to compute metrics once a scenario finishes.
type OrderSummary struct {
	ID                   domain.OrderID
	Status               domain.OrderStatus
	CreatedAtVirtualTime float64
}

// Orders returns every order's current status. Only safe to call directly
// (as the comparison runner does) when driving a simulation headlessly in
// the same goroutine — like CurrentSnapshot, it would need the query
// channel instead if called while Run's actor loop is active elsewhere.
func (s *Simulation) Orders() []OrderSummary {
	out := make([]OrderSummary, 0, len(s.orders))
	for _, o := range s.orders {
		out = append(out, OrderSummary{ID: o.ID, Status: o.Status, CreatedAtVirtualTime: o.CreatedAtVirtualTime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// TotalAssignmentComputeMs is the cumulative real wall-clock time spent
// inside matching calls so far — retained for telemetry and benches.
func (s *Simulation) TotalAssignmentComputeMs() float64 {
	return s.totalAssignmentComputeMs
}

// DispatchCounts reports how many times optimized matching fired a full
// batch versus a single-order immediate (place-time or tick) baseline
// dispatch. Baseline strategy always returns zeros.
func (s *Simulation) DispatchCounts() (batch, immediate int) {
	return s.batchDispatchCount, s.immediateDispatchCount
}

// TrySubmit enqueues a command without blocking. It returns false when the
// command buffer is full, giving callers an explicit overflow signal rather
// than stalling a request on simulation progress.
func (s *Simulation) TrySubmit(cmd Command) bool {
	return s.TrySubmitCtx(context.Background(), cmd)
}

// TrySubmitCtx is TrySubmit carrying ctx's trace onto the actor goroutine.
func (s *Simulation) TrySubmitCtx(ctx context.Context, cmd Command) bool {
	select {
	case s.commands <- envelope{cmd: cmd, trace: telemetry.SpanContextFrom(ctx)}:
		return true
	default:
		return false
	}
}

// Run is the actor loop: the only goroutine that ever mutates simulation
// state. It exits when ctx is canceled. Use either Run (live) or the
// headless Start/Apply/Advance stepping methods on a given simulation, never
// both — they share the same underlying state.
func (s *Simulation) Run(ctx context.Context) {
	ticker := time.NewTicker(s.tickDuration())
	defer ticker.Stop()
	defer close(s.events)

	s.emit(domain.EventSimulationSnapshot, s.snapshotPayload())
	s.publish()

	for {
		select {
		case <-ctx.Done():
			return
		case env := <-s.commands:
			prevSpeed := s.speed
			s.handleTraced(env)
			s.publish()
			if s.speed != prevSpeed {
				ticker.Reset(s.tickDuration())
			}
		case reply := <-s.queries:
			reply <- s.buildSnapshotEvent()
		case <-ticker.C:
			s.tick()
			s.publish()
		}
	}
}

// tickDuration is the wall-clock gap between ticks at the current speed. A
// higher speed multiplier means ticks fire more often; virtual time still
// advances by the same fixed step each tick.
func (s *Simulation) tickDuration() time.Duration {
	if s.speed <= 0 {
		return tickInterval
	}
	return time.Duration(float64(tickInterval) / s.speed)
}

// Start emits the initial snapshot and returns it. Headless counterpart to
// the snapshot Run sends when it begins.
func (s *Simulation) Start() []domain.Event {
	s.emit(domain.EventSimulationSnapshot, s.snapshotPayload())
	return s.takePending()
}

// Apply runs one command and returns the events it produced, with no
// dependence on wall-clock time. Used by comparison and replay runners and
// by determinism tests.
func (s *Simulation) Apply(cmd Command) []domain.Event {
	s.handle(cmd)
	return s.takePending()
}

// handleTraced applies a command inside a span that continues the trace the
// submitting request started, and stamps that trace onto every event the
// command emits.
func (s *Simulation) handleTraced(env envelope) {
	if !env.trace.Valid() {
		s.handle(env.cmd)
		return
	}

	ctx := telemetry.WithSpanContext(telemetry.WithLogger(context.Background(), s.logger), env.trace)
	_, span := telemetry.StartSpan(ctx, "simulation.apply",
		slog.String("simulation_id", s.ID), slog.String("command", fmt.Sprintf("%T", env.cmd)))
	defer span.End()

	s.currentTrace = span.TraceID()
	defer func() { s.currentTrace = "" }()

	before := s.sequence
	s.handle(env.cmd)
	span.SetAttrs(slog.Int("events", s.sequence-before))
}

// handle dispatches a command to its state transition. All mutation happens
// here, on the actor goroutine (or synchronously in headless stepping).
func (s *Simulation) handle(cmd Command) {
	switch c := cmd.(type) {
	case PlaceOrder:
		s.handlePlaceOrder(c)
	case SetPaused:
		s.paused = c.Paused
		s.emit(domain.EventSimulationPaused, map[string]any{"paused": s.paused})
	case SetSpeed:
		if c.Multiplier > 0 {
			s.speed = c.Multiplier
			s.emit(domain.EventSimulationSpeed, map[string]any{"multiplier": s.speed})
		}
	case Reset:
		s.reset()
	case CloseRoad:
		s.handleCloseRoad(c)
	case ReopenRoad:
		s.handleReopenRoad(c)
	}
}

// handleCloseRoad closes a road segment (both directions) and reroutes any
// driver whose current path crosses it. A driver that can no longer reach
// its target becomes idle again and its order is marked unassignable, rather
// than left silently stuck.
func (s *Simulation) handleCloseRoad(c CloseRoad) {
	edge, ok := s.City.EdgeByID(c.EdgeID)
	if !ok || edge.Closed {
		return
	}

	s.currentCausationID = c.CommandID
	defer func() { s.currentCausationID = "" }()

	edgeIDs := []domain.EdgeID{edge.ID}
	s.City.SetClosed(edge.ID, true)
	if reverse, ok := edgeBetween(s.City, edge.To, edge.From); ok {
		s.City.SetClosed(reverse.ID, true)
		edgeIDs = append(edgeIDs, reverse.ID)
	}

	affected, recalcMs := s.recalculateAffectedRoutes()
	s.emit(domain.EventRoadClosed, map[string]any{
		"edgeIds":         edgeIDs,
		"from":            edge.From,
		"to":              edge.To,
		"affectedRoutes":  affected,
		"recalculationMs": recalcMs,
	})
}

// handleReopenRoad reopens both directions of a closed road segment. Unlike
// closing, this never touches any driver's route: reopening only expands
// what a *future* route may use, so nothing currently in flight needs
// recalculating.
func (s *Simulation) handleReopenRoad(c ReopenRoad) {
	edge, ok := s.City.EdgeByID(c.EdgeID)
	if !ok || !edge.Closed {
		return
	}

	edgeIDs := []domain.EdgeID{edge.ID}
	s.City.SetClosed(edge.ID, false)
	if reverse, ok := edgeBetween(s.City, edge.To, edge.From); ok {
		s.City.SetClosed(reverse.ID, false)
		edgeIDs = append(edgeIDs, reverse.ID)
	}

	s.emit(domain.EventRoadReopened, map[string]any{
		"edgeIds": edgeIDs,
		"from":    edge.From,
		"to":      edge.To,
	})
}

// edgeBetween looks up the directed edge from->to by endpoints rather than
// ID, since a reverse edge's ID is a generator detail, not a domain
// guarantee.
func edgeBetween(city *domain.City, from, to domain.NodeID) (domain.Edge, bool) {
	for _, e := range city.Edges[from] {
		if e.To == to {
			return e, true
		}
	}
	return domain.Edge{}, false
}

// recalculateAffectedRoutes reroutes every driver whose current path now
// crosses a closed edge. Returns how many drivers were affected and how long
// recomputation took, both surfaced in the road.closed event.
func (s *Simulation) recalculateAffectedRoutes() (int, float64) {
	start := time.Now()

	ids := make([]domain.DriverID, 0, len(s.drivers))
	for id := range s.drivers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	affected := 0
	for _, id := range ids {
		d := s.drivers[id]
		if len(d.Route) == 0 || !routeUsesClosedEdge(s.City, d.Route, d.RouteIndex) {
			continue
		}
		affected++
		s.rerouteDriver(id, d)
	}

	return affected, float64(time.Since(start).Microseconds()) / 1000.0
}

// routeUsesClosedEdge reports whether any remaining hop of route, starting
// at fromIndex, now crosses a closed (or since-removed) edge.
func routeUsesClosedEdge(city *domain.City, route []domain.NodeID, fromIndex int) bool {
	for i := fromIndex; i < len(route)-1; i++ {
		edge, ok := edgeBetween(city, route[i], route[i+1])
		if !ok || edge.Closed {
			return true
		}
	}
	return false
}

// rerouteDriver recomputes a driver's path to whatever it was already
// heading toward (pickup or destination, based on its status). If no path
// exists, the order becomes unassignable and the driver returns to idle.
func (s *Simulation) rerouteDriver(id domain.DriverID, d *domain.Driver) {
	order := s.orders[d.AssignedOrder]
	if order == nil {
		return
	}

	var target domain.NodeID
	var unreachableReason string
	switch d.Status {
	case domain.DriverEnRouteToPick:
		target, unreachableReason = order.Pickup, "road closure left no path to the pickup"
	case domain.DriverDelivering:
		target, unreachableReason = order.Destination, "road closure left no path to the destination"
	default:
		return
	}

	s.emit(domain.EventRouteInvalidated, map[string]any{"driverId": id, "orderId": order.ID})

	route, ok := s.findRoute(d.Position, target)
	if !ok {
		order.Status = domain.OrderUnassignable
		s.emit(domain.EventOrderUnassignable, map[string]any{"orderId": order.ID, "reason": unreachableReason})

		d.Status = domain.DriverIdle
		d.Route = nil
		d.RouteIndex = 0
		d.AssignedOrder = ""
		s.emit(domain.EventDriverStatusChanged, map[string]any{"driverId": id, "status": d.Status})
		return
	}

	d.Route = route.Nodes
	d.RouteIndex = 0
	s.emit(domain.EventRouteComputed, map[string]any{
		"driverId": id,
		"nodeIds":  route.Nodes,
		"distance": route.Distance,
	})
}

// reset restores the initial seeded layout and announces it with a fresh
// snapshot. The event sequence keeps counting so downstream consumers still
// see a monotonic stream across the reset.
func (s *Simulation) reset() {
	s.drivers = placeDrivers(s.City, s.driverCount)
	s.orders = make(map[domain.OrderID]*domain.Order)
	s.virtualTime = 0
	s.nextOrderID = 0
	s.pendingOrders = nil
	s.nextBatchAt = s.batchWindow
	s.batchDispatchCount = 0
	s.immediateDispatchCount = 0

	if s.driverIndex != nil {
		s.driverIndex = spatial.NewGrid(s.gridCellSize)
		for id, d := range s.drivers {
			pos := s.City.Nodes[d.Position]
			s.driverIndex.Set(string(id), spatial.Point{X: pos.X, Y: pos.Y})
		}
	}

	s.emit(domain.EventSimulationSnapshot, s.snapshotPayload())
}

// buildSnapshotEvent describes current state without emitting into the
// sequenced stream; it reuses the last sequence number so a reconnecting
// client knows which live events still follow.
func (s *Simulation) buildSnapshotEvent() domain.Event {
	return domain.Event{
		SchemaVersion: 1,
		SimulationID:  s.ID,
		Sequence:      s.sequence,
		VirtualTime:   s.virtualTime,
		Type:          domain.EventSimulationSnapshot,
		Payload:       s.snapshotPayload(),
	}
}

// Advance steps virtual time forward by one deterministic tick and returns
// the events it produced.
func (s *Simulation) Advance() []domain.Event {
	s.tick()
	return s.takePending()
}

// publish moves events emitted during the current step onto the outbound
// channel, applying the channel's bounded backpressure.
func (s *Simulation) publish() {
	for _, e := range s.takePending() {
		s.events <- e
	}
}

// takePending returns the events accumulated since the last call and clears
// the buffer.
func (s *Simulation) takePending() []domain.Event {
	out := s.pending
	s.pending = nil
	return out
}

// snapshotPayload describes everything needed to resume rendering from this
// point: the city graph, every driver's position and current route, and every
// order's state. It has to be complete rather than minimal, because a
// periodic snapshot is where a replay starts folding the event log forward.
// Only called from the actor goroutine, so it never races with command
// handling. Everything is sorted by ID so the snapshot is byte-for-byte
// reproducible for a given seed rather than following random map order.
func (s *Simulation) snapshotPayload() map[string]any {
	nodeIDs := make([]domain.NodeID, 0, len(s.City.Nodes))
	for id := range s.City.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	nodes := make([]map[string]any, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		n := s.City.Nodes[id]
		nodes = append(nodes, map[string]any{"id": n.ID, "x": n.X, "y": n.Y})
	}

	allEdges := make([]domain.Edge, 0)
	for _, list := range s.City.Edges {
		allEdges = append(allEdges, list...)
	}
	sort.Slice(allEdges, func(i, j int) bool { return allEdges[i].ID < allEdges[j].ID })
	edges := make([]map[string]any, 0, len(allEdges))
	for _, e := range allEdges {
		edges = append(edges, map[string]any{
			"id": e.ID, "from": e.From, "to": e.To, "closed": e.Closed,
		})
	}

	driverIDs := make([]domain.DriverID, 0, len(s.drivers))
	for id := range s.drivers {
		driverIDs = append(driverIDs, id)
	}
	sort.Slice(driverIDs, func(i, j int) bool { return driverIDs[i] < driverIDs[j] })
	drivers := make([]map[string]any, 0, len(driverIDs))
	for _, id := range driverIDs {
		d := s.drivers[id]
		x, y := s.driverPosition(d)
		drivers = append(drivers, map[string]any{
			"id": d.ID, "position": d.Position, "status": d.Status,
			"x": x, "y": y,
			"route": d.Route, "routeIndex": d.RouteIndex, "routeProgress": d.RouteProgress,
			"assignedOrder": d.AssignedOrder,
		})
	}

	orderIDs := make([]domain.OrderID, 0, len(s.orders))
	for id := range s.orders {
		orderIDs = append(orderIDs, id)
	}
	sort.Slice(orderIDs, func(i, j int) bool { return orderIDs[i] < orderIDs[j] })
	orders := make([]map[string]any, 0, len(orderIDs))
	for _, id := range orderIDs {
		o := s.orders[id]
		orders = append(orders, map[string]any{
			"id": o.ID, "pickup": o.Pickup, "destination": o.Destination,
			"status": o.Status, "assignedDriver": o.AssignedDriver,
			"createdAtVirtualTime": o.CreatedAtVirtualTime,
		})
	}

	return map[string]any{
		"nodes": nodes, "edges": edges, "drivers": drivers, "orders": orders,
		"paused": s.paused, "speed": s.speed,
	}
}

func (s *Simulation) emit(t domain.EventType, payload any) {
	s.sequence++
	s.pending = append(s.pending, domain.Event{
		SchemaVersion: 1,
		SimulationID:  s.ID,
		Sequence:      s.sequence,
		VirtualTime:   s.virtualTime,
		Type:          t,
		Payload:       payload,
		TraceID:       s.currentTrace,
		CausationID:   s.currentCausationID,
	})
}

// findRoute computes one route and records how long it took. Route
// computations inside a matching call are not counted here; those are covered
// by the match latency metric, which times the whole matching call.
func (s *Simulation) findRoute(from, to domain.NodeID) (routing.Route, bool) {
	start := time.Now()
	route, ok := routing.FindRoute(s.City, from, to)
	s.metrics.RouteLatency().Observe(telemetry.DurationMs(time.Since(start)))
	return route, ok
}

func (s *Simulation) handlePlaceOrder(cmd PlaceOrder) {
	s.nextOrderID++
	orderID := domain.OrderID(shortID("order", s.nextOrderID))
	order := &domain.Order{
		ID:                   orderID,
		Pickup:               cmd.Pickup,
		Destination:          cmd.Destination,
		CreatedAtVirtualTime: s.virtualTime,
		Status:               domain.OrderPending,
	}
	s.orders[orderID] = order

	s.emit(domain.EventOrderPlaced, map[string]any{
		"orderId":           orderID,
		"pickupNodeId":      cmd.Pickup,
		"destinationNodeId": cmd.Destination,
	})

	// under optimized matching, queue first so contention is visible. A lone
	// pending order with an idle driver assigns immediately (same virtual
	// time as creation) — matching production systems rather than waiting a
	// tick for purity. Competing orders stay queued for the tick path.
	if s.strategy == StrategyOptimized {
		s.pendingOrders = append(s.pendingOrders, orderID)
		if len(s.pendingOrders) == 1 && s.hasIdleDriver() {
			s.dispatchImmediateBaseline()
		}
		return
	}

	s.assignBaseline(orderID, order)
}

// assignBaseline runs nearest-idle-driver matching for one order. An order it
// cannot place right now is queued rather than rejected, exactly as optimized
// matching already does - without this the two strategies differ in admission
// control as well as matching, and a comparison between them cannot attribute
// any difference to the matching algorithm.
func (s *Simulation) assignBaseline(orderID domain.OrderID, order *domain.Order) {
	start := time.Now()
	driverID, toPickup, ok := matching.Baseline(s.City, s.drivers, order.Pickup)
	computeMs := telemetry.DurationMs(time.Since(start))
	s.totalAssignmentComputeMs += computeMs
	s.metrics.MatchLatency().Observe(computeMs)

	if ok {
		s.applyAssignment(matching.Assignment{OrderID: orderID, DriverID: driverID, ToPickup: toPickup})
		return
	}

	// mirrors optimized's rule: a driver was free to consider and still could
	// not reach the pickup, so nothing will ever reach it. With no free driver
	// at all this is merely "not right now" and the order waits.
	if s.hasIdleDriver() {
		order.Status = domain.OrderUnassignable
		s.emit(domain.EventOrderUnassignable, map[string]any{
			"orderId": orderID,
			"reason":  "no available driver can reach the pickup",
		})
		return
	}
	s.pendingOrders = append(s.pendingOrders, orderID)
}

func (s *Simulation) hasIdleDriver() bool {
	for _, d := range s.drivers {
		if d.Status == domain.DriverIdle {
			return true
		}
	}
	return false
}

// retryPendingBaseline re-offers queued orders once drivers free up. Orders
// are retried in placement order so the queue stays FIFO and deterministic.
func (s *Simulation) retryPendingBaseline() {
	if len(s.pendingOrders) == 0 || !s.hasIdleDriver() {
		return
	}

	queued := s.pendingOrders
	s.pendingOrders = nil
	for _, id := range queued {
		order, ok := s.orders[id]
		if !ok || order.Status != domain.OrderPending {
			continue
		}
		// assignBaseline re-queues anything it still cannot place, so this
		// rebuilds the queue in order rather than dropping the remainder.
		s.assignBaseline(id, order)
	}
}

// runBatch matches every currently pending order (StrategyOptimized only)
// against idle drivers in one joint solve. Orders it can't place this round
// either get a definitive order.unassignable (genuinely no reachable
// driver) or simply remain pending for the next window (lost this round's
// competition to a lower-cost order, but not impossible).
func (s *Simulation) runBatch() {
	if len(s.pendingOrders) == 0 {
		return
	}
	s.batchDispatchCount++

	orders := make([]*domain.Order, 0, len(s.pendingOrders))
	for _, id := range s.pendingOrders {
		orders = append(orders, s.orders[id])
	}

	assigned, infeasible, computeMs := matching.Optimized(
		s.City, s.drivers, orders, s.driverIndex, s.candidatesPerOrder, s.costWeights, s.virtualTime,
	)
	s.totalAssignmentComputeMs += computeMs
	s.metrics.MatchLatency().Observe(computeMs)

	resolved := make(map[domain.OrderID]bool, len(assigned)+len(infeasible))
	for _, a := range assigned {
		resolved[a.OrderID] = true
		s.applyAssignment(a)
	}
	for _, id := range infeasible {
		resolved[id] = true
		order := s.orders[id]
		order.Status = domain.OrderUnassignable
		s.emit(domain.EventOrderUnassignable, map[string]any{
			"orderId": id,
			"reason":  "no available driver can reach the pickup",
		})
	}

	remaining := s.pendingOrders[:0]
	for _, id := range s.pendingOrders {
		if !resolved[id] {
			remaining = append(remaining, id)
		}
	}
	s.pendingOrders = remaining
}

// applyAssignment commits a driver-order pairing decided by either matching
// strategy: computes the destination leg, updates driver/order state, and
// emits the same event sequence regardless of which strategy produced the
// pairing.
//
// Nothing it emits is measured from the wall clock. How long the assignment
// took is real information, but it belongs in the match-latency metric, not
// in a sequenced event: an event log with a timing in it cannot be
// regenerated byte-for-byte, which is what a seeded showcase run depends on.
func (s *Simulation) applyAssignment(a matching.Assignment) {
	order := s.orders[a.OrderID]
	toDestination, ok := s.findRoute(order.Pickup, order.Destination)
	if !ok {
		order.Status = domain.OrderUnassignable
		s.emit(domain.EventOrderUnassignable, map[string]any{
			"orderId": order.ID,
			"reason":  "no path from pickup to destination",
		})
		return
	}

	driver := s.drivers[a.DriverID]
	fullRoute := append(append([]domain.NodeID{}, a.ToPickup.Nodes...), toDestination.Nodes[1:]...)
	driver.Route = fullRoute
	driver.RouteIndex = 0
	driver.Status = domain.DriverEnRouteToPick
	driver.AssignedOrder = order.ID
	if s.driverIndex != nil {
		s.driverIndex.Remove(string(a.DriverID))
	}

	order.Status = domain.OrderAssigned
	order.AssignedDriver = a.DriverID

	// a driver standing on the pickup when it is assigned has already
	// collected the order. tick only checks for pickup arrival after moving,
	// so without this the driver would run the whole delivery still marked
	// en-route-to-pickup and the order would never enter en_route.
	atPickup := driver.Position == order.Pickup
	if atPickup {
		driver.Status = domain.DriverDelivering
		order.Status = domain.OrderEnRoute
	}

	s.emit(domain.EventRouteComputed, map[string]any{
		"driverId": a.DriverID,
		"nodeIds":  fullRoute,
		"distance": a.ToPickup.Distance + toDestination.Distance,
	})
	// ETA is now a real time: routed distance divided by the driver's fixed
	// speed. Using hop count worked when every edge cost one tick, but now
	// longer roads take longer and the estimate has to reflect that.
	s.emit(domain.EventOrderAssigned, map[string]any{
		"orderId":              order.ID,
		"driverId":             a.DriverID,
		"pickupEtaVirtualTime": s.virtualTime + a.ToPickup.Distance/driverSpeed,
		"pickupDistance":       a.ToPickup.Distance,
	})
	s.emit(domain.EventDriverStatusChanged, map[string]any{
		"driverId": a.DriverID,
		"status":   driver.Status,
	})
}

// tick advances virtual time by a fixed deterministic step and moves every
// en-route driver forward at driverSpeed. A paused simulation holds its state
// and emits nothing. Drivers now travel between intersections, so a longer
// road takes more ticks than a short one.
func (s *Simulation) tick() {
	if s.paused {
		return
	}
	s.virtualTime += virtualStepPerTick

	ids := make([]domain.DriverID, 0, len(s.drivers))
	for id := range s.drivers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		s.moveDriver(id, s.drivers[id])
	}

	// both strategies reconsider queued orders after movement, since that is
	// when drivers become free. Baseline retries every tick; optimized uses
	// adaptive min-batch / max-wait rather than a fixed BatchWindow timer.
	if s.strategy == StrategyOptimized {
		s.dispatchOptimized()
	} else {
		s.retryPendingBaseline()
	}
}

// dispatchOptimized chooses between a full optimized batch and a single-order
// baseline handoff. Decisions use only virtual time, idle drivers, and queue
// depth — never wall clock. PlaceOrder may already have assigned a lone
// idle-driver order; this path covers batch, max-wait, and any lone order
// that was still pending (e.g. no idle driver at place time).
//
// Policy: batch at MinBatchSize; otherwise assign a lone order immediately
// when any driver is idle; otherwise wait up to MaxWaitVirtualTime, then
// batch whatever is queued (or baseline a single leftover).
func (s *Simulation) dispatchOptimized() {
	n := len(s.pendingOrders)
	if n == 0 {
		return
	}
	if n >= s.minBatchSize {
		s.runBatch()
		return
	}

	// one pending order and an idle driver: no reason to wait for a batch.
	if n == 1 && s.hasIdleDriver() {
		s.dispatchImmediateBaseline()
		return
	}

	oldestID := s.pendingOrders[0]
	oldest := s.orders[oldestID]
	if oldest == nil || oldest.Status != domain.OrderPending {
		s.pendingOrders = s.pendingOrders[1:]
		return
	}
	if s.virtualTime-oldest.CreatedAtVirtualTime < s.maxWaitVirtualTime {
		return
	}

	if n >= 2 {
		s.runBatch()
		return
	}
	s.dispatchImmediateBaseline()
}

func (s *Simulation) dispatchImmediateBaseline() {
	if len(s.pendingOrders) == 0 {
		return
	}
	id := s.pendingOrders[0]
	order := s.orders[id]
	s.pendingOrders = s.pendingOrders[1:]
	if order == nil || order.Status != domain.OrderPending {
		return
	}
	s.immediateDispatchCount++
	s.assignBaseline(id, order)
}

// moveDriver advances a driver by driverSpeed*virtualStepPerTick along its
// current route, consuming whole edges and possibly stopping partway along an
// edge. It emits one position update with the final interpolated x,y.
func (s *Simulation) moveDriver(id domain.DriverID, d *domain.Driver) {
	if len(d.Route) == 0 || d.RouteIndex >= len(d.Route)-1 {
		return
	}

	budget := driverSpeed * virtualStepPerTick
	for budget > 0 {
		if d.RouteIndex >= len(d.Route)-1 {
			break
		}
		next := d.Route[d.RouteIndex+1]
		edge, ok := edgeBetween(s.City, d.Position, next)
		if !ok || edge.Closed {
			break
		}
		remaining := edge.Weight - d.RouteProgress
		if budget >= remaining {
			budget -= remaining
			d.RouteProgress = 0
			d.RouteIndex++
			d.Position = next
			s.handleDriverAtNode(id, d)
		} else {
			d.RouteProgress += budget
			budget = 0
		}
	}

	s.emitDriverPosition(id, d)
}

// handleDriverAtNode checks whether the driver has reached the pickup or the
// destination and emits the matching status/lifecycle events.
func (s *Simulation) handleDriverAtNode(id domain.DriverID, d *domain.Driver) {
	order, hasOrder := s.orders[d.AssignedOrder]
	if !hasOrder {
		return
	}

	if d.Status == domain.DriverEnRouteToPick && d.Position == order.Pickup {
		d.Status = domain.DriverDelivering
		order.Status = domain.OrderEnRoute
		s.emit(domain.EventDriverStatusChanged, map[string]any{"driverId": id, "status": d.Status})
	}

	if d.RouteIndex == len(d.Route)-1 {
		order.Status = domain.OrderDelivered
		s.emit(domain.EventOrderDelivered, map[string]any{"orderId": order.ID, "driverId": id})

		d.Status = domain.DriverIdle
		d.Route = nil
		d.RouteIndex = 0
		d.RouteProgress = 0
		d.AssignedOrder = ""
		d.IdleSince = s.virtualTime
		if s.driverIndex != nil {
			pos := s.City.Nodes[d.Position]
			s.driverIndex.Set(string(id), spatial.Point{X: pos.X, Y: pos.Y})
		}
		s.emit(domain.EventDriverStatusChanged, map[string]any{"driverId": id, "status": d.Status})
	}
}

// emitDriverPosition sends the driver's current interpolated coordinates so the
// browser can render it between intersections rather than jumping node to node.
func (s *Simulation) emitDriverPosition(id domain.DriverID, d *domain.Driver) {
	x, y := s.driverPosition(d)
	s.emit(domain.EventDriverPositionUpdate, map[string]any{
		"driverId":      id,
		"nodeId":        d.Position,
		"x":             x,
		"y":             y,
		"routeIndex":    d.RouteIndex,
		"routeProgress": d.RouteProgress,
	})
}

// driverPosition interpolates the driver's current x,y between the current
// node and the next one on its route using RouteProgress.
func (s *Simulation) driverPosition(d *domain.Driver) (float64, float64) {
	if len(d.Route) == 0 || d.RouteIndex >= len(d.Route)-1 {
		n := s.City.Nodes[d.Position]
		return n.X, n.Y
	}
	from := s.City.Nodes[d.Position]
	to := s.City.Nodes[d.Route[d.RouteIndex+1]]
	edge, ok := edgeBetween(s.City, d.Position, d.Route[d.RouteIndex+1])
	if !ok || edge.Weight <= 0 {
		return from.X, from.Y
	}
	t := d.RouteProgress / edge.Weight
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return from.X + (to.X-from.X)*t, from.Y + (to.Y-from.Y)*t
}
