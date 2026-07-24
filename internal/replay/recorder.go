package replay

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

// Snapshotter is the part of a simulation the recorder needs: a way to ask
// for full current state, answered on the simulation's own goroutine.
type Snapshotter interface {
	CurrentSnapshot() domain.Event
}

// RecorderConfig tunes how aggressively a simulation is persisted.
type RecorderConfig struct {
	// FlushInterval is the longest an event waits in the buffer before being
	// written. Defaults to 500ms.
	FlushInterval time.Duration
	// BatchSize forces an early flush once this many events are buffered.
	// Defaults to 200.
	BatchSize int
	// SnapshotEvery writes a full snapshot after this many persisted events,
	// so replay can start near a target rather than folding from zero.
	// Defaults to 500.
	SnapshotEvery int
	// BufferSize bounds the recorder's intake queue. Past it, events are
	// dropped rather than allowed to stall the simulation goroutine.
	// Defaults to 4096.
	BufferSize int

	Store       store.Store
	Snapshotter Snapshotter
	Metrics     *telemetry.Metrics
	Logger      *slog.Logger
}

func (c *RecorderConfig) withDefaults() {
	if c.FlushInterval <= 0 {
		c.FlushInterval = 500 * time.Millisecond
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 200
	}
	if c.SnapshotEvery <= 0 {
		c.SnapshotEvery = 500
	}
	if c.BufferSize <= 0 {
		c.BufferSize = 4096
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Recorder persists one simulation's event stream. It sits between the
// simulation and the WebSocket hub: it forwards every event onward
// unchanged and writes it to the store in batches, because a database
// transaction per animation frame is exactly what the persistence rules
// forbid.
type Recorder struct {
	cfg          RecorderConfig
	simulationID string

	// snapshotDue is a one-slot signal, so a snapshot request never blocks the
	// forwarding loop and requests cannot pile up.
	snapshotDue chan struct{}
	done        chan struct{}
}

// NewRecorder returns a recorder for one simulation. A nil store disables
// recording entirely; Tap then just forwards.
func NewRecorder(simulationID string, cfg RecorderConfig) *Recorder {
	cfg.withDefaults()
	return &Recorder{
		cfg:          cfg,
		simulationID: simulationID,
		snapshotDue:  make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
}

// Tap forwards in to the returned channel while persisting what passes
// through. The returned channel closes once in does and the final batch has
// been written. ctx cancellation stops the writer, not the forwarding: the
// stream keeps working with persistence turned off rather than stalling a
// live demo on a database problem.
func (r *Recorder) Tap(ctx context.Context, in <-chan domain.Event) <-chan domain.Event {
	out := make(chan domain.Event, cap(in))
	if r.cfg.Store == nil {
		go func() {
			defer close(r.done)
			forward(in, out)
		}()
		return out
	}

	queue := make(chan domain.Event, r.cfg.BufferSize)
	go r.forwardAndQueue(in, out, queue)
	go r.writeLoop(ctx, queue)
	go r.snapshotLoop(ctx)
	return out
}

// Done reports when the writer has flushed everything and exited.
func (r *Recorder) Done() <-chan struct{} { return r.done }

func forward(in <-chan domain.Event, out chan<- domain.Event) {
	for event := range in {
		out <- event
	}
	close(out)
}

// forwardAndQueue never blocks on the persistence queue. A full queue means
// the database is slower than the simulation, and dropping an event there is
// far better than stalling the actor loop that feeds every live viewer.
func (r *Recorder) forwardAndQueue(in <-chan domain.Event, out chan<- domain.Event, queue chan<- domain.Event) {
	for event := range in {
		select {
		case queue <- event:
		default:
			r.cfg.Metrics.DroppedUpdates().Inc()
			r.cfg.Logger.Warn("dropped an event before persistence",
				"simulation_id", r.simulationID, "sequence", event.Sequence)
		}
		out <- event
	}
	close(queue)
	close(out)
}

func (r *Recorder) writeLoop(ctx context.Context, queue <-chan domain.Event) {
	defer close(r.done)

	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]store.Event, 0, r.cfg.BatchSize)
	sinceSnapshot := 0

	flush := func() {
		if len(batch) == 0 {
			return
		}
		written := r.write(ctx, batch)
		batch = batch[:0]
		sinceSnapshot += written
		if sinceSnapshot >= r.cfg.SnapshotEvery {
			sinceSnapshot = 0
			r.requestSnapshot()
		}
	}

	for {
		select {
		case event, ok := <-queue:
			if !ok {
				flush()
				return
			}
			record, err := store.EventFrom(event, event.TraceID, time.Now().UTC())
			if err != nil {
				r.cfg.Metrics.PersistenceErrors().Inc()
				r.cfg.Logger.Error("could not encode an event for persistence",
					"simulation_id", r.simulationID, "sequence", event.Sequence, "error", err)
				continue
			}
			batch = append(batch, record)
			if len(batch) >= r.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}

// write persists one batch and reports how many events it contained, or 0 if
// the write failed. A failure is counted and logged rather than retried: the
// simulation is the source of truth for a live viewer, and blocking on a
// database retry would degrade the demo to fix the archive.
func (r *Recorder) write(ctx context.Context, batch []store.Event) int {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	spanCtx, span := telemetry.StartSpan(telemetry.WithLogger(writeCtx, r.cfg.Logger), "store.append_events",
		slog.String("simulation_id", r.simulationID), slog.Int("events", len(batch)))
	defer span.End()

	if err := r.cfg.Store.AppendEvents(spanCtx, batch); err != nil {
		span.RecordError(err)
		r.cfg.Metrics.PersistenceErrors().Inc()
		r.cfg.Logger.Error("could not persist an event batch",
			"simulation_id", r.simulationID, "events", len(batch), "error", err)
		return 0
	}

	r.cfg.Metrics.EventsPersisted().Add(float64(len(batch)))
	return len(batch)
}

func (r *Recorder) requestSnapshot() {
	select {
	case r.snapshotDue <- struct{}{}:
	default:
		// a snapshot is already pending; another one adds nothing.
	}
}

// snapshotLoop runs in its own goroutine because asking the simulation for a
// snapshot blocks on its actor loop, and that loop can itself be blocked
// publishing to the recorder. Keeping the request off the write path is what
// keeps the two from deadlocking.
func (r *Recorder) snapshotLoop(ctx context.Context) {
	if r.cfg.Snapshotter == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.snapshotDue:
			r.writeSnapshot(ctx)
		}
	}
}

func (r *Recorder) writeSnapshot(ctx context.Context) {
	event := r.cfg.Snapshotter.CurrentSnapshot()

	payload, err := json.Marshal(event.Payload)
	if err != nil {
		r.cfg.Metrics.PersistenceErrors().Inc()
		r.cfg.Logger.Error("could not encode a snapshot",
			"simulation_id", r.simulationID, "error", err)
		return
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	spanCtx, span := telemetry.StartSpan(telemetry.WithLogger(writeCtx, r.cfg.Logger), "store.save_snapshot",
		slog.String("simulation_id", r.simulationID), slog.Int("sequence", event.Sequence))
	defer span.End()

	snapshot := store.Snapshot{
		SimulationID: r.simulationID,
		Sequence:     event.Sequence,
		VirtualTime:  event.VirtualTime,
		Payload:      payload,
		RecordedAt:   time.Now().UTC(),
	}
	if err := r.cfg.Store.SaveSnapshot(spanCtx, snapshot); err != nil {
		span.RecordError(err)
		r.cfg.Metrics.PersistenceErrors().Inc()
		r.cfg.Logger.Error("could not persist a snapshot",
			"simulation_id", r.simulationID, "sequence", event.Sequence, "error", err)
		return
	}
	r.cfg.Metrics.SnapshotsWritten().Inc()
}
