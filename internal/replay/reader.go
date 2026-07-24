package replay

import (
	"context"
	"errors"
	"fmt"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/store"
)

// DefaultPageSize bounds how many events one replay request returns. A
// showcase run is a few thousand events, so the default fetches a whole run
// in a single request while still refusing to stream an unbounded log.
const DefaultPageSize = 5000

// maxPageSize caps what a caller can ask for.
const maxPageSize = 20000

// ErrNotFound means the simulation has neither a metadata row nor any
// persisted events.
var ErrNotFound = errors.New("replay not found")

// Log is one page of a simulation's persisted history, plus enough metadata
// for a client to render and scrub it.
type Log struct {
	Simulation     store.Simulation `json:"simulation"`
	Events         []domain.Event   `json:"events"`
	FromSequence   int              `json:"fromSequence"`
	LatestSequence int              `json:"latestSequence"`
	HasMore        bool             `json:"hasMore"`
}

// Reader serves persisted history out of a store.
type Reader struct {
	store store.Store
}

// NewReader returns a reader over the given store.
func NewReader(s store.Store) *Reader {
	return &Reader{store: s}
}

// Load returns the events after fromSequence, in order. The event log always
// opens with the simulation.snapshot the run emitted at sequence 1, so a
// client that starts at fromSequence 0 needs nothing else to render.
func (r *Reader) Load(ctx context.Context, simulationID string, fromSequence, limit int) (Log, error) {
	sim, err := r.simulation(ctx, simulationID)
	if err != nil {
		return Log{}, err
	}

	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if fromSequence < 0 {
		fromSequence = 0
	}

	// one extra row tells us whether another page exists without a count query.
	records, err := r.store.Events(ctx, simulationID, fromSequence, limit+1)
	if err != nil {
		return Log{}, fmt.Errorf("read events: %w", err)
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}

	latest, err := r.store.LatestSequence(ctx, simulationID)
	if err != nil {
		return Log{}, fmt.Errorf("read latest sequence: %w", err)
	}

	events := make([]domain.Event, 0, len(records))
	for _, record := range records {
		event, err := record.Domain()
		if err != nil {
			return Log{}, fmt.Errorf("decode event %d: %w", record.Sequence, err)
		}
		events = append(events, event)
	}

	return Log{
		Simulation:     sim,
		Events:         events,
		FromSequence:   fromSequence,
		LatestSequence: latest,
		HasMore:        hasMore,
	}, nil
}

// StateAt reconstructs state at a sequence number: it starts from the newest
// snapshot at or before the target and folds the events between forward,
// which is the whole reason snapshots are written periodically.
func (r *Reader) StateAt(ctx context.Context, simulationID string, sequence int) (State, error) {
	if _, err := r.simulation(ctx, simulationID); err != nil {
		return State{}, err
	}

	if sequence <= 0 {
		latest, err := r.store.LatestSequence(ctx, simulationID)
		if err != nil {
			return State{}, fmt.Errorf("read latest sequence: %w", err)
		}
		sequence = latest
	}

	var base *store.Snapshot
	snapshot, err := r.store.SnapshotAtOrBefore(ctx, simulationID, sequence)
	switch {
	case err == nil:
		base = &snapshot
	case errors.Is(err, store.ErrNotFound):
		// no snapshot yet: fold the log from the beginning instead.
	default:
		return State{}, fmt.Errorf("read snapshot: %w", err)
	}

	from := 0
	if base != nil {
		from = base.Sequence
	}

	var events []store.Event
	for {
		page, err := r.store.Events(ctx, simulationID, from, maxPageSize)
		if err != nil {
			return State{}, fmt.Errorf("read events: %w", err)
		}
		if len(page) == 0 {
			break
		}
		events = append(events, page...)
		from = page[len(page)-1].Sequence
		if from >= sequence {
			break
		}
	}

	return StateAt(simulationID, base, events, sequence)
}

// simulation resolves the metadata row, tolerating a run whose events were
// persisted before its metadata row was (or whose row was pruned) by
// synthesizing a minimal record from the log itself.
func (r *Reader) simulation(ctx context.Context, id string) (store.Simulation, error) {
	sim, err := r.store.GetSimulation(ctx, id)
	if err == nil {
		return sim, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Simulation{}, fmt.Errorf("read simulation: %w", err)
	}

	latest, seqErr := r.store.LatestSequence(ctx, id)
	if seqErr != nil {
		return store.Simulation{}, fmt.Errorf("read latest sequence: %w", seqErr)
	}
	if latest == 0 {
		return store.Simulation{}, ErrNotFound
	}
	return store.Simulation{ID: id}, nil
}
