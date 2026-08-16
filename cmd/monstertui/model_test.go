package main

import (
	"strings"
	"testing"

	"github.com/qustavo/monster/internal/mostro"
)

// TestSanitizeTableTextStripsAmbiguousWidthGlyphs guards against the bug
// that caused this order to persistently corrupt the table/sidebar
// layout: a keycap emoji ("2️⃣" = digit + U+FE0F variation selector +
// U+20E3 combining enclosing keycap) whose real terminal-rendered width
// doesn't match Go's width tables. Ordinary standalone pictographs
// (🔹/🔸) sitting right next to it in the same string rendered fine —
// it's specifically the combining-character cluster that broke.
func TestSanitizeTableTextStripsAmbiguousWidthGlyphs(t *testing.T) {
	const raw = "USDT\U0001F539USDC ➕2️⃣ ALL NETWORKS BINANCE\U0001F538PAY"
	got := sanitizeTableText(raw)
	const want = "USDT USDC 2 ALL NETWORKS BINANCE PAY"
	if got != want {
		t.Errorf("sanitizeTableText(%q) = %q, want %q", raw, got, want)
	}

	// Accented Latin (common in Spanish/Portuguese payment-method names)
	// and CJK/Kana/Hangul must survive — only symbol/emoji/dingbat
	// ranges get stripped.
	for _, keep := range []string{"Lemón", "Yape", "东京银行", "한국은행"} {
		if got := sanitizeTableText(keep); got != keep {
			t.Errorf("sanitizeTableText(%q) = %q, want unchanged", keep, got)
		}
	}
}

// TestOrderFreeTextIsSanitizedEverywhere ensures every place an order's
// free-text node name or payment methods reach the table or sidebar goes
// through sanitizeTableText, not the raw maker-supplied string — both
// fields can contain arbitrary Unicode from the Mostro network.
func TestOrderFreeTextIsSanitizedEverywhere(t *testing.T) {
	o := &mostro.Order{
		Type:           mostro.OrderTypeSell,
		FiatCode:       "USD",
		FiatAmount:     "130",
		NodeName:       "Mostro\U0001F539",
		PaymentMethods: []string{"USDT\U0001F539USDC ➕2️⃣ ALL NETWORKS BINANCE\U0001F538PAY"},
	}

	if strings.ContainsRune(nodeLabel(o), '\U0001F539') {
		t.Errorf("nodeLabel leaked an unsanitized glyph: %q", nodeLabel(o))
	}
	if strings.ContainsRune(formatPaymentMethods(o), '\U0001F539') || strings.ContainsRune(formatPaymentMethods(o), '\U000020E3') {
		t.Errorf("formatPaymentMethods leaked an unsanitized glyph: %q", formatPaymentMethods(o))
	}
	if strings.ContainsRune(renderSummary(o), '\U0001F539') {
		t.Errorf("renderSummary leaked an unsanitized glyph: %q", renderSummary(o))
	}
}

// TestComputeWidthsNeverOverflows sweeps realistic terminal widths and
// checks computeWidths never returns column widths whose sum exceeds the
// space it was given — a real order with a long node name and a
// well-established reputation (3-digit reviews/days is unremarkable) can
// otherwise force the row wider than its budget. Below 63 cols, six real
// columns don't fit even at their structural floors and title
// legitimately collapses to 0; that's expected degradation, not overflow.
// TestCountsRespectsActiveFilter guards against counts() reading m.rows
// directly instead of applying the active text filter: the tab bar's
// "See All (N) Buy (N) Sell (N)" counts must match what filteredRows
// would actually show for each tab, not the unfiltered totals.
func TestCountsRespectsActiveFilter(t *testing.T) {
	m := newModel("", "")
	m.rows = []*mostro.Order{
		{ID: "1", Type: mostro.OrderTypeBuy, NodeName: "alice"},
		{ID: "2", Type: mostro.OrderTypeSell, NodeName: "alice"},
		{ID: "3", Type: mostro.OrderTypeSell, NodeName: "bob"},
	}

	if total, buy, sell := m.counts(); total != 3 || buy != 2 || sell != 1 {
		t.Fatalf("counts() with no filter = (%d,%d,%d), want (3,2,1)", total, buy, sell)
	}

	m.filterInput.SetValue("alice")
	if total, buy, sell := m.counts(); total != 2 || buy != 1 || sell != 1 {
		t.Errorf("counts() with filter %q = (%d,%d,%d), want (2,1,1)", "alice", total, buy, sell)
	}

	// grandCounts (status bar) must stay unfiltered even while a filter
	// is active — only counts() (tab bar) narrows.
	if total, buy, sell := m.grandCounts(); total != 3 || buy != 2 || sell != 1 {
		t.Errorf("grandCounts() with filter %q = (%d,%d,%d), want (3,2,1)", "alice", total, buy, sell)
	}
}

func TestComputeWidthsNeverOverflows(t *testing.T) {
	rows := []*mostro.Order{
		{
			ID: "1", Type: mostro.OrderTypeBuy, FiatCode: "COP",
			MinAmount: "10000", MaxAmount: "1708000", Premium: "-2",
			PaymentMethods: []string{"Nequi", "Llaves BRE-B", "Daviplata"},
			NodeName:       "MostroColombia — a very long node display name indeed",
			Rating:         &mostro.Rating{TotalRating: 4.9, TotalReviews: 112, Days: 152},
			CreatedAt:      1700000000,
		},
		{
			ID: "2", Type: mostro.OrderTypeSell, FiatCode: "USD",
			FiatAmount:     "100",
			PaymentMethods: []string{"Zelle"},
			NodeName:       "hodlhodl",
			CreatedAt:      1700000000,
		},
	}

	for listWidth := 63; listWidth <= 120; listWidth++ {
		w := computeWidths(listWidth, rows)
		total := w.typeCol + w.icon + w.title + w.premium + w.reputation + w.node + w.created
		if total > listWidth {
			t.Errorf("listWidth=%d: total=%d exceeds listWidth (overflow of %d) — colWidths=%+v",
				listWidth, total, total-listWidth, w)
		}
	}
}
