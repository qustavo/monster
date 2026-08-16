package mostro

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func baseOrderEvent(extraTags ...[]string) *nostr.Event {
	tags := nostr.Tags{
		nostr.Tag{"d", "order-1"},
		nostr.Tag{"k", "sell"},
		nostr.Tag{"f", "USD"},
	}
	for _, t := range extraTags {
		tags = append(tags, nostr.Tag(t))
	}
	return &nostr.Event{
		ID:   "event-1",
		Kind: OrderEventKind,
		Tags: tags,
	}
}

// TestParseOrderMalformedNumericTagsFail guards against Sscanf's old
// behavior of silently leaving AmountSats/ExpiresAt at 0 on a malformed
// value, indistinguishable from a legitimately-zero amount. ParseOrder
// must now fail loudly instead of fabricating a zero.
func TestParseOrderMalformedNumericTagsFail(t *testing.T) {
	tests := []struct {
		name string
		tag  []string
	}{
		{"malformed amt", []string{"amt", "not-a-number"}},
		{"malformed expires_at", []string{"expires_at", "soon"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOrder(baseOrderEvent(tt.tag))
			if err == nil {
				t.Errorf("ParseOrder with %v: expected error, got nil", tt.tag)
			}
		})
	}
}

func TestParseOrderValidNumericTags(t *testing.T) {
	ev := baseOrderEvent([]string{"amt", "1500"}, []string{"expires_at", "1700000000"})
	o, err := ParseOrder(ev)
	if err != nil {
		t.Fatalf("ParseOrder: %v", err)
	}
	if o.AmountSats != 1500 {
		t.Errorf("AmountSats = %d, want 1500", o.AmountSats)
	}
	if o.ExpiresAt != 1700000000 {
		t.Errorf("ExpiresAt = %d, want 1700000000", o.ExpiresAt)
	}
}
