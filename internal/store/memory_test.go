package store_test

import (
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/store"
	"dispatchlab/internal/store/storetest"
)

func TestMemoryStoreConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return store.NewMemory()
	})
}

func TestEventConversionRoundTrip(t *testing.T) {
	original := domain.Event{
		SchemaVersion: 1,
		SimulationID:  "sim-1",
		Sequence:      7,
		VirtualTime:   3.5,
		Type:          domain.EventOrderAssigned,
		Payload: map[string]any{
			"orderId":  "order-1",
			"driverId": "driver-2",
		},
	}

	record, err := store.EventFrom(original, "trace-abc", time.Now())
	if err != nil {
		t.Fatalf("EventFrom: %v", err)
	}
	if record.TraceID != "trace-abc" {
		t.Errorf("trace id = %q", record.TraceID)
	}

	back, err := record.Domain()
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}
	if back.SimulationID != original.SimulationID || back.Sequence != original.Sequence ||
		back.VirtualTime != original.VirtualTime || back.Type != original.Type {
		t.Fatalf("envelope changed across a round trip: %+v", back)
	}
	if back.TraceID != "trace-abc" {
		t.Errorf("trace id lost on the way back: %q", back.TraceID)
	}

	payload, ok := back.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload decoded as %T, want map", back.Payload)
	}
	if payload["orderId"] != "order-1" || payload["driverId"] != "driver-2" {
		t.Errorf("payload changed across a round trip: %v", payload)
	}
}

func TestEventConversionRejectsUnmarshalablePayload(t *testing.T) {
	_, err := store.EventFrom(domain.Event{Payload: make(chan int)}, "", time.Now())
	if err == nil {
		t.Fatal("expected an error for a payload that cannot be marshalled")
	}
}
