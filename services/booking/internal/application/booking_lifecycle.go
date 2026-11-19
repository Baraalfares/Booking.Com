package application

import (
	"context"
	"errors"
	"strings"

	"example.com/bookingcom/internal/domain"
)

var (
	ErrBookingNotFound      = errors.New("booking not found")
	ErrBookingExpired       = errors.New("booking expired before payment confirmation")
	ErrBookingStateConflict = errors.New("booking status conflicts with payment result")
	ErrPaymentEventConflict = errors.New("payment event ID was already used for a different result")
	ErrInvalidPaymentResult = errors.New("invalid payment result")
)

type PaymentOutcome string

const (
	PaymentSucceeded PaymentOutcome = "SUCCEEDED"
	PaymentFailed    PaymentOutcome = "FAILED"
)

type PaymentResult struct {
	EventID   string
	BookingID int64
	Outcome   PaymentOutcome
}

type PaymentResultRepository interface {
	ApplyPaymentResult(ctx context.Context, result PaymentResult) (domain.BookingStatus, error)
}

type ExpiringBookingRepository interface {
	ExpirePending(ctx context.Context, limit int) (int, error)
}

type ProcessPaymentResult struct {
	Repository PaymentResultRepository
}

func (uc ProcessPaymentResult) Execute(ctx context.Context, result PaymentResult) (domain.BookingStatus, error) {
	if strings.TrimSpace(result.EventID) == "" || len(result.EventID) > 128 || result.BookingID <= 0 {
		return "", ErrInvalidPaymentResult
	}
	if result.Outcome != PaymentSucceeded && result.Outcome != PaymentFailed {
		return "", ErrInvalidPaymentResult
	}
	return uc.Repository.ApplyPaymentResult(ctx, result)
}

type ExpirePendingBookings struct {
	Repository ExpiringBookingRepository
}

func (uc ExpirePendingBookings) Execute(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("expiry batch limit must be between 1 and 1000")
	}
	return uc.Repository.ExpirePending(ctx, limit)
}
