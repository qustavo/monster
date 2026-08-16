package main

import (
	"context"
	"fmt"
	"log"
	"net"

	"github.com/qustavo/monster/internal/daemon"
	"github.com/qustavo/monster/internal/store/sqlite"
)

// autostart runs an embedded monsterd (relay ingestion + HTTP/SSE API)
// in-process, on a random available port, for when the user hasn't
// pointed monstertui at an existing server via -endpoint. It uses an
// in-memory database — this is a throwaway viewer instance, not meant
// to persist anything across runs — and everything it started is torn
// down when ctx is canceled.
func autostart(ctx context.Context, relays []string) (apiBase string, err error) {
	db, err := sqlite.Open(":memory:")
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
