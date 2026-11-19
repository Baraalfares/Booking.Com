package postgres

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOperationalStatusCannotReduceHeldCapacity(t *testing.T) {
	databaseURL := os.Getenv("BOOKING_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set BOOKING_TEST_DATABASE_URL to run database integration checks")
	}
	pool := newLifecycleTestPool(t, databaseURL)
	ctx := context.Background()
	var hotelID, categoryID, roomID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO hotels(name, address, currency)
		VALUES ('Capacity Test Hotel', 'Test Address', 'USD') RETURNING id::text
	`).Scan(&hotelID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO room_categories(hotel_id, name, max_guests, nightly_price_minor)
		VALUES ($1::uuid, 'Standard', 2, 1000) RETURNING id::text
	`, hotelID).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO rooms(hotel_id, category_id, number, status)
		VALUES ($1::uuid, $2::uuid, '101', 'active') RETURNING id::text
	`, hotelID, categoryID).Scan(&roomID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO category_night_inventory(category_id, stay_date, reserved_rooms)
		VALUES ($1::uuid, '2030-01-01', 1)
	`, categoryID); err != nil {
		t.Fatal(err)
	}

	_, err := pool.Exec(ctx, `UPDATE rooms SET status = 'out_of_service' WHERE id = $1::uuid`, roomID)
	assertCapacityViolation(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO room_status_overrides(room_id, starts_on, ends_on, status)
		VALUES ($1::uuid, '2030-01-01', '2030-01-02', 'out_of_service')
	`, roomID)
	assertCapacityViolation(t, err)

	if _, err := pool.Exec(ctx, `
		UPDATE category_night_inventory SET reserved_rooms = 0
		WHERE category_id = $1::uuid AND stay_date = '2030-01-01'
	`, categoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rooms SET status = 'out_of_service' WHERE id = $1::uuid`, roomID); err != nil {
		t.Fatalf("released room should become unavailable: %v", err)
	}
	var overrideID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO room_status_overrides(room_id, starts_on, ends_on, status)
		VALUES ($1::uuid, '2030-01-01', '2030-01-02', 'active') RETURNING id
	`, roomID).Scan(&overrideID); err != nil {
		t.Fatalf("future activation should be allowed: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE category_night_inventory SET reserved_rooms = 1
		WHERE category_id = $1::uuid AND stay_date = '2030-01-01'
	`, categoryID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `DELETE FROM room_status_overrides WHERE id = $1`, overrideID)
	assertCapacityViolation(t, err)

	// A completed stay keeps its ledger but must not prevent an operator from
	// changing today's room status.
	if _, err := pool.Exec(ctx, `
		UPDATE category_night_inventory SET reserved_rooms = 0
		WHERE category_id = $1::uuid AND stay_date = '2030-01-01'
	`, categoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM room_status_overrides WHERE id = $1`, overrideID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rooms SET status = 'active' WHERE id = $1::uuid`, roomID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO category_night_inventory(category_id, stay_date, reserved_rooms)
		VALUES ($1::uuid, '2020-01-01', 1)
	`, categoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rooms SET status = 'out_of_service' WHERE id = $1::uuid`, roomID); err != nil {
		t.Fatalf("historical stay blocked room retirement: %v", err)
	}

	// An operational writer with a fixed old snapshot could miss newly booked
	// nights, so the trigger rejects isolation levels above READ COMMITTED.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE rooms SET status = 'active' WHERE id = $1::uuid`, roomID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25000" {
		t.Fatalf("repeatable-read status update error=%v; want isolation rejection", err)
	}
}

func assertCapacityViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("capacity reduction error=%v; want PostgreSQL check violation", err)
	}
}
