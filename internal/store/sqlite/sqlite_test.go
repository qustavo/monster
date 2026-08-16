package sqlite

import (
	"context"
	"testing"

	"github.com/qustavo/monster/internal/mostro"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestUpdateOrderStatusRejectsSameTimestamp guards the Store interface's
// documented contract ("must reject updates that aren't newer than the
// stored state"): an update carrying the same updated_at as what's
// already stored must not be applied, only strictly newer ones. The SQL
// guard previously used "updated_at <= ?", which let an update with an
// identical timestamp overwrite the stored row.
func TestUpdateOrderStatusRejectsSameTimestamp(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	o := &mostro.Order{
		ID:         "order-1",
		Type:       mostro.OrderTypeSell,
		FiatCode:   "USD",
		Status:     mostro.StatusPending,
		CreatedAt:  1000,
		FiatAmount: "10",
	}
	if _, err := db.CreateOrder(ctx, o); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// Same timestamp as the stored row: must be rejected, status stays
	// pending.
	if err := db.UpdateOrderStatus(ctx, "order-1", mostro.StatusCanceled, 1000); err == nil {
		t.Errorf("UpdateOrderStatus with equal timestamp: expected rejection, got nil error")
	}
	got, err := db.GetOrder(ctx, "order-1")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != mostro.StatusPending {
		t.Errorf("status after same-timestamp update = %q, want %q (update should have been rejected)", got.Status, mostro.StatusPending)
	}

	// Strictly newer timestamp: must be applied.
	if err := db.UpdateOrderStatus(ctx, "order-1", mostro.StatusCanceled, 1001); err != nil {
		t.Fatalf("UpdateOrderStatus with newer timestamp: %v", err)
	}
	got, err = db.GetOrder(ctx, "order-1")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != mostro.StatusCanceled {
		t.Errorf("status after newer-timestamp update = %q, want %q", got.Status, mostro.StatusCanceled)
	}
}
