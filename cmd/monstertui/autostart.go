package main

import (
	"context"
	"fmt"
	"log"
	"net"

	"github.com/qustavo/monster/internal/daemon"
	"github.com/qustavo/monster/internal/store/sqlite"
)

// autostartDB is the database file autostart opens — the same default
// path monsterd itself uses, so a plain `monstertui` run persists its
// order history and can share state with a separately run monsterd
// pointed at the same file.
const autostartDB = "orders.db"

// autostart runs an embedded monsterd (relay ingestion + HTTP/SSE API)
// in-process, on a random available port, for when the user hasn't
// pointed monstertui at an existing server via -endpoint. Everything it
// started is torn down when ctx is canceled, but the database itself
// (autostartDB) persists across runs like monsterd's own.
func autostart(ctx context.Context, relays []string) (apiBase string, err error) {
	db, err := sqlite.Open(autostartDB)
	if err != nil {
		return "", fmt.Errorf("autostart: open db: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		db.Close()
		return "", fmt.Errorf("autostart: listen: %w", err)
	}

	go func() {
		if err := daemon.Bootstrap(ctx, relays, db, listener); err != nil && ctx.Err() == nil {
			log.Printf("autostart: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		listener.Close()
		db.Close()
	}()

	return "http://" + listener.Addr().String(), nil
}
