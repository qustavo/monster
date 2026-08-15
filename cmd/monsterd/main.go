// Command monsterd connects to Mostro relays, ingests order events
// (kind 38383, NIP-69), and serves them over HTTP/SSE.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/qustavo/monster/internal/api"
	"github.com/qustavo/monster/internal/daemon"
	"github.com/qustavo/monster/internal/store/sqlite"
)

func main() {
	relayFlag := flag.String("relays",
		"wss://mostro-p2p.tech,wss://nos.lol,wss://relay.mostro.network",
		"comma-separated relay URLs")
	dbFlag := flag.String("db", "orders.db", "path to sqlite database")
	addrFlag := flag.String("addr", ":8080", "HTTP API listen address")
	flag.Parse()

	relays := strings.Split(*relayFlag, ",")

	db, err := sqlite.Open(*dbFlag)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	hub := api.NewHub()
	mux := api.NewMux(db, hub)
	go func() {
		log.Printf("API listening on %s", *addrFlag)
		if err := http.ListenAndServe(*addrFlag, mux); err != nil {
			log.Fatal(err)
		}
	}()

	if err := daemon.Run(ctx, relays, db, hub); err != nil {
		log.Fatal(err)
	}
}
