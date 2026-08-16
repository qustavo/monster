package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/qustavo/monster/internal/api"
	"github.com/qustavo/monster/internal/mostro"
	"github.com/qustavo/monster/internal/store/sqlite"
)

// TestSaveBroadcastsOriginalCreatedAt guards against a republished order
// (same id, newer event timestamp, same or unchanged status — a routine
// Mostro keep-alive, not a new order) making clients think it's brand
// new. save() must broadcast the order's stored/original CreatedAt on
// an update, not the triggering event's own timestamp.
func TestSaveBroadcastsOriginalCreatedAt(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	hub := api.NewHub()
	ch, cancel := hub.Subscribe()
	t.Cleanup(cancel)

	original := &mostro.Order{
		ID: "order-1", Type: mostro.OrderTypeSell, FiatCode: "USD",
		Status: mostro.StatusPending, CreatedAt: 1000, FiatAmount: "10",
	}
	if err := save(ctx, db, hub, original); err != nil {
		t.Fatalf("save (create): %v", err)
	}
	drainEvent(t, ch, api.EventCreated, 1000)

	// A later event for the same order (republish/keep-alive): newer
	// timestamp, same status.
	republished := &mostro.Order{
		ID: "order-1", Type: mostro.OrderTypeSell, FiatCode: "USD",
		Status: mostro.StatusPending, CreatedAt: 2000, FiatAmount: "10",
	}
	if err := save(ctx, db, hub, republished); err != nil {
		t.Fatalf("save (update): %v", err)
	}
	drainEvent(t, ch, api.EventUpdated, 1000)
}

func drainEvent(t *testing.T, ch chan api.Event, wantType api.EventType, wantCreatedAt int64) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != wantType {
			t.Errorf("event type = %q, want %q", ev.Type, wantType)
		}
		if ev.Order.CreatedAt != wantCreatedAt {
			t.Errorf("broadcast Order.CreatedAt = %d, want %d (original creation time, not the triggering event's own timestamp)",
				ev.Order.CreatedAt, wantCreatedAt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hub event")
	}
}
