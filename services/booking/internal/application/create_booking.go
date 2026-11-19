package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"example.com/bookingcom/internal/domain"
)

var (
	ErrInvalidBooking           = errors.New("invalid booking")
	ErrCategoryNotFound         = errors.New("room category not found in hotel")
	ErrGuestCapacityExceeded    = errors.New("room category guest capacity exceeded")
	ErrInsufficientAvailability = errors.New("insufficient availability")
	ErrIdempotencyConflict      = errors.New("idempotency key used for a different request")
	ErrBookingBusy              = errors.New("booking temporarily unavailable")
)

type CreateBookingItem struct {
	CategoryID string
	RoomCount  int
	GuestCount int
}

type CreateBookingCommand struct {
	GuestID        string
	HotelID        string
	CheckIn        time.Time
	CheckOut       time.Time
	Items          []CreateBookingItem
	IdempotencyKey string
}

// BookingCreator must reserve all category nights and persist the booking in
// the same database transaction.
type BookingCreator interface {
	Create(ctx context.Context, command CreateBookingCommand) (domain.Booking, error)
}

type CreateBooking struct {
	Bookings BookingCreator
}

func (uc CreateBooking) Execute(ctx context.Context, command CreateBookingCommand) (domain.Booking, error) {
	command.GuestID = strings.TrimSpace(command.GuestID)
	command.HotelID = strings.ToLower(strings.TrimSpace(command.HotelID))
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if command.GuestID == "" || len(command.GuestID) > 128 || command.HotelID == "" ||
		command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 {
		return domain.Booking{}, fmt.Errorf("%w: guest ID, hotel ID, and idempotency key are required", ErrInvalidBooking)
	}

	command.CheckIn = utcDate(command.CheckIn)
	command.CheckOut = utcDate(command.CheckOut)
	if command.CheckIn.IsZero() || command.CheckOut.IsZero() ||
		!command.CheckOut.After(command.CheckIn) ||
		command.CheckOut.After(command.CheckIn.AddDate(0, 0, 30)) {
		return domain.Booking{}, fmt.Errorf("%w: stay must last 1 to 30 nights", ErrInvalidBooking)
	}
	if len(command.Items) == 0 || len(command.Items) > 20 {
		return domain.Booking{}, fmt.Errorf("%w: provide 1 to 20 distinct room categories", ErrInvalidBooking)
	}

	seen := make(map[string]struct{}, len(command.Items))
	totalRooms, totalGuests := 0, 0
	for i := range command.Items {
		item := &command.Items[i]
		item.CategoryID = strings.ToLower(strings.TrimSpace(item.CategoryID))
		if item.CategoryID == "" || item.RoomCount < 1 || item.RoomCount > 20 ||
			item.GuestCount < 1 || item.GuestCount > 100 {
			return domain.Booking{}, fmt.Errorf("%w: each item needs a category and valid room and guest counts", ErrInvalidBooking)
		}
		if _, duplicate := seen[item.CategoryID]; duplicate {
			return domain.Booking{}, fmt.Errorf("%w: duplicate room category", ErrInvalidBooking)
		}
		seen[item.CategoryID] = struct{}{}
		totalRooms += item.RoomCount
		totalGuests += item.GuestCount
	}
	if totalRooms > 20 || totalGuests > 100 {
		return domain.Booking{}, fmt.Errorf("%w: one booking may request at most 20 rooms and 100 guests", ErrInvalidBooking)
	}
	return uc.Bookings.Create(ctx, command)
}

func utcDate(t time.Time) time.Time {
	year, month, day := t.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
