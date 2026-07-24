package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/simulation"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

// ShowcaseRun is a curated run the server provisions itself. Its id is fixed
// and its content is a pure function of its seed and script, so
// /replay/<id> is a URL that can be put in a README and still work a year
// later — regenerated identically on a fresh database.
type ShowcaseRun struct {
	ID      string
	Title   string
	Seed    int64
	Drivers int
	// Orders is how many of the seeded scenario's arrivals to place.
	Orders int
	// CloseRoadAfter is the virtual time at which a road on some driver's
	// active route is closed, so the run contains the reroute the demo is
	// built around. Zero closes nothing.
	CloseRoadAfter float64
	// Ticks is how long the run is stepped for.
	Ticks int
}

// DefaultShowcaseRuns are the runs every deployment provisions. Two of them:
// one plain delivery run, and one where a closure forces a live reroute.
func DefaultShowcaseRuns() []ShowcaseRun {
	return []ShowcaseRun{
		{
			ID:      "showcase-first-delivery",
			Title:   "A single delivery, start to finish",
			Seed:    42,
			Drivers: 8,
			Orders:  3,
			Ticks:   60,
		},
		{
			ID:             "showcase-road-closure",
			Title:          "A closure forces a live reroute",
			Seed:           7,
			Drivers:        10,
			Orders:         6,
			CloseRoadAfter: 4,
			Ticks:          80,
		},
	}
}

// ProvisionShowcases makes sure every curated run exists in the store,
// generating any that do not. It is safe to call on every start: a run that
// is already there is left alone rather than regenerated, so its event log
// stays byte-identical across deployments.
func ProvisionShowcases(ctx context.Context, s store.Store, runs []ShowcaseRun, metrics *telemetry.Metrics, logger *slog.Logger) error {
	if s == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	for _, run := range runs {
		existing, err := s.GetSimulation(ctx, run.ID)
		if err == nil && existing.Showcase {
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("check showcase %s: %w", run.ID, err)
		}

		if err := generateShowcase(ctx, s, run); err != nil {
			metrics.PersistenceErrors().Inc()
			return fmt.Errorf("provision showcase %s: %w", run.ID, err)
		}
		logger.Info("provisioned a showcase run", "simulation_id", run.ID, "seed", run.Seed)
	}
	return nil
}

// generateShowcase replays a scripted scenario headlessly and writes the
// resulting log. Nothing here touches wall-clock time, so the same script
// produces the same events on any machine.
func generateShowcase(ctx context.Context, s store.Store, run ShowcaseRun) error {
	sim := simulation.New(run.ID, run.Seed, run.Drivers)

	var events []domain.Event
	events = append(events, sim.Start()...)

	scenario := DefaultScenario(run.Seed, run.Drivers)
	arrivals := scenario.Arrivals
	if run.Orders < len(arrivals) {
		arrivals = arrivals[:run.Orders]
	}

	next := 0
	closed := run.CloseRoadAfter <= 0
	for tick := 0; tick < run.Ticks; tick++ {
		now := float64(tick)
		for next < len(arrivals) && arrivals[next].VirtualTime <= now {
			a := arrivals[next]
			events = append(events, sim.Apply(simulation.PlaceOrder{
				Pickup: a.Pickup, Destination: a.Destination,
			})...)
			next++
		}

		if !closed && now >= run.CloseRoadAfter {
			if edge, ok := edgeOnSomeRoute(sim); ok {
				events = append(events, sim.Apply(simulation.CloseRoad{EdgeID: edge})...)
			}
			closed = true
		}

		events = append(events, sim.Advance()...)
	}

	now := time.Now().UTC()
	sim0 := store.Simulation{
		ID:        run.ID,
		Seed:      run.Seed,
		Drivers:   run.Drivers,
		Strategy:  string(simulation.StrategyBaseline),
		CreatedAt: now,
		Showcase:  true,
		// no owner and no expiry: these belong to the server, not a visitor,
		// and are never swept.
		CompletedAt: &now,
	}
	if err := s.CreateSimulation(ctx, sim0); err != nil {
		return err
	}

	records := make([]store.Event, 0, len(events))
	for _, e := range events {
		record, err := store.EventFrom(e, "", now)
		if err != nil {
			return err
		}
		records = append(records, record)
	}
	if err := s.AppendEvents(ctx, records); err != nil {
		return err
	}

	final := sim.Snapshot()
	payload, err := json.Marshal(final.Payload)
	if err != nil {
		return err
	}
	return s.SaveSnapshot(ctx, store.Snapshot{
		SimulationID: run.ID,
		Sequence:     final.Sequence,
		VirtualTime:  final.VirtualTime,
		Payload:      payload,
		RecordedAt:   now,
	})
}

// edgeOnSomeRoute finds an edge a driver is about to travel, so the closure
// actually invalidates a route rather than landing on an unused road.
func edgeOnSomeRoute(sim *simulation.Simulation) (domain.EdgeID, bool) {
	var state struct {
		Drivers []struct {
			Route      []domain.NodeID `json:"route"`
			RouteIndex int             `json:"routeIndex"`
		} `json:"drivers"`
	}

	raw, err := json.Marshal(sim.Snapshot().Payload)
	if err != nil {
		return "", false
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", false
	}

	for _, d := range state.Drivers {
		if len(d.Route) < d.RouteIndex+2 {
			continue
		}
		from, to := d.Route[d.RouteIndex], d.Route[d.RouteIndex+1]
		for _, edge := range sim.City.Edges[from] {
			if edge.To == to {
				return edge.ID, true
			}
		}
	}
	return "", false
}
