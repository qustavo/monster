package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/qustavo/monster/internal/api"
	"github.com/qustavo/monster/internal/mostro"
)

// Palette mirrors gh-dash's default theme (ANSI-indexed, adapts to
// light/dark terminals) rather than picking arbitrary hex colors.
var (
	colorPrimaryText   = lipgloss.AdaptiveColor{Light: "0", Dark: "15"}
	colorSecondaryText = lipgloss.AdaptiveColor{Light: "244", Dark: "251"}
	colorFaintText     = lipgloss.AdaptiveColor{Light: "7", Dark: "245"}
	colorSelectedBg    = lipgloss.AdaptiveColor{Light: "7", Dark: "236"}
	colorSuccess       = lipgloss.AdaptiveColor{Light: "2", Dark: "10"}
	colorError         = lipgloss.AdaptiveColor{Light: "1", Dark: "9"}
	colorFaintBorder   = lipgloss.AdaptiveColor{Light: "254", Dark: "234"}
	colorPillBg        = lipgloss.Color("74") // sky blue (darker), fixed regardless of theme (GitHub labels use fixed colors too)
	colorPillText      = lipgloss.Color("0")  // black
	colorReputationBg  = lipgloss.Color("80") // teal — gold clashed with the ⭐ glyph's own yellow, made it disappear

	separatorStyle = lipgloss.NewStyle().Foreground(colorFaintBorder)

	activeTabStyle   = lipgloss.NewStyle().Bold(true).Background(colorSelectedBg).Foreground(colorPrimaryText).Padding(0, 2)
	inactiveTabStyle = lipgloss.NewStyle().Faint(true).Foreground(colorSecondaryText).Padding(0, 2)
	iWantToStyle     = lipgloss.NewStyle().Foreground(colorFaintText).Padding(0, 1)
	statusBarStyle   = lipgloss.NewStyle().Background(colorSelectedBg).Foreground(colorPrimaryText)
)

// tab filters the order list by side.
type tab int

const (
	tabAll tab = iota
	tabBuy
	tabSell
)

var tabTitles = [...]string{tabAll: "See All", tabBuy: "Buy", tabSell: "Sell"}

// Row layout: each order renders as a bold headline line plus a faint
// meta line beneath it (gh-dash's "extended title" pattern), so every
// entry costs 2 content lines + 1 blank spacer.
const (
	rowContentHeight = 2
	rowSpacing       = 1
	rowHeight        = rowContentHeight + rowSpacing

	// Rows moved per mouse wheel notch — one row per notch feels too
	// slow for a long order book.
	mouseScrollStep = 3

	// 2 (⚡/🔗 measure as double-width glyphs) + padding(0,1). A
	// single-width content area silently wraps these to the cell's
	// second line instead of clipping — verified against real lipgloss
	// rendering, not assumed.
	iconColWidth = 4
	nodeMaxWid   = 24
	nodeMinWid   = 8
	defaultWidth = 80

	// Cells whose content includes emoji (the reputation pill's
	// ⭐💬🗓️) reserve this many extra columns beyond Go's own width
	// count: real terminals don't always agree with Go's width tables
	// on exactly how wide a given emoji renders, and an undercount
	// clips the cell's trailing character on whichever row happens to
	// define that column's max width.
	emojiWidthMargin = 2

	// " " + one thumb/track cell, reserved unconditionally (even when
	// the list fits without scrolling) so the table's other column
	// widths don't jitter as the row count crosses the scrolling
	// threshold — e.g. while typing into the filter box.
	scrollbarWidth = 2

	sidebarWidth  = 36
	// Terminal width below which the sidebar is dropped. Six real
	// columns (icon+type pill+premium+reputation+node+created) need
	// ~61 cols even with node at its floor — a well-reviewed order's
	// reputation pill (3-digit reviews/days is unremarkable, not an
	// edge case) can't be truncated the way node names can — plus a
	// usable title floor of ~15 and the sidebar's own width+gap. Below
	// this, showing the sidebar starves the table instead of the
	// reverse. Verified empirically: computeWidths still overflows its
	// budget below this width even with node fully shrunk.
	sidebarMinTot = 116
)

type (
	ordersLoadedMsg []*mostro.Order
	orderEventMsg   api.Event
	errMsg          struct{ err error }
)

type model struct {
	apiBase string
	orders  map[string]*mostro.Order
	rows    []*mostro.Order // sorted view of all pending orders, rebuilt on change

	activeTab tab
	cursor    int
	offset    int
	width     int
	height    int

	filterInput textinput.Model
	filtering   bool // true while the filter box has focus

	connected bool
	status    string
}

func newModel(apiBase, initialFilter string) model {
	fi := textinput.New()
	fi.Placeholder = "cur:usd,eur pm:zelle node…"
	fi.Prompt = "/ "
	fi.CharLimit = 64
	if initialFilter != "" {
		fi.SetValue(initialFilter)
	}

	return model{
		apiBase:     apiBase,
		orders:      make(map[string]*mostro.Order),
		filterInput: fi,
		status:      "connecting…",
	}
}

func (m model) Init() tea.Cmd { return nil }

// upsert adds/updates o if it's pending, or drops it if a previously
// pending order has moved to another status.
func (m *model) upsert(o *mostro.Order) {
	if o.Status == mostro.StatusPending {
		m.orders[o.ID] = o
	} else {
		delete(m.orders, o.ID)
	}
	m.rebuild()
}

func (m *model) rebuild() {
	rows := make([]*mostro.Order, 0, len(m.orders))
	for _, o := range m.orders {
		rows = append(rows, o)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt > rows[j].CreatedAt })
	m.rows = rows
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
}

// visibleRowCount returns how many order rows fit in height, given the
// 1-line tab bar, 1-line filter bar, 1-line status bar, and rowHeight
// lines (2 content + 1 separator) per order.
func visibleRowCount(height int) int {
	if height <= 0 {
		return 6
	}
	n := (height - 3) / rowHeight
	return max(1, n)
}

// filteredRows returns m.rows narrowed to the active tab and, if set,
// the parsed filter query.
func (m model) filteredRows() []*mostro.Order {
	// Order-book convention: the Buy tab is where you go to buy, so it
	// lists sell-type orders (the liquidity you'd buy from) and vice
	// versa. Each row still honestly labels its own order.Type.
	var want mostro.OrderType
	switch m.activeTab {
	case tabBuy:
		want = mostro.OrderTypeSell
	case tabSell:
		want = mostro.OrderTypeBuy
	}

	query := parseFilterQuery(m.filterInput.Value())

	out := make([]*mostro.Order, 0, len(m.rows))
	for _, o := range m.rows {
		if want != "" && o.Type != want {
			continue
		}
		if !query.matches(o) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// filterQuery is a small structured query parsed from the filter box.
// Facets are independent and AND together; each facet supports one or
// more comma-separated values that OR together within that facet.
// Syntax: "cur:usd,eur pm:zelle,cash hodlhodl" — cur:/currency: and
// pm:/payment: are explicit facets; any other bare word matches the
// node/instance identity.
type filterQuery struct {
	currencies []string
	payments   []string
	nodeTerms  []string
}

func parseFilterQuery(raw string) filterQuery {
	var q filterQuery
	for _, tok := range strings.Fields(raw) {
		low := strings.ToLower(tok)
		switch {
		case strings.HasPrefix(low, "cur:"):
			q.currencies = append(q.currencies, splitFilterValues(low[len("cur:"):])...)
		case strings.HasPrefix(low, "currency:"):
			q.currencies = append(q.currencies, splitFilterValues(low[len("currency:"):])...)
		case strings.HasPrefix(low, "pm:"):
			q.payments = append(q.payments, splitFilterValues(low[len("pm:"):])...)
		case strings.HasPrefix(low, "payment:"):
			q.payments = append(q.payments, splitFilterValues(low[len("payment:"):])...)
		case strings.HasPrefix(low, "node:"):
			q.nodeTerms = append(q.nodeTerms, low[len("node:"):])
		case strings.HasPrefix(low, "instance:"):
			q.nodeTerms = append(q.nodeTerms, low[len("instance:"):])
		default:
			q.nodeTerms = append(q.nodeTerms, low)
		}
	}
	return q
}

func splitFilterValues(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (q filterQuery) empty() bool {
	return len(q.currencies) == 0 && len(q.payments) == 0 && len(q.nodeTerms) == 0
}

// matches reports whether o satisfies every facet present in q. Each
// facet independently requires at least one of its values to match;
// a facet that wasn't specified imposes no constraint.
func (q filterQuery) matches(o *mostro.Order) bool {
	if len(q.currencies) > 0 {
		cur := strings.ToLower(o.FiatCode)
		if !anyContains(q.currencies, cur) {
			return false
		}
	}
	if len(q.payments) > 0 {
		matched := false
		for _, pm := range o.PaymentMethods {
			if anyContains(q.payments, strings.ToLower(pm)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(q.nodeTerms) > 0 {
		label := strings.ToLower(nodeLabel(o))
		for _, term := range q.nodeTerms {
			if !strings.Contains(label, term) {
				return false
			}
		}
	}
	return true
}

func anyContains(values []string, s string) bool {
	for _, v := range values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

// counts returns (total, buy, sell) across pending orders matching the
// active text filter, independent of the active tab. buy/sell are how
// many orders would appear under each tab — i.e. inverted from
// order.Type, matching filteredRows's order-book convention (Buy tab =
// sell-type orders).
func (m model) counts() (total, buy, sell int) {
	query := parseFilterQuery(m.filterInput.Value())
	for _, o := range m.rows {
		if !query.matches(o) {
			continue
		}
		total++
		if o.Type == mostro.OrderTypeSell {
			buy++
		} else if o.Type == mostro.OrderTypeBuy {
			sell++
		}
	}
	return total, buy, sell
}

// grandCounts returns (total, buy, sell) across every pending order,
// ignoring both the active tab and the text filter — the status bar's
// grand totals, as opposed to counts()'s filtered tab-bar numbers.
func (m model) grandCounts() (total, buy, sell int) {
	total = len(m.rows)
	for _, o := range m.rows {
		if o.Type == mostro.OrderTypeSell {
			buy++
		} else if o.Type == mostro.OrderTypeBuy {
			sell++
		}
	}
	return total, buy, sell
}

// moveCursor shifts the cursor by delta rows, clamped to the current
// filtered view (0 when there's nothing to select).
func (m *model) moveCursor(delta int) {
	n := len(m.filteredRows())
	if n == 0 {
		m.cursor = 0
		return
	}
	m.cursor = min(max(m.cursor+delta, 0), n-1)
}

func (m *model) clampScroll() {
	visible := visibleRowCount(m.height)
	rows := len(m.filteredRows())
	if m.cursor >= rows {
		m.cursor = max(0, rows-1)
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyMsg:
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filterInput.SetValue("")
				m.filterInput.Blur()
				m.cursor, m.offset = 0, 0
			case "enter":
				m.filtering = false
				m.filterInput.Blur()
			default:
				var cmd tea.Cmd
				m.filterInput, cmd = m.filterInput.Update(msg)
				m.cursor, m.offset = 0, 0
				m.clampScroll()
				return m, cmd
			}
			break
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "/":
			m.filtering = true
			return m, m.filterInput.Focus()
		case "up", "k":
			m.moveCursor(-1)
		case "down", "j":
			m.moveCursor(1)
		case "right", "l", "tab":
			m.activeTab = (m.activeTab + 1) % 3
			m.cursor, m.offset = 0, 0
		case "left", "h", "shift+tab":
			m.activeTab = (m.activeTab + 2) % 3
			m.cursor, m.offset = 0, 0
		case "1":
			m.activeTab, m.cursor, m.offset = tabAll, 0, 0
		case "2":
			m.activeTab, m.cursor, m.offset = tabBuy, 0, 0
		case "3":
			m.activeTab, m.cursor, m.offset = tabSell, 0, 0
		case "enter":
			rows := m.filteredRows()
			if m.cursor >= 0 && m.cursor < len(rows) {
				if src := rows[m.cursor].Source; strings.HasPrefix(src, "https://") {
					return m, openBrowserCmd(src)
				}
			}
		}

	case tea.MouseMsg:
		switch msg.Type {
		case tea.MouseWheelUp:
			m.moveCursor(-mouseScrollStep)
		case tea.MouseWheelDown:
			m.moveCursor(mouseScrollStep)
		}

	case ordersLoadedMsg:
		m.orders = make(map[string]*mostro.Order, len(msg))
		for _, o := range msg {
			m.orders[o.ID] = o
		}
		m.rebuild()
		m.connected = true
		m.status = "connected"

	case orderEventMsg:
		m.upsert(msg.Order)
		m.status = fmt.Sprintf("%s: %s", msg.Type, msg.Order.ID)

	case errMsg:
		m.connected = false
		m.status = "error: " + msg.err.Error()
	}

	m.clampScroll()
	return m, nil
}

// colWidths are the computed widths (including their own padding) of
// the icon, premium, reputation, node and created_at columns; the title
// block gets whatever's left of the terminal width.
type colWidths struct {
	typeCol, icon, title, premium, reputation, node, created int
}

// typeColWidth is fixed: it only needs to fit the two pill variants
// ("buying"/"selling"), not per-row content.
func typeColWidth() int {
	return max(lipgloss.Width(typePill(mostro.OrderTypeBuy)), lipgloss.Width(typePill(mostro.OrderTypeSell)))
}

func computeWidths(width int, rows []*mostro.Order) colWidths {
	if width <= 0 {
		width = defaultWidth
	}

	typeCol := typeColWidth()
	premium := lipgloss.Width("PREMIUM")
	reputation := lipgloss.Width(renderPill("2.5⭐ 10💬 999🗓️", colorReputationBg))
	node := lipgloss.Width("NODE")
	created := lipgloss.Width("CREATED_AT")
	for _, o := range rows {
		if w := lipgloss.Width(formatPremium(o.Premium)); w > premium {
			premium = w
		}
		if w := lipgloss.Width(renderPill(formatReputation(o), colorReputationBg)); w > reputation {
			reputation = w
		}
		if w := lipgloss.Width(nodeLabel(o)); w > node {
			node = w
		}
		if w := lipgloss.Width(humanizeAgo(o.CreatedAt)); w > created {
			created = w
		}
	}
	premium += 2
	reputation += emojiWidthMargin
	node += 2
	if node > nodeMaxWid {
		node = nodeMaxWid
	}
	created += 2

	fixed := typeCol + iconColWidth + premium + reputation + node + created

	// If the non-title columns don't leave room for even a 1-wide
	// title, shrink node further (down to its floor) before letting
	// title collapse — node is already truncated with an ellipsis when
	// rendered, so it's the most graceful column to compress under
	// real width pressure. The -1 reserves that minimum title column;
	// without it, shrinking node to exactly fit `fixed` still leaves
	// title's own max(...,1) floor pushing the total 1 over width.
	if over := fixed - (width - 1); over > 0 && node > nodeMinWid {
		shrink := min(over, node-nodeMinWid)
		node -= shrink
		fixed -= shrink
	}

	// The row must never render wider than the space allocated for it;
	// title gets whatever's left. On terminals too narrow to fit the
	// other six columns even at their floors (reputation in particular
	// can't be shrunk below the pill's own content), title collapses to
	// 0 rather than forcing an overflow — renderTitleCell degrades
	// gracefully at width 0.
	title := max(width-fixed, 0)

	return colWidths{typeCol: typeCol, icon: iconColWidth, title: title, premium: premium, reputation: reputation, node: node, created: created}
}

// formatReputation renders the maker's rating as "2.5⭐ 10💬 4🗓️"
// (rating, review count, days) or "–" when no rating was published.
func formatReputation(o *mostro.Order) string {
	if o.Rating == nil {
		return "–"
	}
	return fmt.Sprintf("%.1f⭐ %d💬 %d🗓️", o.Rating.TotalRating, o.Rating.TotalReviews, o.Rating.Days)
}

// nodeLabel is the best available identity for the Mostro node that
// published an order: its kind-0 profile name when known (resolved by
// monsterd and served on Order.NodeName), falling back to the order's
// own y-tag instance label, and finally to an abbreviated pubkey.
func nodeLabel(o *mostro.Order) string {
	if o.NodeName != "" {
		return sanitizeTableText(o.NodeName)
	}
	if l := instanceLabel(o); l != "" {
		return sanitizeTableText(l)
	}
	return shortKey(o.NodePubkey)
}

// formatPaymentMethods joins an order's payment methods for the table's
// title-cell meta line, sanitized (see sanitizeTableText).
func formatPaymentMethods(o *mostro.Order) string {
	return sanitizeTableText(strings.Join(o.PaymentMethods, ", "))
}

// sanitizeTableText strips symbols/emoji/pictographs from node names and
// payment methods before they enter the table's column-width math — both
// are free text set by the order's maker on the Mostro network and can
// contain characters (colored-circle bullets, dingbats, flag sequences)
// whose terminal-rendered width doesn't match what Go's width tables
// report. That mismatch throws off computeWidths' column budget and the
// row's real on-screen alignment, which showed up as rows visually
// bleeding into their neighbors. ASCII, Latin/Cyrillic/Greek letters
// (accented payment-method names are common), and CJK/Kana/Hangul (which
// terminals and Go agree are unambiguously double-width) are kept; the
// rest is dropped rather than guessed at.
func sanitizeTableText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x2000:
			b.WriteRune(r)
		case r >= 0x0370 && r <= 0x1FFF: // Greek, Cyrillic, and other alphabetic scripts
			b.WriteRune(r)
		case r >= 0x3040 && r <= 0x30FF, // Hiragana, Katakana
			r >= 0x4E00 && r <= 0x9FFF, // CJK Unified Ideographs
			r >= 0xAC00 && r <= 0xD7A3: // Hangul syllables
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

// instanceLabel is the best available name for the platform that
// published an order: the operator-chosen instance name when present,
// otherwise the bare software identifier (e.g. "Bitblik", "lnp2pbot").
func instanceLabel(o *mostro.Order) string {
	if o.InstanceName != "" {
		return o.InstanceName
	}
	return o.Platform
}

func (m model) View() string {
	width := m.width
	if width <= 0 {
		width = defaultWidth
	}

	showSidebar := width >= sidebarMinTot
	listWidth := width - scrollbarWidth
	if showSidebar {
		listWidth -= sidebarWidth + 1
	}

	rows := m.filteredRows()
	w := computeWidths(listWidth, rows)

	lines := make([]string, 0, 2+visibleRowCount(m.height)*rowHeight)
	lines = append(lines, m.renderTabs())

	visible := visibleRowCount(m.height)
	end := min(m.offset+visible, len(rows))
	separator := separatorStyle.Render(strings.Repeat("─", w.typeCol+w.icon+w.title+w.premium+w.reputation+w.node+w.created))

	var selected *mostro.Order
	for i := m.offset; i < end; i++ {
		o := rows[i]
		isSelected := i == m.cursor
		if isSelected {
			selected = o
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top,
			renderLayerCell(o, isSelected, w.icon),
			renderTypeCell(o, isSelected, w.typeCol),
			renderTitleCell(o, isSelected, w.title),
			renderPremiumCell(o, isSelected, w.premium),
			renderReputationCell(o, isSelected, w.reputation),
			renderNodeCell(o, isSelected, w.node),
			renderCreatedCell(o, isSelected, w.created),
		)
		lines = append(lines, row, separator)
	}
	list := strings.Join(lines, "\n")
	scrollbar := renderScrollbar(visible, len(rows), m.offset)

	// target is the full body height available (reserving the filter
	// bar + status bar lines) — used both to size the sidebar and, below,
	// to pad the final output. The sidebar must use this instead of the
	// list's own (possibly short) rendered height, or a short/filtered
	// list would shrink the sidebar down with it instead of filling the
	// terminal.
	target := m.height - 2
	sidebarHeight := max(lipgloss.Height(list), lipgloss.Height(scrollbar), target)

	body := lipgloss.JoinHorizontal(lipgloss.Top, list, scrollbar)
	if showSidebar {
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, renderSidebar(selected, sidebarWidth, sidebarHeight))
	}

	// Pad up to the full terminal height so the filter/status bars stay
	// pinned to the bottom instead of trailing right after a short list
	// (few matching orders, or a tall terminal).
	if m.height > 0 {
		if pad := target - lipgloss.Height(body); pad > 0 {
			body += strings.Repeat("\n", pad)
		}
	}

	return body + "\n" + m.renderFilterBar() + "\n" + m.renderStatusBar()
}

// scrollbarThumb computes the thumb's (start, length) in track-cell units
// for a track visible cells tall, proportional to how offset sits within
// [0, total-visible]. Uses rounded float math rather than integer
// division: truncating division rounds toward 0 across the whole scroll
// range (division only happens to land exactly at the very ends), so
// the thumb would consistently trail behind its true proportional
// position instead of tracking it symmetrically.
func scrollbarThumb(visible, total, offset int) (start, length int) {
	if total <= visible {
		return 0, visible
	}
	length = max(1, int(math.Round(float64(visible*visible)/float64(total))))
	maxOffset := total - visible
	start = int(math.Round(float64(offset*(visible-length)) / float64(maxOffset)))
	return start, length
}

// renderScrollbar builds a one-column vertical scroll indicator matching
// the row list's height: a faint track with a highlighted thumb showing
// where the current viewport (visible rows starting at offset) sits
// within the full total. Thumb size and position are proportional to
// the track, not a literal 1:1 map of which rows are on screen. A
// leading blank line aligns it with the tab bar above the list.
func renderScrollbar(visible, total, offset int) string {
	trackChar := separatorStyle.Render(" │")
	thumbChar := lipgloss.NewStyle().Foreground(colorPillBg).Render(" ┃")

	thumbStart, thumbLen := scrollbarThumb(visible, total, offset)

	lines := make([]string, 0, 1+visible*rowHeight)
	lines = append(lines, "  ") // blank line aligning with the tab bar
	for i := range visible {
		ch := trackChar
		if i >= thumbStart && i < thumbStart+thumbLen {
			ch = thumbChar
		}
		for range rowHeight {
			lines = append(lines, ch)
		}
	}

	return strings.Join(lines, "\n")
}

// renderTabs renders the "All (N)  Buy (N)  Sell (N)" tab bar, with the
// active tab highlighted.
func (m model) renderTabs() string {
	total, buy, sell := m.counts()
	counts := [3]int{tabAll: total, tabBuy: buy, tabSell: sell}

	cells := make([]string, 0, 4)
	cells = append(cells, iWantToStyle.Render("I want to"))
	for t := tabAll; t <= tabSell; t++ {
		label := fmt.Sprintf("%s (%d)", tabTitles[t], counts[t])
		style := inactiveTabStyle
		if t == m.activeTab {
			style = activeTabStyle
		}
		cells = append(cells, style.Render(label))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

// renderFilterBar renders the filter input when active, or a faint
// summary/hint line when not — either way exactly one line.
func (m model) renderFilterBar() string {
	if m.filtering {
		return m.filterInput.View()
	}

	hint := lipgloss.NewStyle().Foreground(colorFaintText)
	if q := strings.TrimSpace(m.filterInput.Value()); q != "" {
		return hint.Render(fmt.Sprintf("Filter: %q  (/ to edit, esc to clear)", q))
	}
	return hint.Render("/ to filter — cur:usd,eur  pm:zelle,cash  or a bare node search")
}

// renderStatusBar renders a full-width status bar: connection state,
// order counts, and the last event, on a solid background band.
//
// Every piece is rendered independently and then concatenated (never
// nested inside another Render call): a style's reset code clears the
// whole SGR state, so embedding one rendered segment inside a string
// that's later wrapped by another style would clobber the outer
// background partway through.
func (m model) renderStatusBar() string {
	dotColor := colorSuccess
	if !m.connected {
		dotColor = colorError
	}
	dotStyle := lipgloss.NewStyle().Background(colorSelectedBg).Foreground(dotColor)

	total, buy, sell := m.grandCounts()
	left := statusBarStyle.Render(" ") + dotStyle.Render("●") +
		statusBarStyle.Render(fmt.Sprintf("  Total %d · Buy %d · Sell %d", total, buy, sell))
	right := statusBarStyle.Render(fmt.Sprintf("%s   ↑/↓ move · ←/→ tabs · / filter · enter open · q quit  ", m.status))

	width := m.width
	if width <= 0 {
		width = defaultWidth
	}
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))

	return left + statusBarStyle.Render(strings.Repeat(" ", gap)) + right
}

// renderSidebar renders the detail panel for the currently selected
// order (or a placeholder if nothing is selected), bordered on the
// left to divide it from the order list.
func renderSidebar(o *mostro.Order, width, height int) string {
	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).
		BorderForeground(colorFaintBorder).
		Padding(0, 1).
		Width(width).
		Height(height).
		MaxHeight(height)

	if o == nil {
		return style.Foreground(colorFaintText).Render("No order selected")
	}

	contentWidth := max(width-2, 1)
	sectionLabel := lipgloss.NewStyle().Foreground(colorFaintText).Bold(true)
	sectionValue := lipgloss.NewStyle().Foreground(colorPrimaryText).Width(contentWidth)

	summary := lipgloss.NewStyle().Foreground(colorPrimaryText).Width(contentWidth).Render(renderSummary(o))
	rule := separatorStyle.Render(strings.Repeat("─", contentWidth))

	section := func(title, body string) string {
		return sectionLabel.Render(title) + "\n" + sectionValue.Render(body)
	}

	paymentMethods := section("PAYMENT METHODS", formatPaymentMethods(o))
	orderID := section("ORDER ID", o.ID)
	reputation := section("REPUTATION", longReputationText(o))

	lines := []string{summary, rule, paymentMethods, "", orderID, "", reputation}
	if strings.HasPrefix(o.Source, "https://") {
		lines = append(lines, "", section("SOURCE", o.Source+" (enter to open in your browser)"))
	}
	return style.Render(strings.Join(lines, "\n"))
}

// longReputationText is the sidebar's expanded reputation write-up,
// spelling out every field in the rating tag (average, spread, most
// recent rating, and days) rather than the table column's compact form.
func longReputationText(o *mostro.Order) string {
	if o.Rating == nil {
		return "No reputation data was published with this order."
	}
	r := o.Rating
	if r.TotalReviews == 0 {
		return "No reviews yet."
	}

	s := fmt.Sprintf("%.1f★ average from %d %s", r.TotalRating, r.TotalReviews, pluralize(r.TotalReviews, "review"))
	if r.Days > 0 {
		s += fmt.Sprintf(" over %d %s", r.Days, pluralize(r.Days, "day"))
	}
	s += "."

	if r.MinRate > 0 && r.MaxRate > 0 {
		s += fmt.Sprintf(" Individual ratings have ranged from %d★ to %d★", r.MinRate, r.MaxRate)
		if r.LastRating > 0 {
			s += fmt.Sprintf(", most recently %d★", r.LastRating)
		}
		s += "."
	} else if r.LastRating > 0 {
		s += fmt.Sprintf(" Most recent rating: %d★.", r.LastRating)
	}
	return s
}

// pluralize appends "s" to word unless n == 1.
func pluralize(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// renderSummary is the sidebar's plain-language lede: what's being
// traded, at what price, how to pay, and who's on the other side.
func renderSummary(o *mostro.Order) string {
	verb := "selling"
	if o.Type == mostro.OrderTypeBuy {
		verb = "buying"
	}

	s := fmt.Sprintf("Someone is %s sats for %s %s %s",
		verb, formatAmount(o), currencyLabel(o.FiatCode), marketPriceClause(o.Premium))

	if len(o.PaymentMethods) > 0 {
		s += ", via " + formatPaymentMethods(o)
	}
	if n := nodeLabel(o); n != "" {
		s += ", on " + n
	}
	s += "."

	return s
}

// marketPriceClause describes a premium value in plain language, e.g.
// "at 5% above market price", "at market price" for zero/unparseable.
func marketPriceClause(premium string) string {
	v, err := strconv.ParseFloat(premium, 64)
	if err != nil || v == 0 {
		return "at market price"
	}
	pct := strconv.FormatFloat(math.Abs(v), 'f', -1, 64)
	if v > 0 {
		return fmt.Sprintf("at %s%% above market price", pct)
	}
	return fmt.Sprintf("at %s%% below market price", pct)
}

// shortKey abbreviates a hex pubkey to its first/last 8 characters.
func shortKey(pubkey string) string {
	if len(pubkey) <= 16 {
		return pubkey
	}
	return pubkey[:8] + "…" + pubkey[len(pubkey)-8:]
}

// renderLayerCell renders the settlement layer icon (⚡ lightning, 🔗
// onchain), vertically centered across the row's height.
func renderLayerCell(o *mostro.Order, selected bool, width int) string {
	var icon string
	switch o.Layer {
	case "lightning":
		icon = "⚡"
	case "onchain":
		icon = "🔗"
	}
	style := lipgloss.NewStyle().Padding(0, 1).Height(rowContentHeight).
		Width(width).Align(lipgloss.Center)
	if selected {
		style = style.Background(colorSelectedBg)
	}
	return style.Render(icon)
}

// renderTitleCell renders the two-line block: a bold headline (amount,
// currency+flag) over a faint meta line (payment methods). The order
// side (buy/sell) is its own leading column — see renderTypeCell.
func renderTitleCell(o *mostro.Order, selected bool, width int) string {
	contentWidth := max(width-2, 1)

	headline := ansi.Truncate(fmt.Sprintf("%s %s", formatAmount(o), currencyLabel(o.FiatCode)), contentWidth, "…")
	meta := ansi.Truncate(formatPaymentMethods(o), contentWidth, "…")

	headlineStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimaryText).
		Padding(0, 1).Width(width).Height(1)
	metaStyle := lipgloss.NewStyle().Foreground(colorFaintText).
		Padding(0, 1).Width(width).Height(1)
	if selected {
		headlineStyle = headlineStyle.Background(colorSelectedBg)
		metaStyle = metaStyle.Background(colorSelectedBg)
	}

	return lipgloss.JoinVertical(lipgloss.Left, headlineStyle.Render(headline), metaStyle.Render(meta))
}

// renderTypeCell renders the buy/sell pill as its own leading column.
// The pill keeps its own fixed background always (like the reputation
// badge) — never nest a pre-rendered background segment inside another
// Style.Render call, since the inner reset would clobber the outer
// background partway through. Every piece here is rendered
// independently and concatenated.
func renderTypeCell(o *mostro.Order, selected bool, width int) string {
	return renderCenteredPillCell(typePill(o.Type), selected, width)
}

// renderCenteredPillCell centers a pre-rendered pill (see renderPill)
// horizontally within a two-line cell — line 1 carries the pill, line 2
// is blank filler so the cell matches the row's other 2-line-tall cells.
func renderCenteredPillCell(pill string, selected bool, width int) string {
	bg := lipgloss.NewStyle()
	if selected {
		bg = bg.Background(colorSelectedBg)
	}

	leftPad := max((width-lipgloss.Width(pill))/2, 0)
	rightPad := max(width-leftPad-lipgloss.Width(pill), 0)
	line1 := bg.Render(strings.Repeat(" ", leftPad)) + pill + bg.Render(strings.Repeat(" ", rightPad))
	line2 := bg.Render(strings.Repeat(" ", width))

	return line1 + "\n" + line2
}

// typePill renders a GitHub-label-style chip for the order side.
//
// Attempt 1 used plain Unicode half-circles (◖◗) for rounded caps — that
// failed because those glyphs carry internal padding baked into their
// design and don't tile edge-to-edge with an adjacent solid-color cell,
// so it never reads as one continuous pill even though the glyph itself
// is round.
//
// This is attempt 2: Nerd Font's Powerline round-separator glyphs
// (U+E0B6 / U+E0B4) exist specifically to solve that — they're designed
// for zero-gap edge connection against a solid background, which is
// exactly why gh-dash uses these same two codepoints for this same
// effect. Requires a Nerd-Font-patched terminal font; without one these
// render as tofu/missing-glyph boxes instead of rounded caps.
func typePill(t mostro.OrderType) string {
	text := "selling"
	if t == mostro.OrderTypeBuy {
		text = "buying"
	}
	return renderPill(text, colorPillBg)
}

// renderPill is the shared GitHub-label-chip mechanism: flat solid
// background with Nerd Font Powerline round-separator caps. See
// typePill's doc comment for why this exact, minimal combination of
// style calls is what actually renders rounded — Padding or
// BorderBackground here previously broke it.
func renderPill(text string, bg lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(colorPillText).
		Background(bg).
		Border(lipgloss.Border{Left: "", Right: ""}, false, true, false, true).
		BorderForeground(bg).
		Render(text)
}

// renderPremiumCell renders the premium, colored green/red by sign.
func renderPremiumCell(o *mostro.Order, selected bool, width int) string {
	color := colorSecondaryText
	if v, err := strconv.ParseFloat(o.Premium, 64); err == nil {
		switch {
		case v > 0:
			color = colorSuccess
		case v < 0:
			color = colorError
		}
	}
	style := lipgloss.NewStyle().Padding(0, 1).Height(rowContentHeight).
		Width(width).Align(lipgloss.Right).Foreground(color)
	if selected {
		style = style.Background(colorSelectedBg)
	}
	return style.Render(formatPremium(o.Premium))
}

// renderReputationCell renders the maker's rating as a pill (see
// formatReputation), matching the type pill's formatting. Same
// concatenation rule as renderTypeCell: the pill keeps its own fixed
// background always, so it's built independently and never nested
// inside another Style.Render call.
func renderReputationCell(o *mostro.Order, selected bool, width int) string {
	return renderCenteredPillCell(renderPill(formatReputation(o), colorReputationBg), selected, width)
}

// renderNodeCell renders the publishing node's identity (see nodeLabel).
func renderNodeCell(o *mostro.Order, selected bool, width int) string {
	contentWidth := max(width-2, 1)
	style := lipgloss.NewStyle().Padding(0, 1).Height(rowContentHeight).
		Width(width).Foreground(colorFaintText)
	if selected {
		style = style.Background(colorSelectedBg)
	}
	return style.Render(ansi.Truncate(nodeLabel(o), contentWidth, "…"))
}

// renderCreatedCell renders the relative creation time.
func renderCreatedCell(o *mostro.Order, selected bool, width int) string {
	style := lipgloss.NewStyle().Padding(0, 1).Height(rowContentHeight).
		Width(width).Foreground(colorSecondaryText)
	if selected {
		style = style.Background(colorSelectedBg)
	}
	return style.Render(humanizeAgo(o.CreatedAt))
}

// formatAmount renders o's fiat amount as a "min-max" range when the
// order has one, otherwise the fixed amount.
func formatAmount(o *mostro.Order) string {
	if o.MinAmount != "" && o.MaxAmount != "" {
		return o.MinAmount + "-" + o.MaxAmount
	}
	return o.FiatAmount
}

// formatPremium renders a premium value with an explicit sign, e.g.
// "+5%" or "-3%". Falls back to the raw string if it isn't numeric.
func formatPremium(s string) string {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	sign := ""
	if v > 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%s%%", sign, strconv.FormatFloat(v, 'f', -1, 64))
}

// humanizeAgo renders a unix timestamp as a relative duration, e.g.
// "3m ago", falling back to a date for anything older than a month.
func humanizeAgo(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return time.Unix(unix, 0).Format("2006-01-02")
	}
}

// currencyToCountry maps common ISO 4217 fiat codes seen in Mostro
// orders to the ISO 3166-1 alpha-2 (or the special "EU") region used to
// render a flag emoji. Codes without a known mapping just show as text.
var currencyToCountry = map[string]string{
	"USD": "US", "EUR": "EU", "GBP": "GB", "CHF": "CH", "CAD": "CA", "AUD": "AU",
	"VES": "VE", "COP": "CO", "ARS": "AR", "BRL": "BR", "PEN": "PE", "BOB": "BO",
	"MXN": "MX", "CLP": "CL", "UYU": "UY", "PYG": "PY", "CUP": "CU", "DOP": "DO",
	"GTQ": "GT", "HNL": "HN", "NIO": "NI", "CRC": "CR", "PAB": "PA",
	"PLN": "PL", "CZK": "CZ", "SEK": "SE", "NOK": "NO", "DKK": "DK", "TRY": "TR", "RUB": "RU",
	"JPY": "JP", "CNY": "CN", "KRW": "KR", "INR": "IN", "IDR": "ID", "VND": "VN",
	"PHP": "PH", "THB": "TH", "MYR": "MY", "SGD": "SG",
	"NGN": "NG", "KES": "KE", "ZAR": "ZA", "EGP": "EG", "MAD": "MA",
	"AED": "AE", "SAR": "SA", "ILS": "IL", "PKR": "PK", "BDT": "BD",
}

// currencyLabel renders a fiat code with its country flag when known,
// e.g. "🇺🇸 USD", falling back to the bare code otherwise.
func currencyLabel(fiatCode string) string {
	flag := countryFlag(currencyToCountry[fiatCode])
	if flag == "" {
		return fiatCode
	}
	return fiatCode + " " + flag
}

// countryFlag builds a regional-indicator flag emoji from a 2-letter
// region code (e.g. "US" -> 🇺🇸), or "" if code isn't exactly 2 letters.
func countryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	r1 := rune(code[0]-'A') + 0x1F1E6
	r2 := rune(code[1]-'A') + 0x1F1E6
	return string(r1) + string(r2)
}
