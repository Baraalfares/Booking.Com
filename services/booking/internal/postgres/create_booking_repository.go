package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type BookingRepository struct {
	pool *pgxpool.Pool
}

func NewBookingRepository(pool *pgxpool.Pool) *BookingRepository {
	return &BookingRepository{pool: pool}
}

type pricedBookingItem struct {
	application.CreateBookingItem
	nightlyPriceMinor int64
	lineTotalMinor    int64
}

type nightAllocation struct {
	categoryID string
	date       time.Time
	roomCount  int
}

// Create commits the booking, its price snapshots, and every requested night
// together. A shortage or any database error rolls the whole transaction back.
func (r *BookingRepository) Create(ctx context.Context, command application.CreateBookingCommand) (domain.Booking, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		booking, err := r.createOnce(ctx, command)
		if err == nil {
			return booking, nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "40P01" && pgErr.Code != "40001") {
			return domain.Booking{}, err
		}
		if attempt == maxAttempts-1 {
			return domain.Booking{}, fmt.Errorf("%w: transaction retries exhausted: %v", application.ErrBookingBusy, err)
		}
		wait := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
		select {
		case <-wait.C:
		case <-ctx.Done():
			wait.Stop()
			return domain.Booking{}, ctx.Err()
		}
	}
	return domain.Booking{}, application.ErrBookingBusy
}

func (r *BookingRepository) createOnce(ctx context.Context, command application.CreateBookingCommand) (domain.Booking, error) {
	hash, err := bookingRequestHash(command)
	if err != nil {
		return domain.Booking{}, fmt.Errorf("hash booking request: %w", err)
	}
	items := append([]application.CreateBookingItem(nil), command.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].CategoryID < items[j].CategoryID })

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.Booking{}, fmt.Errorf("begin booking transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var currency string
	err = tx.QueryRow(ctx, `SELECT currency FROM hotels WHERE id = $1::uuid FOR SHARE`, command.HotelID).Scan(&currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Booking{}, application.ErrHotelNotFound
	}
	if err != nil {
		return domain.Booking{}, fmt.Errorf("find booking hotel: %w", err)
	}

	var bookingID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO bookings (
			guest_id, hotel_id, check_in, check_out, status, currency,
			idempotency_key, request_hash
		)
		VALUES ($1, $2::uuid, $3::date, $4::date, 'PENDING_PAYMENT', $5, $6, $7)
		ON CONFLICT (guest_id, idempotency_key) DO NOTHING
		RETURNING id`, command.GuestID, command.HotelID, command.CheckIn,
		command.CheckOut, currency, command.IdempotencyKey, hash[:]).Scan(&bookingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return existingBookingForRequest(ctx, tx, command.GuestID, command.IdempotencyKey, hash)
	}
	if err != nil {
		return domain.Booking{}, fmt.Errorf("insert booking: %w", err)
	}
	// A retry must reach the idempotency lookup before this time-sensitive rule.
	// A new request with a past check-in rolls the uncommitted insert back.
	if command.CheckIn.Before(time.Now().UTC().Truncate(24 * time.Hour)) {
		return domain.Booking{}, fmt.Errorf("%w: check-in must not be in the past", application.ErrInvalidBooking)
	}

	nights := int(command.CheckOut.Sub(command.CheckIn).Hours() / 24)
	pricedItems := make([]pricedBookingItem, 0, len(items))
	allocations := make([]nightAllocation, 0, len(items)*nights)
	var totalMinor int64
	for _, item := range items {
		var maxGuests int
		var nightlyPriceMinor int64
		err = tx.QueryRow(ctx, `
			SELECT max_guests, nightly_price_minor
			FROM room_categories
			WHERE hotel_id = $1::uuid AND id = $2::uuid
			FOR SHARE`, command.HotelID, item.CategoryID).Scan(&maxGuests, &nightlyPriceMinor)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Booking{}, application.ErrCategoryNotFound
		}
		if err != nil {
			return domain.Booking{}, fmt.Errorf("find booking category: %w", err)
		}
		if int64(item.GuestCount) > int64(item.RoomCount)*int64(maxGuests) {
			return domain.Booking{}, application.ErrGuestCapacityExceeded
		}
		lineTotal, ok := checkedLineTotal(nightlyPriceMinor, item.RoomCount, nights)
		if !ok || totalMinor > math.MaxInt64-lineTotal {
			return domain.Booking{}, fmt.Errorf("%w: price exceeds supported amount", application.ErrInvalidBooking)
		}
		totalMinor += lineTotal
		pricedItems = append(pricedItems, pricedBookingItem{
			CreateBookingItem: item,
			nightlyPriceMinor: nightlyPriceMinor,
			lineTotalMinor:    lineTotal,
		})
		for date := command.CheckIn; date.Before(command.CheckOut); date = date.AddDate(0, 0, 1) {
			allocations = append(allocations, nightAllocation{item.CategoryID, date, item.RoomCount})
		}
	}

	// A stable order prevents two multi-category bookings from locking nights in
	// opposite orders and deadlocking each other.
	sort.Slice(allocations, func(i, j int) bool {
		if allocations[i].categoryID == allocations[j].categoryID {
			return allocations[i].date.Before(allocations[j].date)
		}
		return allocations[i].categoryID < allocations[j].categoryID
	})
	for _, allocation := range allocations {
		if err := reserveCategoryNight(ctx, tx, allocation); err != nil {
			return domain.Booking{}, err
		}
	}

	for _, item := range pricedItems {
		_, err = tx.Exec(ctx, `
			INSERT INTO booking_items (
				booking_id, hotel_id, category_id, room_count, guest_count,
				nightly_price_minor, line_total_minor
			)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7)`,
			bookingID, command.HotelID, item.CategoryID, item.RoomCount,
			item.GuestCount, item.nightlyPriceMinor, item.lineTotalMinor)
		if err != nil {
			return domain.Booking{}, fmt.Errorf("insert booking item: %w", err)
		}
	}
	for _, allocation := range allocations {
		_, err = tx.Exec(ctx, `
			INSERT INTO booking_nights (booking_id, category_id, stay_date, room_count)
			VALUES ($1, $2::uuid, $3::date, $4)`,
			bookingID, allocation.categoryID, allocation.date, allocation.roomCount)
		if err != nil {
			return domain.Booking{}, fmt.Errorf("insert booking night: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET total_minor = $2, expires_at = clock_timestamp() + INTERVAL '15 minutes'
		WHERE id = $1`, bookingID, totalMinor)
	if err != nil {
		return domain.Booking{}, fmt.Errorf("save booking total: %w", err)
	}
	booking, err := loadBookingTx(ctx, tx, bookingID)
	if err != nil {
		return domain.Booking{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Booking{}, fmt.Errorf("commit booking: %w", err)
	}
	return booking, nil
}

func reserveCategoryNight(ctx context.Context, tx pgx.Tx, allocation nightAllocation) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO category_night_inventory (category_id, stay_date)
		VALUES ($1::uuid, $2::date)
		ON CONFLICT (category_id, stay_date) DO NOTHING`, allocation.categoryID, allocation.date)
	if err != nil {
		return fmt.Errorf("create category night inventory: %w", err)
	}
	var reserved int
	err = tx.QueryRow(ctx, `
		SELECT reserved_rooms FROM category_night_inventory
		WHERE category_id = $1::uuid AND stay_date = $2::date
		FOR UPDATE`, allocation.categoryID, allocation.date).Scan(&reserved)
	if err != nil {
		return fmt.Errorf("lock category night inventory: %w", err)
	}
	var capacity int
	err = tx.QueryRow(ctx, `
		SELECT count(*)::integer
		FROM rooms AS r
		LEFT JOIN room_status_overrides AS o
			ON o.room_id = r.id AND o.starts_on <= $2::date AND $2::date < o.ends_on
		WHERE r.category_id = $1::uuid
			AND COALESCE(o.status, r.status) = 'active'`,
		allocation.categoryID, allocation.date).Scan(&capacity)
	if err != nil {
		return fmt.Errorf("count available category rooms: %w", err)
	}
	if allocation.roomCount > capacity-reserved {
		return fmt.Errorf("%w: category %s on %s", application.ErrInsufficientAvailability,
			allocation.categoryID, allocation.date.Format(time.DateOnly))
	}
	_, err = tx.Exec(ctx, `
		UPDATE category_night_inventory
		SET reserved_rooms = reserved_rooms + $3
		WHERE category_id = $1::uuid AND stay_date = $2::date`,
		allocation.categoryID, allocation.date, allocation.roomCount)
	if err != nil {
		return fmt.Errorf("reserve category night: %w", err)
	}
	return nil
}

func checkedLineTotal(nightlyPriceMinor int64, roomCount, nights int) (int64, bool) {
	if nightlyPriceMinor < 0 || roomCount < 1 || nights < 1 {
		return 0, false
	}
	if nightlyPriceMinor > math.MaxInt64/int64(roomCount) {
		return 0, false
	}
	roomTotal := nightlyPriceMinor * int64(roomCount)
	if roomTotal > math.MaxInt64/int64(nights) {
		return 0, false
	}
	return roomTotal * int64(nights), true
}

func bookingRequestHash(command application.CreateBookingCommand) ([32]byte, error) {
	items := append([]application.CreateBookingItem(nil), command.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].CategoryID < items[j].CategoryID })
	payload, err := json.Marshal(struct {
		GuestID  string
		HotelID  string
		CheckIn  string
		CheckOut string
		Items    []application.CreateBookingItem
	}{command.GuestID, command.HotelID, command.CheckIn.Format(time.DateOnly),
		command.CheckOut.Format(time.DateOnly), items})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func existingBookingForRequest(ctx context.Context, tx pgx.Tx, guestID, key string, hash [32]byte) (domain.Booking, error) {
	var bookingID int64
	var storedHash []byte
	err := tx.QueryRow(ctx, `
		SELECT id, request_hash FROM bookings
		WHERE guest_id = $1 AND idempotency_key = $2`, guestID, key).Scan(&bookingID, &storedHash)
	if err != nil {
		return domain.Booking{}, fmt.Errorf("find existing idempotent booking: %w", err)
	}
	if len(storedHash) != len(hash) {
		return domain.Booking{}, application.ErrIdempotencyConflict
	}
	for i := range hash {
		if storedHash[i] != hash[i] {
			return domain.Booking{}, application.ErrIdempotencyConflict
		}
	}
	return loadBookingTx(ctx, tx, bookingID)
}

func loadBookingTx(ctx context.Context, tx pgx.Tx, bookingID int64) (domain.Booking, error) {
	var booking domain.Booking
	var status string
	err := tx.QueryRow(ctx, `
		SELECT id, guest_id, hotel_id::text, check_in, check_out, status,
			total_minor, currency, expires_at
		FROM bookings WHERE id = $1`, bookingID).Scan(
		&booking.BookingID, &booking.GuestID, &booking.HotelID,
		&booking.CheckIn, &booking.CheckOut, &status,
		&booking.TotalMinor, &booking.Currency, &booking.ExpiresAt)
	if err != nil {
		return domain.Booking{}, fmt.Errorf("read booking: %w", err)
	}
	booking.Status = domain.BookingStatus(status)
	rows, err := tx.Query(ctx, `
		SELECT category_id::text, room_count, guest_count,
			nightly_price_minor, line_total_minor
		FROM booking_items WHERE booking_id = $1 ORDER BY category_id`, bookingID)
	if err != nil {
		return domain.Booking{}, fmt.Errorf("read booking items: %w", err)
	}
	defer rows.Close()
	booking.Items = make([]domain.BookingItem, 0)
	for rows.Next() {
		var item domain.BookingItem
		if err := rows.Scan(&item.CategoryID, &item.RoomCount, &item.GuestCount,
			&item.NightlyPriceMinor, &item.LineTotalMinor); err != nil {
			return domain.Booking{}, fmt.Errorf("scan booking item: %w", err)
		}
		booking.Items = append(booking.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Booking{}, fmt.Errorf("iterate booking items: %w", err)
	}
	return booking, nil
}
