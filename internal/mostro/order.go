// Package mostro decodes Mostro P2P order events (NIP-69, kind 38383).
package mostro

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/nbd-wtf/go-nostr"
)

// OrderEventKind is the addressable Nostr event kind Mostro uses to
// publish order books (NIP-69).
const OrderEventKind = 38383

// ProfileEventKind is the standard NIP-01 metadata event kind. A Mostro
// node publishes one under its own pubkey (the same pubkey that signs
// its order events) to advertise a human-readable identity.
const ProfileEventKind = 0

type OrderType string

const (
	OrderTypeBuy  OrderType = "buy"
	OrderTypeSell OrderType = "sell"
)

type OrderStatus string

const (
	StatusPending    OrderStatus = "pending"
	StatusCanceled   OrderStatus = "canceled"
	StatusInProgress OrderStatus = "in-progress"
	StatusSuccess    OrderStatus = "success"
	StatusExpired    OrderStatus = "expired"
)

// Rating is the maker's reputation, embedded as a JSON string in the
// "rating" tag. The protocol leaves the calculation method up to each
// platform; Mostro itself doesn't define what "days" counts (order docs
// don't say), so treat it as an opaque platform-reported figure.
type Rating struct {
	TotalReviews int     `json:"total_reviews"`
	TotalRating  float64 `json:"total_rating"`
	LastRating   int     `json:"last_rating"`
	MaxRate      int     `json:"max_rate"`
	MinRate      int     `json:"min_rate"`
	Days         int     `json:"days"`
}

// Profile is a Mostro node's identity, decoded from the content of its
// kind-0 event (NIP-01 metadata: name/about/picture/website).
type Profile struct {
	Name    string `json:"name"`
	About   string `json:"about"`
	Picture string `json:"picture"`
	Website string `json:"website"`
}

// ParseProfile decodes a kind-0 event's JSON content into a Profile.
func ParseProfile(ev *nostr.Event) (*Profile, error) {
	if ev.Kind != ProfileEventKind {
		return nil, fmt.Errorf("mostro: event kind %d is not a profile event (want %d)", ev.Kind, ProfileEventKind)
	}
	var p Profile
	if err := json.Unmarshal([]byte(ev.Content), &p); err != nil {
		return nil, fmt.Errorf("mostro: parse profile content: %w", err)
	}
	return &p, nil
}

// Order is a decoded view of a kind-38383 event's tags. NodePubkey is
// the Mostro node's own signing key (ev.pubkey) — Mostro publishes
// orders on behalf of its users, so this identifies the trading node,
// not an individual trader. The same pubkey signs that node's kind-0
// profile event.
type Order struct {
	ID             string
	NodePubkey     string
	NodeName       string // enrichment, not from the order event: resolved from NodePubkey's kind-0 Profile.Name, if known
	CreatedAt      int64
	Type           OrderType
	FiatCode       string
	Status         OrderStatus
	AmountSats     int64 // 0 means market price
	FiatAmount     string // set when the order has a fixed amount
	MinAmount      string // set instead of FiatAmount for range orders
	MaxAmount      string
	PaymentMethods []string
	Premium        string
	ExpiresAt      int64
	Platform       string // software identifier, e.g. "mostro", "Bitblik" (y tag, 2nd element)
	InstanceName   string // operator-chosen display name, mostro-specific (y tag, 3rd element, if present)
	Source         string
	Rating         *Rating
	Network        string
	Layer          string
	Name           string // maker's own display name (name tag), distinct from NodeName
	Geohash        string
	Bond           string

	Raw *nostr.Event `json:"-"`
}

// ParseOrder decodes a kind-38383 event into an Order. It returns an
// error if the event isn't an order event or is missing a required tag.
func ParseOrder(ev *nostr.Event) (*Order, error) {
	if ev.Kind != OrderEventKind {
		return nil, fmt.Errorf("mostro: event kind %d is not an order event (want %d)", ev.Kind, OrderEventKind)
	}

	o := &Order{
		NodePubkey: ev.PubKey,
		CreatedAt:  int64(ev.CreatedAt),
		Raw:        ev,
	}

	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		key, val := tag[0], tag[1]
		switch key {
		case "d":
			o.ID = val
		case "k":
			o.Type = OrderType(val)
		case "f":
			o.FiatCode = val
		case "s":
			o.Status = OrderStatus(val)
		case "amt":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("mostro: order event %s has malformed \"amt\" tag %q: %w", ev.ID, val, err)
			}
			o.AmountSats = n
		case "fa":
			if len(tag) >= 3 {
				o.MinAmount = tag[1]
				o.MaxAmount = tag[2]
			} else {
				o.FiatAmount = val
			}
		case "pm":
			o.PaymentMethods = tag[1:]
		case "premium":
			o.Premium = val
		case "expires_at":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("mostro: order event %s has malformed \"expires_at\" tag %q: %w", ev.ID, val, err)
			}
			o.ExpiresAt = n
		case "y":
			o.Platform = val
			if len(tag) >= 3 {
				o.InstanceName = tag[2]
			}
		case "source":
			o.Source = val
		case "rating":
			if r := parseRating(val); r != nil {
				o.Rating = r
			}
		case "network":
			o.Network = val
		case "layer":
			o.Layer = val
		case "name":
			o.Name = val
		case "g":
			o.Geohash = val
		case "bond":
			o.Bond = val
		}
	}

	if o.ID == "" {
		return nil, fmt.Errorf("mostro: order event %s missing required \"d\" tag", ev.ID)
	}

	return o, nil
}

// parseRating decodes a "rating" tag value. NIP-69 documents it as a
// bare object (`{"total_reviews":1,...}`), but real Mostro instances
// publish it wrapped as a 2-element array (`["rating",{"total_reviews":1,...}]`);
// handle both.
func parseRating(val string) *Rating {
	var r Rating
	if err := json.Unmarshal([]byte(val), &r); err == nil {
		return &r
	}

	var wrapped []json.RawMessage
	if err := json.Unmarshal([]byte(val), &wrapped); err != nil || len(wrapped) < 2 {
		return nil
	}
	if err := json.Unmarshal(wrapped[1], &r); err != nil {
		return nil
	}
	return &r
}
