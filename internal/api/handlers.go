// Package api exposes the Mostro order book over HTTP: a list endpoint
// and an SSE stream for live order/status changes.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/qustavo/monster/internal/mostro"
	"github.com/qustavo/monster/internal/store"
)

// NewMux builds the HTTP routes backed by db and hub.
func NewMux(db store.Store, hub *Hub) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders", listOrders(db))
	mux.HandleFunc("GET /orders/stream", streamOrders(db, hub))
	return mux
}

// listOrders handles GET /orders?type=&status=&fiat_code=
func listOrders(db store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter := store.OrderFilter{
			Type:     mostro.OrderType(q.Get("type")),
			Status:   mostro.OrderStatus(q.Get("status")),
			FiatCode: q.Get("fiat_code"),
		}

		orders, err := db.ListOrders(r.Context(), filter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if orders == nil {
			orders = []*mostro.Order{}
		}
		if err := attachNodeNames(r.Context(), db, orders); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(orders)
	}
}

// attachNodeNames resolves each order's Mostro node profile (kind-0,
// keyed by NodePubkey) and sets Order.NodeName when known, in a single
// batched lookup.
func attachNodeNames(ctx context.Context, db store.Store, orders []*mostro.Order) error {
	pubkeys := make([]string, 0, len(orders))
	seen := make(map[string]bool, len(orders))
	for _, o := range orders {
		if !seen[o.NodePubkey] {
			seen[o.NodePubkey] = true
			pubkeys = append(pubkeys, o.NodePubkey)
		}
	}

	profiles, err := db.GetProfiles(ctx, pubkeys)
	if err != nil {
		return fmt.Errorf("resolve node profiles: %w", err)
	}
	for _, o := range orders {
		if p, ok := profiles[o.NodePubkey]; ok {
			o.NodeName = p.Name
		}
	}
	return nil
}

// streamOrders handles GET /orders/stream, an SSE feed of Events as
// orders are created or change status.
func streamOrders(db store.Store, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ch, cancel := hub.Subscribe()
		defer cancel()

		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				// The hub broadcasts one shared *mostro.Order pointer
				// to every subscriber; copy before mutating so
				// concurrent SSE connections can't race on it.
				orderCopy := *ev.Order
				if p, err := db.GetProfile(r.Context(), orderCopy.NodePubkey); err == nil {
					orderCopy.NodeName = p.Name
				}
				ev.Order = &orderCopy
				b, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "event: order\ndata: %s\n\n", b)
				flusher.Flush()
			}
		}
	}
}
