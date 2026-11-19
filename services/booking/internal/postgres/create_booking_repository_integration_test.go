package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/bookingcom/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run with BOOKING_TEST_DATABASE_URL set to a disposable development database
// that already has the Flyway migrations applied.
func TestCreateBookingTransaction(t *testing.T) {
	url := os.Getenv("BOOKING_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("BOOKING_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	var hotelID string
	err = pool.QueryRow(ctx, `
		INSERT INTO hotels (name, address, currency)
		VALUES ('Booking transaction test hotel', 'test-only', 'USD')
		RETURNING id::text`).Scan(&hotelID)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteBookingTestHotel(t, pool, hotelID)

	roomNumber := 100
	addCategory := func(id, name string, rooms int, price int64) string {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO room_categories (id, hotel_id, name, max_guests, nightly_price_minor)
			VALUES ($1::uuid, $2::uuid, $3, 4, $4)`, id, hotelID, name, price)
		if err != nil {
			t.Fatal(err)
		}
		for range rooms {
			roomNumber++
			_, err := pool.Exec(ctx, `
				INSERT INTO rooms (hotel_id, category_id, number, status)
				VALUES ($1::uuid, $2::uuid, $3, 'active')`, hotelID, id, fmt.Sprint(roomNumber))
			if err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	// Stable category ID order makes the shortage test prove that a successful
	// first-night reservation rolls back when a later category is sold out.
	suffix := strings.ReplaceAll(hotelID, "-", "")
	suffix = suffix[len(suffix)-12:]
	standardID := addCategory("10000000-0000-4000-8000-"+suffix, "Standard", 2, 1000)
	familyID := addCategory("20000000-0000-4000-8000-"+suffix, "Family", 1, 2500)
	lastRoomID := addCategory("30000000-0000-4000-8000-"+suffix, "Last room", 1, 3000)

	checkIn := time.Now().UTC().AddDate(0, 0, 100).Truncate(24 * time.Hour)
	checkOut := checkIn.AddDate(0, 0, 3)
	useCase := application.CreateBooking{Bookings: NewBookingRepository(pool)}
	command := application.CreateBookingCommand{
		GuestID: "booking-test-guest", HotelID: hotelID,
		CheckIn: checkIn, CheckOut: checkOut, IdempotencyKey: "initial-request",
		Items: []application.CreateBookingItem{
			{CategoryID: familyID, RoomCount: 1, GuestCount: 2},
			{CategoryID: standardID, RoomCount: 1, GuestCount: 2},
		},
	}
	booking, err := useCase.Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if booking.TotalMinor != 10500 || len(booking.Items) != 2 {
		t.Fatalf("unexpected booking snapshot: %+v", booking)
	}
	for _, item := range booking.Items {
		if item.CategoryID == standardID && (item.NightlyPriceMinor != 1000 || item.LineTotalMinor != 3000) {
			t.Fatalf("standard price snapshot changed: %+v", item)
		}
	}
	if remaining := time.Until(booking.ExpiresAt); remaining < 14*time.Minute || remaining > 16*time.Minute {
		t.Fatalf("new booking hold expires too early or late: %v remaining", remaining)
	}

	// UUID casing is presentation only: it must not change the request hash,
	// duplicate detection, or the order in which inventory locks are acquired.
	upperCaseCommand := command
	upperCaseCommand.HotelID = strings.ToUpper(command.HotelID)
	upperCaseCommand.Items = append([]application.CreateBookingItem(nil), command.Items...)
	for i := range upperCaseCommand.Items {
		upperCaseCommand.Items[i].CategoryID = strings.ToUpper(upperCaseCommand.Items[i].CategoryID)
	}
	upperCaseReplay, err := useCase.Execute(ctx, upperCaseCommand)
	if err != nil || upperCaseReplay.BookingID != booking.BookingID {
		t.Fatalf("UUID casing changed idempotent booking: replay=%+v err=%v", upperCaseReplay, err)
	}
	duplicateCategory := command
	duplicateCategory.IdempotencyKey = "duplicate-category-casing"
	duplicateCategory.Items = []application.CreateBookingItem{
		{CategoryID: standardID, RoomCount: 1, GuestCount: 1},
		{CategoryID: strings.ToUpper(standardID), RoomCount: 1, GuestCount: 1},
	}
	if _, err := useCase.Execute(ctx, duplicateCategory); !errors.Is(err, application.ErrInvalidBooking) {
		t.Fatalf("alternate-case duplicate category: got %v; want invalid booking", err)
	}

	// Model a retry after midnight by inserting an already-created request with
	// yesterday's check-in. The same key must replay, while a new key is invalid.
	pastCommand := application.CreateBookingCommand{
		GuestID: "past-replay-guest", HotelID: hotelID,
		CheckIn:        time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1),
		CheckOut:       time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1),
		IdempotencyKey: "past-replay",
		Items:          []application.CreateBookingItem{{CategoryID: standardID, RoomCount: 1, GuestCount: 1}},
	}
	pastHash, err := bookingRequestHash(pastCommand)
	if err != nil {
		t.Fatal(err)
	}
	var pastBookingID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO bookings(guest_id, hotel_id, check_in, check_out, status,
			total_minor, currency, idempotency_key, request_hash)
		VALUES($1, $2::uuid, $3::date, $4::date, 'CONFIRMED', 0, 'USD', $5, $6)
		RETURNING id`, pastCommand.GuestID, hotelID, pastCommand.CheckIn,
		pastCommand.CheckOut, pastCommand.IdempotencyKey, pastHash[:]).Scan(&pastBookingID)
	if err != nil {
		t.Fatal(err)
	}
	pastReplay, err := useCase.Execute(ctx, pastCommand)
	if err != nil || pastReplay.BookingID != pastBookingID {
		t.Fatalf("past-date idempotent replay: replay=%+v err=%v", pastReplay, err)
	}
	pastCommand.IdempotencyKey = "new-past-request"
	if _, err := useCase.Execute(ctx, pastCommand); !errors.Is(err, application.ErrInvalidBooking) {
		t.Fatalf("new past-date request: got %v; want invalid booking", err)
	}

	// A retry with items in another order must return the same booking, even
	// after the current catalog price changes.
	_, err = pool.Exec(ctx, `UPDATE room_categories SET nightly_price_minor = 9000 WHERE id = $1::uuid`, standardID)
	if err != nil {
		t.Fatal(err)
	}
	command.Items[0], command.Items[1] = command.Items[1], command.Items[0]
	replay, err := useCase.Execute(ctx, command)
	if err != nil || replay.BookingID != booking.BookingID || replay.TotalMinor != booking.TotalMinor {
		t.Fatalf("idempotent replay changed booking: replay=%+v err=%v", replay, err)
	}
	command.Items[0].RoomCount = 2
	_, err = useCase.Execute(ctx, command)
	if !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatalf("changed request with same key: got %v", err)
	}

	// Family has only one room, already reserved. The Standard room must not
	// remain reserved when the second category fails.
	command.Items[0].RoomCount = 1
	command.IdempotencyKey = "unavailable-request"
	_, err = useCase.Execute(ctx, command)
	if !errors.Is(err, application.ErrInsufficientAvailability) {
		t.Fatalf("expected whole booking to fail on shortage, got %v", err)
	}
	var reserved int
	err = pool.QueryRow(ctx, `
		SELECT reserved_rooms FROM category_night_inventory
		WHERE category_id = $1::uuid AND stay_date = $2::date`, standardID, checkIn).Scan(&reserved)
	if err != nil || reserved != 1 {
		t.Fatalf("failed booking changed first category inventory: reserved=%d err=%v", reserved, err)
	}

	// Two concurrent requests compete for one room on the same three nights.
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := useCase.Execute(ctx, application.CreateBookingCommand{
				GuestID: fmt.Sprintf("concurrent-guest-%d", i), HotelID: hotelID,
				CheckIn: checkIn, CheckOut: checkOut,
				IdempotencyKey: fmt.Sprintf("concurrent-request-%d", i),
				Items:          []application.CreateBookingItem{{CategoryID: lastRoomID, RoomCount: 1, GuestCount: 1}},
			})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	succeeded, unavailable := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, application.ErrInsufficientAvailability):
			unavailable++
		default:
			t.Fatalf("unexpected concurrent booking error: %v", err)
		}
	}
	if succeeded != 1 || unavailable != 1 {
		t.Fatalf("concurrent bookings: succeeded=%d unavailable=%d", succeeded, unavailable)
	}
}

func deleteBookingTestHotel(t *testing.T, pool *pgxpool.Pool, hotelID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	statements := []string{
		`DELETE FROM booking_nights WHERE booking_id IN (SELECT id FROM bookings WHERE hotel_id = $1::uuid)`,
		`DELETE FROM booking_items WHERE booking_id IN (SELECT id FROM bookings WHERE hotel_id = $1::uuid)`,
		`DELETE FROM bookings WHERE hotel_id = $1::uuid`,
		`DELETE FROM category_night_inventory WHERE category_id IN (SELECT id FROM room_categories WHERE hotel_id = $1::uuid)`,
		`DELETE FROM room_status_overrides WHERE room_id IN (SELECT id FROM rooms WHERE hotel_id = $1::uuid)`,
		`DELETE FROM rooms WHERE hotel_id = $1::uuid`,
		`DELETE FROM room_categories WHERE hotel_id = $1::uuid`,
		`DELETE FROM hotels WHERE id = $1::uuid`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement, hotelID); err != nil {
			t.Errorf("clean booking test fixture: %v", err)
		}
	}
}
