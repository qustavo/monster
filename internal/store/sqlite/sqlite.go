// Package sqlite is a SQLite-backed implementation of store.Store.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/qustavo/monster/internal/mostro"
	"github.com/qustavo/monster/internal/store"
)

var _ store.Store = (*DB)(nil)

const schema = `
CREATE TABLE IF NOT EXISTS orders (
	id              TEXT PRIMARY KEY,
	node_pubkey     TEXT NOT NULL,
	created_at      INTEGER NOT NULL,
	type            TEXT NOT NULL,
	fiat_code       TEXT NOT NULL,
	status          TEXT NOT NULL,
	amount_sats     INTEGER NOT NULL,
	fiat_amount     TEXT NOT NULL,
	min_amount      TEXT,
	max_amount      TEXT,
	payment_methods TEXT NOT NULL,
	premium         TEXT NOT NULL,
	expires_at      INTEGER NOT NULL,
	platform        TEXT NOT NULL,
	instance_name   TEXT,
	source          TEXT NOT NULL,
	rating          TEXT,
	network         TEXT,
	layer           TEXT,
	name            TEXT,
	geohash         TEXT,
	bond            TEXT,
	updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orders_status    ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_type      ON orders(type);
CREATE INDEX IF NOT EXISTS idx_orders_fiat_code ON orders(fiat_code);
CREATE INDEX IF NOT EXISTS idx_orders_node      ON orders(node_pubkey);

CREATE TABLE IF NOT EXISTS profiles (
	pubkey     TEXT PRIMARY KEY,
	name       TEXT,
	about      TEXT,
	picture    TEXT,
	website    TEXT,
	updated_at INTEGER NOT NULL
);
`

// DB is a SQLite-backed order store.
type DB struct {
	db *sql.DB
}

// Open opens (creating if needed) a SQLite database at path and ensures
// the schema exists.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// modernc.org/sqlite doesn't handle concurrent writers well; serialize.
	db.SetMaxOpenConns(1)

	// Must run before the schema below: it creates an index on
	// node_pubkey, which only exists on a pre-existing database once
	// this rename has happened.
	migrate(db)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	return &DB{db: db}, nil
}

// migrate applies best-effort schema changes to a pre-existing orders
// table that predates the node_pubkey rename and the instance_name/
// min_amount/max_amount columns. Errors are ignored: on a fresh database
// orders doesn't exist yet (nothing to migrate), and on an
// already-migrated database these have already been applied.
func migrate(db *sql.DB) {
	db.Exec(`ALTER TABLE orders RENAME COLUMN maker_pubkey TO node_pubkey`)
	db.Exec(`ALTER TABLE orders ADD COLUMN instance_name TEXT`)
	db.Exec(`ALTER TABLE orders ADD COLUMN min_amount TEXT`)
	db.Exec(`ALTER TABLE orders ADD COLUMN max_amount TEXT`)
}

// Close closes the underlying database.
func (s *DB) Close() error {
	return s.db.Close()
}

// CreateOrder inserts a new order and reports whether it was actually
// inserted. If an order with the same id already exists, it's left
// untouched, created is false, and no error is returned (relays commonly
// redeliver the same event).
func (s *DB) CreateOrder(ctx context.Context, o *mostro.Order) (created bool, err error) {
	pm, err := json.Marshal(o.PaymentMethods)
	if err != nil {
		return false, fmt.Errorf("store: marshal payment methods: %w", err)
	}

	var rating []byte
	if o.Rating != nil {
		rating, err = json.Marshal(o.Rating)
		if err != nil {
			return false, fmt.Errorf("store: marshal rating: %w", err)
		}
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO orders (
			id, node_pubkey, created_at, type, fiat_code, status,
			amount_sats, fiat_amount, min_amount, max_amount, payment_methods, premium, expires_at,
			platform, instance_name, source, rating, network, layer, name, geohash, bond,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		o.ID, o.NodePubkey, o.CreatedAt, string(o.Type), o.FiatCode, string(o.Status),
		o.AmountSats, o.FiatAmount, nullableStr(o.MinAmount), nullableStr(o.MaxAmount), string(pm), o.Premium, o.ExpiresAt,
		o.Platform, nullableStr(o.InstanceName), o.Source, nullable(rating), o.Network, o.Layer, o.Name, o.Geohash, o.Bond,
		o.CreatedAt,
	)
	if err != nil {
		return false, fmt.Errorf("store: create order %s: %w", o.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: create order %s: %w", o.ID, err)
	}
	return n > 0, nil
}

// UpdateOrderStatus sets an order's status. updatedAt should be the
// triggering event's created_at timestamp; the update is skipped if it's
// not newer than the stored one, so out-of-order relay delivery can't
// roll an order's status backwards.
func (s *DB) UpdateOrderStatus(ctx context.Context, id string, status mostro.OrderStatus, updatedAt int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE orders SET status = ?, updated_at = ?
		WHERE id = ? AND updated_at <= ?`,
		string(status), updatedAt, id, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: update order %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update order %s: %w", id, err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// LatestEventTime returns the created_at of the most recent event
// recorded, or 0 if the store is empty.
func (s *DB) LatestEventTime(ctx context.Context) (int64, error) {
	var ts int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(updated_at), 0) FROM orders`).Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("store: latest event time: %w", err)
	}
	return ts, nil
}

// GetOrder retrieves a single order by id.
func (s *DB) GetOrder(ctx context.Context, id string) (*mostro.Order, error) {
	row := s.db.QueryRowContext(ctx, selectCols+` WHERE id = ?`, id)
	o, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get order %s: %w", id, err)
	}
	return o, nil
}

// ListOrders retrieves orders matching filter, newest first.
func (s *DB) ListOrders(ctx context.Context, filter store.OrderFilter) ([]*mostro.Order, error) {
	var where []string
	var args []any

	if filter.Type != "" {
		where = append(where, "type = ?")
		args = append(args, string(filter.Type))
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.FiatCode != "" {
		where = append(where, "fiat_code = ?")
		args = append(args, filter.FiatCode)
	}

	query := selectCols
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list orders: %w", err)
	}
	defer rows.Close()

	var orders []*mostro.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list orders: %w", err)
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list orders: %w", err)
	}
	return orders, nil
}

const selectCols = `SELECT
	id, node_pubkey, created_at, type, fiat_code, status,
	amount_sats, fiat_amount, min_amount, max_amount, payment_methods, premium, expires_at,
	platform, instance_name, source, rating, network, layer, name, geohash, bond
	FROM orders`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(row scanner) (*mostro.Order, error) {
	var o mostro.Order
	var typ, status string
	var pm string
	var minAmount, maxAmount, instanceName, rating sql.NullString

	err := row.Scan(
		&o.ID, &o.NodePubkey, &o.CreatedAt, &typ, &o.FiatCode, &status,
		&o.AmountSats, &o.FiatAmount, &minAmount, &maxAmount, &pm, &o.Premium, &o.ExpiresAt,
		&o.Platform, &instanceName, &o.Source, &rating, &o.Network, &o.Layer, &o.Name, &o.Geohash, &o.Bond,
	)
	if err != nil {
		return nil, err
	}

	o.Type = mostro.OrderType(typ)
	o.Status = mostro.OrderStatus(status)
	o.MinAmount = minAmount.String
	o.MaxAmount = maxAmount.String
	o.InstanceName = instanceName.String

	if err := json.Unmarshal([]byte(pm), &o.PaymentMethods); err != nil {
		return nil, fmt.Errorf("unmarshal payment methods: %w", err)
	}
	if rating.Valid {
		var r mostro.Rating
		if err := json.Unmarshal([]byte(rating.String), &r); err != nil {
			return nil, fmt.Errorf("unmarshal rating: %w", err)
		}
		o.Rating = &r
	}

	return &o, nil
}

// UpsertProfile stores a node's kind-0 profile, replacing any older
// record for the same pubkey (guarded by updated_at, mirroring
// UpdateOrderStatus's staleness guard).
func (s *DB) UpsertProfile(ctx context.Context, pubkey string, p *mostro.Profile, updatedAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO profiles (pubkey, name, about, picture, website, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(pubkey) DO UPDATE SET
			name = excluded.name, about = excluded.about, picture = excluded.picture,
			website = excluded.website, updated_at = excluded.updated_at
		WHERE excluded.updated_at >= profiles.updated_at`,
		pubkey, p.Name, p.About, p.Picture, p.Website, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: upsert profile %s: %w", pubkey, err)
	}
	return nil
}

// GetProfile retrieves a single node's profile.
func (s *DB) GetProfile(ctx context.Context, pubkey string) (*mostro.Profile, error) {
	var p mostro.Profile
	err := s.db.QueryRowContext(ctx,
		`SELECT name, about, picture, website FROM profiles WHERE pubkey = ?`, pubkey,
	).Scan(&p.Name, &p.About, &p.Picture, &p.Website)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get profile %s: %w", pubkey, err)
	}
	return &p, nil
}

// GetProfiles retrieves known profiles for pubkeys in a single query.
func (s *DB) GetProfiles(ctx context.Context, pubkeys []string) (map[string]*mostro.Profile, error) {
	result := make(map[string]*mostro.Profile, len(pubkeys))
	if len(pubkeys) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(pubkeys))
	args := make([]any, len(pubkeys))
	for i, pk := range pubkeys {
		placeholders[i] = "?"
		args[i] = pk
	}

	query := `SELECT pubkey, name, about, picture, website FROM profiles WHERE pubkey IN (` +
		strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: get profiles: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var pubkey string
		var p mostro.Profile
		if err := rows.Scan(&pubkey, &p.Name, &p.About, &p.Picture, &p.Website); err != nil {
			return nil, fmt.Errorf("store: get profiles: %w", err)
		}
		result[pubkey] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: get profiles: %w", err)
	}
	return result, nil
}

func nullable(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
