// Command monstertui is a terminal client for monsterd: it fetches the
// current order book over HTTP and subscribes to live updates over SSE.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/qustavo/monster/internal/daemon"
)

func main() {
	endpointFlag := flag.String("endpoint", "",
		"monsterd API base URL (e.g. http://localhost:8080); if empty, monstertui starts its own embedded server on a random port")
	relaysFlag := flag.String("relays", daemon.DefaultRelays,
		"comma-separated relay URLs, used only when -endpoint is not set")
	filterFlag := flag.String("filter", "", `initial filter query, e.g. "cur:usd,eur pm:zelle"`)
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	apiBase := *endpointFlag
	if apiBase == "" {
		var err error
		apiBase, err = autostart(ctx, strings.Split(*relaysFlag, ","))
		if err != nil {
			fmt.Fprintln(os.Stderr, "monstertui:", err)
			os.Exit(1)
		}
	}

	p := tea.NewProgram(newModel(apiBase, *filterFlag), tea.WithAltScreen(), tea.WithMouseCellMotion())

	go run(ctx, p, apiBase)

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "monstertui:", err)
		os.Exit(1)
	}
}
