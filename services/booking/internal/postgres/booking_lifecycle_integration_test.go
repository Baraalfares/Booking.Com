package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBookingLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("BOOKING_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set BOOKING_TEST_DATABASE_URL to run database integration checks")
	}
	ctx := context.Background()
	pool := newLifecycleTestPool(t, databaseURL)
	repository := NewBookingRepository(pool)

	t.Run("confirmation keeps inventory and event replay is idempotent", func(t *testing.T) {
		bookingID, categoryID := insertHeldBooking(t, pool, time.Now().Add(time.Hour))
		result := application.PaymentResult{EventID: fmt.Sprintf("success-%d", bookingID), BookingID: bookingID, Outcome: application.PaymentSucceeded}
		status, err := repository.ApplyPaymentResult(ctx, result)
		if err != nil || status != domain.BookingStatusConfirmed {
			t.Fatalf("first payment result: status=%s err=%v", status, err)
		}
		status, err = repository.ApplyPaymentResult(ctx, result)
		if err != nil || status != domain.BookingStatusConfirmed {
			t.Fatalf("replayed payment result: status=%s err=%v", status, err)
		}
		assertReservedRooms(t, pool, categoryID, 2)
		if _, err := repository.ApplyPaymentResult(ctx, application.PaymentResult{
			EventID: result.EventID, BookingID: bookingID, Outcome: application.PaymentFailed,
		}); !errors.Is(err, application.ErrPaymentEventConflict) {
			t.Fatalf("changed event payload err=%v; want conflict", err)
		}
	})

	t.Run("failure releases every night once", func(t *testing.T) {
		bookingID, categoryID := insertHeldBooking(t, pool, time.Now().Add(time.Hour))
		result := application.PaymentResult{EventID: fmt.Sprintf("failure-%d", bookingID), BookingID: bookingID, Outcome: application.PaymentFailed}
		for i := 0; i < 2; i++ {
			status, err := repository.ApplyPaymentResult(ctx, result)
			if err != nil || status != domain.BookingStatusFailed {
				t.Fatalf("payment attempt %d: status=%s err=%v", i, status, err)
			}
		}
		assertReservedRooms(t, pool, categoryID, 0)
	})

	t.Run("late success expires and releases inventory", func(t *testing.T) {
		bookingID, categoryID := insertHeldBooking(t, pool, time.Now().Add(-time.Minute))
		result := application.PaymentResult{EventID: fmt.Sprintf("late-%d", bookingID), BookingID: bookingID, Outcome: application.PaymentSucceeded}
		status, err := repository.ApplyPaymentResult(ctx, result)
		if status != domain.BookingStatusExpired || !errors.Is(err, application.ErrBookingExpired) {
			t.Fatalf("late payment: status=%s err=%v", status, err)
		}
		assertReservedRooms(t, pool, categoryID, 0)
		status, err = repository.ApplyPaymentResult(ctx, result)
		if status != domain.BookingStatusExpired || !errors.Is(err, application.ErrBookingExpired) {
			t.Fatalf("replayed late payment: status=%s err=%v", status, err)
		}
		assertReservedRooms(t, pool, categoryID, 0)
	})

	t.Run("payment waiting on a row lock cannot confirm after expiry", func(t *testing.T) {
		bookingID, categoryID := insertHeldBooking(t, pool, time.Now().Add(800*time.Millisecond))
		lockTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lockTx.Rollback(ctx)
		if _, err := lockTx.Exec(ctx, `SELECT id FROM bookings WHERE id = $1 FOR UPDATE`, bookingID); err != nil {
			t.Fatal(err)
		}
		type paymentAttempt struct {
			status domain.BookingStatus
			err    error
		}
		attempts := make(chan paymentAttempt, 1)
		go func() {
			status, err := repository.ApplyPaymentResult(ctx, application.PaymentResult{
				EventID: fmt.Sprintf("waited-%d", bookingID), BookingID: bookingID,
				Outcome: application.PaymentSucceeded,
			})
			attempts <- paymentAttempt{status, err}
		}()
		time.Sleep(time.Second)
		if err := lockTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		attempt := <-attempts
		if attempt.status != domain.BookingStatusExpired || !errors.Is(attempt.err, application.ErrBookingExpired) {
			t.Fatalf("late payment after row lock: status=%s err=%v", attempt.status, attempt.err)
		}
		assertReservedRooms(t, pool, categoryID, 0)
	})

	t.Run("late success after worker expiry still consumes event ID", func(t *testing.T) {
		bookingID, categoryID := insertHeldBooking(t, pool, time.Now().Add(-time.Minute))
		if count, err := repository.ExpirePending(ctx, 1); err != nil || count != 1 {
			t.Fatalf("expire pending booking: count=%d err=%v", count, err)
		}
		eventID := fmt.Sprintf("worker-first-%d", bookingID)
		status, err := repository.ApplyPaymentResult(ctx, application.PaymentResult{
			EventID: eventID, BookingID: bookingID, Outcome: application.PaymentSucceeded,
		})
		if status != domain.BookingStatusExpired || !errors.Is(err, application.ErrBookingExpired) {
			t.Fatalf("late success after worker: status=%s err=%v", status, err)
		}
		assertReservedRooms(t, pool, categoryID, 0)
		otherBookingID, _ := insertHeldBooking(t, pool, time.Now().Add(time.Hour))
		if _, err := repository.ApplyPaymentResult(ctx, application.PaymentResult{
			EventID: eventID, BookingID: otherBookingID, Outcome: application.PaymentSucceeded,
		}); !errors.Is(err, application.ErrPaymentEventConflict) {
			t.Fatalf("reused late payment event ID: got %v; want conflict", err)
		}
	})

	t.Run("concurrent expiry workers claim one booking once", func(t *testing.T) {
		bookingID, categoryID := insertHeldBooking(t, pool, time.Now().Add(-time.Minute))
		_ = bookingID
		var wg sync.WaitGroup
		counts := make([]int, 2)
		errorsByWorker := make([]error, 2)
		for i := range counts {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				counts[index], errorsByWorker[index] = repository.ExpirePending(ctx, 1)
			}(i)
		}
		wg.Wait()
		if errorsByWorker[0] != nil || errorsByWorker[1] != nil || counts[0]+counts[1] != 1 {
			t.Fatalf("worker counts=%v errors=%v; want one expiry", counts, errorsByWorker)
		}
		assertReservedRooms(t, pool, categoryID, 0)
	})
}

func newLifecycleTestPool(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("booking_lifecycle_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrationPaths, err := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "V*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(migrationPaths)
	if len(migrationPaths) < 6 {
		t.Fatalf("need V1-V6 migrations, got %v", migrationPaths)
	}
	for _, path := range migrationPaths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	return pool
}

func insertHeldBooking(t *testing.T, pool *pgxpool.Pool, expiresAt time.Time) (int64, string) {
	t.Helper()
	ctx := context.Background()
	var hotelID, categoryID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO hotels(name,address,currency)
		VALUES ('Lifecycle Test Hotel','Test Address','USD') RETURNING id::text
	`).Scan(&hotelID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO room_categories(hotel_id,name,max_guests,nightly_price_minor)
		VALUES ($1::uuid,'Standard',2,1000) RETURNING id::text
	`, hotelID).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	for _, stayDate := range []string{"2030-01-01", "2030-01-02"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO category_night_inventory(category_id,stay_date,reserved_rooms)
			VALUES ($1::uuid,$2::date,2)
		`, categoryID, stayDate); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte(categoryID))
	var bookingID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO bookings(guest_id,hotel_id,check_in,check_out,status,total_minor,currency,idempotency_key,request_hash,expires_at)
		VALUES ($1,$2::uuid,'2030-01-01','2030-01-03','PENDING_PAYMENT',4000,'USD',$3,$4,$5)
		RETURNING id
	`, "lifecycle-test-guest-"+categoryID, hotelID, "lifecycle-test-"+categoryID, hash[:], expiresAt).Scan(&bookingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO booking_items(booking_id,hotel_id,category_id,room_count,guest_count,nightly_price_minor,line_total_minor)
		VALUES ($1,$2::uuid,$3::uuid,2,2,1000,4000)
	`, bookingID, hotelID, categoryID); err != nil {
		t.Fatal(err)
	}
	for _, stayDate := range []string{"2030-01-01", "2030-01-02"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO booking_nights(booking_id,category_id,stay_date,room_count)
			VALUES ($1,$2::uuid,$3::date,2)
		`, bookingID, categoryID, stayDate); err != nil {
			t.Fatal(err)
		}
	}
	return bookingID, categoryID
}

func assertReservedRooms(t *testing.T, pool *pgxpool.Pool, categoryID string, want int) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT reserved_rooms FROM category_night_inventory
		WHERE category_id = $1::uuid ORDER BY stay_date
	`, categoryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var got int
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("night %d reserved_rooms=%d; want %d", count, got, want)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("got %d nights; want 2", count)
	}
}
