// Package daemon is the Mostro relay-ingestion loop shared by monsterd
// and monstertui's autostarted embedded server: subscribe to order
// events, persist them, and resolve node identities.
package daemon

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/qustavo/monster/internal/api"
	"github.com/qustavo/monster/internal/mostro"
	"github.com/qustavo/monster/internal/store"
)

// FirstRunBackfill bounds how far back to fetch order history when the
// store is empty, instead of replaying the relay's entire backlog.
const FirstRunBackfill = 48 * time.Hour

// Run connects to relays, ingests Mostro order events into db, and
// broadcasts changes on hub. It blocks until ctx is canceled or the
// relay subscription ends.
func Run(ctx context.Context, relays []string, db store.Store, hub *api.Hub) error {
	pool := nostr.NewSimplePool(ctx)

	filter := nostr.Filter{
		Kinds: []int{mostro.OrderEventKind},
	}

	latest, err := db.LatestEventTime(ctx)
	if err != nil {
		return err
	}
	sinceTs := latest
	if sinceTs == 0 {
		// First run against an empty store: backfill a bounded window
		// instead of the relay's entire history.
		sinceTs = time.Now().Add(-FirstRunBackfill).Unix()
	}
	since := nostr.Timestamp(sinceTs)
	filter.Since = &since

	log.Printf("subscribing to %v for kind %d since %d", relays, mostro.OrderEventKind, sinceTs)

	knownNodes := make(map[string]bool)

	for re := range pool.SubscribeMany(ctx, relays, filter) {
		order, err := mostro.ParseOrder(re.Event)
		if err != nil {
			continue
		}
		if err := save(ctx, db, hub, order); err != nil {
			log.Printf("save order %s: %v", order.ID, err)
		}

		if !knownNodes[order.NodePubkey] {
			knownNodes[order.NodePubkey] = true
			go fetchProfile(ctx, pool, relays, db, order.NodePubkey)
		}
	}
	return nil
}

// fetchProfile resolves a Mostro node's kind-0 identity, if any, and
// caches it. Nodes that never configured profile metadata simply won't
// have one published; this is a one-shot best-effort lookup, not an
// error if nothing comes back.
func fetchProfile(ctx context.Context, pool *nostr.SimplePool, relays []string, db store.Store, pubkey string) {
	if _, err := db.GetProfile(ctx, pubkey); err == nil {
		return // already cached from a previous run
	}

	filter := nostr.Filter{
		Kinds:   []int{mostro.ProfileEventKind},
		Authors: []string{pubkey},
		Limit:   1,
	}
	for re := range pool.FetchMany(ctx, relays, filter) {
		profile, err := mostro.ParseProfile(re.Event)
		if err != nil {
			continue
		}
		if err := db.UpsertProfile(ctx, pubkey, profile, int64(re.Event.CreatedAt)); err != nil {
			log.Printf("upsert profile %s: %v", pubkey, err)
		}
	}
}

// save creates the order if it's new, otherwise applies its status, and
// broadcasts an Event on the hub for whichever happened.
func save(ctx context.Context, db store.Store, hub *api.Hub, o *mostro.Order) error {
	created, err := db.CreateOrder(ctx, o)
	if err != nil {
		return err
	}
	if created {
		hub.Publish(api.Event{Type: api.EventCreated, Order: o})
		return nil
	}

	err = db.UpdateOrderStatus(ctx, o.ID, o.Status, o.CreatedAt)
	if errors.Is(err, store.ErrNotFound) {
		// Stale event for an order already at a newer state; ignore.
		return nil
	}
	if err != nil {
		return err
	}
	hub.Publish(api.Event{Type: api.EventUpdated, Order: o})
	return nil
}
