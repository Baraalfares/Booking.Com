package postgres

import (
	"context"
	"errors"
	"fmt"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	_ application.PaymentResultRepository   = (*BookingRepository)(nil)
	_ application.ExpiringBookingRepository = (*BookingRepository)(nil)
)

// ApplyPaymentResult records a payment event and changes the booking status in
// one transaction. The booking row lock serializes payment events and expiry.
func (r *BookingRepository) ApplyPaymentResult(ctx context.Context, result application.PaymentResult) (domain.BookingStatus, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", fmt.Errorf("begin payment result transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentValue string
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM bookings
		WHERE id = $1
		FOR UPDATE
	`, result.BookingID).Scan(&currentValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", application.ErrBookingNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock booking %d: %w", result.BookingID, err)
	}
	current := domain.BookingStatus(currentValue)
	// Check the wall clock after acquiring the row lock. PostgreSQL now() is
	// fixed at transaction start and could predate a long lock wait.
	var pastExpiry bool
	err = tx.QueryRow(ctx, `
		SELECT expires_at <= clock_timestamp() FROM bookings WHERE id = $1
	`, result.BookingID).Scan(&pastExpiry)
	if err != nil {
		return "", fmt.Errorf("check booking %d expiry: %w", result.BookingID, err)
	}

	var existingBookingID int64
	var existingOutcomeValue string
	err = tx.QueryRow(ctx, `
		SELECT booking_id, outcome
		FROM payment_events
		WHERE event_id = $1
	`, result.EventID).Scan(&existingBookingID, &existingOutcomeValue)
	if err == nil {
		if existingBookingID != result.BookingID || application.PaymentOutcome(existingOutcomeValue) != result.Outcome {
			return "", application.ErrPaymentEventConflict
		}
		if current == domain.BookingStatusExpired && result.Outcome == application.PaymentSucceeded {
			return current, application.ErrBookingExpired
		}
		return current, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("check payment event %q: %w", result.EventID, err)
	}

	var next domain.BookingStatus
	lateSuccess := false
	switch current {
	case domain.BookingStatusPendingPayment:
		if pastExpiry {
			next = domain.BookingStatusExpired
			lateSuccess = result.Outcome == application.PaymentSucceeded
			if err := releaseBookingNights(ctx, tx, result.BookingID); err != nil {
				return "", err
			}
		} else if result.Outcome == application.PaymentSucceeded {
			next = domain.BookingStatusConfirmed
		} else {
			next = domain.BookingStatusFailed
			if err := releaseBookingNights(ctx, tx, result.BookingID); err != nil {
				return "", err
			}
		}
	case domain.BookingStatusConfirmed:
		if result.Outcome != application.PaymentSucceeded {
			return "", application.ErrBookingStateConflict
		}
		next = current
	case domain.BookingStatusFailed:
		if result.Outcome != application.PaymentFailed {
			return "", application.ErrBookingStateConflict
		}
		next = current
	case domain.BookingStatusExpired:
		if result.Outcome == application.PaymentSucceeded {
			lateSuccess = true
		}
		next = current
	default:
		return "", fmt.Errorf("unknown booking status %q", current)
	}

	if next != current {
		_, err = tx.Exec(ctx, `
			UPDATE bookings SET status = $2, updated_at = now() WHERE id = $1
		`, result.BookingID, next)
		if err != nil {
			return "", fmt.Errorf("update booking %d status: %w", result.BookingID, err)
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO payment_events(event_id, booking_id, outcome)
		VALUES ($1, $2, $3)
	`, result.EventID, result.BookingID, result.Outcome)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", application.ErrPaymentEventConflict
		}
		return "", fmt.Errorf("record payment event %q: %w", result.EventID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit payment result for booking %d: %w", result.BookingID, err)
	}
	if lateSuccess {
		return next, application.ErrBookingExpired
	}
	return next, nil
}

type heldNight struct {
	categoryID string
	stayDate   string
	roomCount  int
}

// releaseBookingNights updates category-night rows in a fixed order. Booking
// creation uses the same order, which avoids lock-order deadlocks.
func releaseBookingNights(ctx context.Context, tx pgx.Tx, bookingID int64) error {
	rows, err := tx.Query(ctx, `
		SELECT category_id::text, stay_date::text, room_count
		FROM booking_nights
		WHERE booking_id = $1
		ORDER BY category_id, stay_date
	`, bookingID)
	if err != nil {
		return fmt.Errorf("read held nights for booking %d: %w", bookingID, err)
	}
	var nights []heldNight
	for rows.Next() {
		var night heldNight
		if err := rows.Scan(&night.categoryID, &night.stayDate, &night.roomCount); err != nil {
			rows.Close()
			return fmt.Errorf("scan held night for booking %d: %w", bookingID, err)
		}
		nights = append(nights, night)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read held nights for booking %d: %w", bookingID, err)
	}
	rows.Close()

	for _, night := range nights {
		tag, err := tx.Exec(ctx, `
			UPDATE category_night_inventory
			SET reserved_rooms = reserved_rooms - $3
			WHERE category_id = $1::uuid AND stay_date = $2::date
			  AND reserved_rooms >= $3
		`, night.categoryID, night.stayDate, night.roomCount)
		if err != nil {
			return fmt.Errorf("release category %s on %s: %w", night.categoryID, night.stayDate, err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("inventory count inconsistent for category %s on %s", night.categoryID, night.stayDate)
		}
	}
	return nil
}

// ExpirePending claims one expired booking at a time with SKIP LOCKED so
// multiple workers can run without processing the same hold simultaneously.
func (r *BookingRepository) ExpirePending(ctx context.Context, limit int) (int, error) {
	expired := 0
	for expired < limit {
		found, err := r.expireOnePending(ctx)
		if err != nil {
			return expired, err
		}
		if !found {
			break
		}
		expired++
	}
	return expired, nil
}

func (r *BookingRepository) expireOnePending(ctx context.Context) (bool, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, fmt.Errorf("begin expiry transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var bookingID int64
	err = tx.QueryRow(ctx, `
		SELECT id
		FROM bookings
		WHERE status = 'PENDING_PAYMENT' AND expires_at <= now()
		ORDER BY expires_at, id
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`).Scan(&bookingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim expired booking: %w", err)
	}
	if err := releaseBookingNights(ctx, tx, bookingID); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE bookings SET status = 'EXPIRED', updated_at = now() WHERE id = $1
	`, bookingID)
	if err != nil {
		return false, fmt.Errorf("mark booking %d expired: %w", bookingID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit expiry for booking %d: %w", bookingID, err)
	}
	return true, nil
}
