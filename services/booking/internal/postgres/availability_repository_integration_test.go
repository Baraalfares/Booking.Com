package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"example.com/bookingcom/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run with DATABASE_URL set after applying migrations and loading hotel data.
func TestAvailabilityRepositoryAgainstLocalDatabase(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set DATABASE_URL to run the local database integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	var hotelID string
	if err := pool.QueryRow(ctx, "SELECT hotel_id::text FROM room_categories LIMIT 1").Scan(&hotelID); err != nil {
		t.Fatalf("find hotel with categories: %v", err)
	}
	checkIn := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	categories, err := NewAvailabilityRepository(pool).ListByHotelAndStay(
		ctx, hotelID, checkIn, checkIn.AddDate(0, 0, 2),
	)
	if err != nil {
		t.Fatalf("query availability: %v", err)
	}
	if len(categories) == 0 {
		t.Fatal("expected categories for hotel")
	}
	for _, category := range categories {
		if category.RemainingRooms < 0 {
			t.Fatalf("negative remaining rooms for category %s", category.CategoryID)
		}
	}
}

func TestFutureRoomActivationIncreasesAvailability(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set DATABASE_URL to run the local database integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	checkIn := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	checkOut := checkIn.AddDate(0, 0, 1)
	var roomID, categoryID, hotelID string
	err = tx.QueryRow(ctx, `
		SELECT room.id::text, room.category_id::text, category.hotel_id::text
		FROM rooms AS room
		JOIN room_categories AS category ON category.id = room.category_id
		WHERE room.status = 'out_of_service'
		  AND NOT EXISTS (
			SELECT 1 FROM room_status_overrides AS status_override
			WHERE status_override.room_id = room.id
			  AND daterange(status_override.starts_on, status_override.ends_on, '[)') @> $1::date
		  )
		LIMIT 1
	`, checkIn).Scan(&roomID, &categoryID, &hotelID)
	if err != nil {
		t.Fatalf("find room to activate for a future night: %v", err)
	}
	repository := &AvailabilityRepository{db: tx}
	before, err := repository.ListByHotelAndStay(ctx, hotelID, checkIn, checkOut)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO room_status_overrides (room_id, starts_on, ends_on, status)
		VALUES ($1::uuid, $2::date, $3::date, 'active')
	`, roomID, checkIn, checkOut); err != nil {
		t.Fatal(err)
	}
	after, err := repository.ListByHotelAndStay(ctx, hotelID, checkIn, checkOut)
	if err != nil {
		t.Fatal(err)
	}

	remaining := func(categories []application.CategoryNightAvailability) (int, bool) {
		for _, category := range categories {
			if category.CategoryID == categoryID {
				return category.RemainingRooms, true
			}
		}
		return 0, false
	}
	beforeCount, beforeFound := remaining(before)
	afterCount, afterFound := remaining(after)
	if !beforeFound || !afterFound {
		t.Fatalf("category %s missing from availability result", categoryID)
	}
	if afterCount != beforeCount+1 {
		t.Fatalf("future activation changed remaining rooms from %d to %d, want +1", beforeCount, afterCount)
	}
}
