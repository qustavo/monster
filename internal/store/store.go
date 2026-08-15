// Package store defines the order persistence interface. Concrete
// backends live in subpackages (e.g. internal/store/sqlite).
package store

import (
	"context"
	"errors"

	"github.com/qustavo/monster/internal/mostro"
)

// ErrNotFound is returned when an order lookup or update targets an id
// that doesn't exist in the store.
var ErrNotFound = errors.New("store: order not found")

// OrderFilter narrows ListOrders results. Zero-value fields are
// unfiltered.
type OrderFilter struct {
	Type     mostro.OrderType
	Status   mostro.OrderStatus
	FiatCode string
}

// Store persists and serves Mostro orders.
type Store interface {
	// CreateOrder inserts a new order and reports whether it was
	// actually inserted; if an order with the same id already exists,
	// created is false and no error is returned.
	CreateOrder(ctx context.Context, o *mostro.Order) (created bool, err error)

	// UpdateOrderStatus sets an order's status. updatedAt should be the
	// triggering event's timestamp; implementations must reject updates
	// that aren't newer than the stored state, returning ErrNotFound if
	// the order doesn't exist or the update is stale.
	UpdateOrderStatus(ctx context.Context, id string, status mostro.OrderStatus, updatedAt int64) error

	// GetOrder retrieves a single order by id, or ErrNotFound.
	GetOrder(ctx context.Context, id string) (*mostro.Order, error)

	// ListOrders retrieves orders matching filter, newest first.
	ListOrders(ctx context.Context, filter OrderFilter) ([]*mostro.Order, error)

	// LatestEventTime returns the timestamp of the most recently
	// recorded event, or 0 if the store is empty.
	LatestEventTime(ctx context.Context) (int64, error)

	// UpsertProfile stores a node's kind-0 profile. updatedAt is the
	// triggering event's timestamp; like UpdateOrderStatus, updates
	// older than what's stored are ignored (kind 0 is itself a
	// replaceable-per-pubkey event upstream).
	UpsertProfile(ctx context.Context, pubkey string, p *mostro.Profile, updatedAt int64) error

	// GetProfile retrieves a single node's profile, or ErrNotFound.
	GetProfile(ctx context.Context, pubkey string) (*mostro.Profile, error)

	// GetProfiles retrieves known profiles for the given pubkeys in one
	// call. Pubkeys with no known profile are simply absent from the
	// result map (not an error).
	GetProfiles(ctx context.Context, pubkeys []string) (map[string]*mostro.Profile, error)

	Close() error
}
