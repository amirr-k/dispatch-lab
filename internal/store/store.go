// Package store defines how simulations, their event logs, their periodic
// snapshots, and comparison results are persisted. The interface is small on
// purpose: the only queries the backend needs are "append these events",
// "give me the events after N", and "give me the newest snapshot at or before
// N", which is exactly what replay reconstruction runs on.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"dispatchlab/internal/domain"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Simulation is the metadata row for one simulation run.
type Simulation struct {
	ID          string     `json:"id"`
	Seed        int64      `json:"seed"`
	Drivers     int        `json:"drivers"`
	Strategy    string     `json:"strategy"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	// Showcase marks a run as permanently retained so its /replay/:id URL
	// stays valid. Anonymous guest runs are not retained.
	Showcase bool `json:"showcase"`
}

// Event is one persisted simulation event. The payload is kept as raw JSON:
// the store never needs to interpret it, and keeping it opaque means a new
// event type needs no migration.
type Event struct {
	SimulationID string           `json:"simulationId"`
	Sequence     int              `json:"sequence"`
	VirtualTime  float64          `json:"virtualTime"`
	Type         domain.EventType `json:"type"`
	Payload      json.RawMessage  `json:"payload"`
	TraceID      string           `json:"traceId,omitempty"`
	RecordedAt   time.Time        `json:"recordedAt"`
}

// Snapshot is full simulation state as of a sequence number, written
// periodically so replay can start near a target instead of at sequence zero.
type Snapshot struct {
	SimulationID string          `json:"simulationId"`
	Sequence     int             `json:"sequence"`
	VirtualTime  float64         `json:"virtualTime"`
	Payload      json.RawMessage `json:"payload"`
	RecordedAt   time.Time       `json:"recordedAt"`
}

// Comparison is a stored algorithm-comparison result, kept as raw JSON for
// the same reason event payloads are.
type Comparison struct {
	ID        string          `json:"id"`
	Seed      int64           `json:"seed"`
	Drivers   int             `json:"drivers"`
	Result    json.RawMessage `json:"result"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Store is the persistence contract. Every method must be safe for
// concurrent use.
type Store interface {
	CreateSimulation(ctx context.Context, sim Simulation) error
	GetSimulation(ctx context.Context, id string) (Simulation, error)
	// MarkShowcase retains a run permanently and stamps its completion time,
	// which is what gives it a stable replay URL.
	MarkShowcase(ctx context.Context, id string, completedAt time.Time) error

	AppendEvents(ctx context.Context, events []Event) error
	// Events returns up to limit events with a sequence strictly greater than
	// fromSequence, in sequence order.
	Events(ctx context.Context, simulationID string, fromSequence, limit int) ([]Event, error)
	// LatestSequence returns the highest persisted sequence for a simulation,
	// or 0 when it has no events yet.
	LatestSequence(ctx context.Context, simulationID string) (int, error)

	SaveSnapshot(ctx context.Context, snapshot Snapshot) error
	// SnapshotAtOrBefore returns the newest snapshot with a sequence at or
	// below the target, the starting point replay reconstructs forward from.
	SnapshotAtOrBefore(ctx context.Context, simulationID string, sequence int) (Snapshot, error)

	SaveComparison(ctx context.Context, comparison Comparison) error
	GetComparison(ctx context.Context, id string) (Comparison, error)

	Close() error
}

// EventFrom converts a domain event into its persisted form, marshalling the
// payload once at the boundary.
func EventFrom(e domain.Event, traceID string, recordedAt time.Time) (Event, error) {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return Event{}, err
	}
	return Event{
		SimulationID: e.SimulationID,
		Sequence:     e.Sequence,
		VirtualTime:  e.VirtualTime,
		Type:         e.Type,
		Payload:      payload,
		TraceID:      traceID,
		RecordedAt:   recordedAt,
	}, nil
}

// Domain converts a persisted event back into the wire/domain form, decoding
// the payload into a generic structure since the original Go type is not
// recoverable and no consumer needs it.
func (e Event) Domain() (domain.Event, error) {
	var payload any
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return domain.Event{}, err
		}
	}
	return domain.Event{
		SchemaVersion: 1,
		SimulationID:  e.SimulationID,
		Sequence:      e.Sequence,
		VirtualTime:   e.VirtualTime,
		Type:          e.Type,
		Payload:       payload,
		TraceID:       e.TraceID,
	}, nil
}
