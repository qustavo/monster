package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/qustavo/monster/internal/api"
	"github.com/qustavo/monster/internal/mostro"
)

// fetchOrders retrieves the current pending order book from monsterd.
func fetchOrders(ctx context.Context, apiBase string) ([]*mostro.Order, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/orders?status=pending", nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /orders: unexpected status %s", resp.Status)
	}

	var orders []*mostro.Order
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, fmt.Errorf("decode orders: %w", err)
	}
	return orders, nil
}

// streamOrders connects to monsterd's SSE feed and invokes onEvent for
// each order event until ctx is canceled or the connection ends.
func streamOrders(ctx context.Context, apiBase string, onEvent func(api.Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/orders/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /orders/stream: unexpected status %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var ev api.Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		onEvent(ev)
	}
	return scanner.Err()
}

// run fetches the order book and tails the live stream, sending results
// to the TUI as tea messages. On disconnect it retries after a short
// delay, refetching the full order book each time to resync.
func run(ctx context.Context, p *tea.Program, apiBase string) {
	for {
		orders, err := fetchOrders(ctx, apiBase)
		if err != nil {
			p.Send(errMsg{err})
		} else {
			p.Send(ordersLoadedMsg(orders))
		}

		err = streamOrders(ctx, apiBase, func(ev api.Event) {
			p.Send(orderEventMsg(ev))
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.Send(errMsg{fmt.Errorf("stream: %w", err)})
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
