package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	MaxStayNights      = 30
	MaxRequestedRooms  = 20
	MaxRequestedGuests = 100
)

var ErrInvalidAvailabilityRequest = errors.New("invalid availability request")

// CategoryNightAvailability is the smallest remaining room count across a stay.
// A zero count is returned for sold-out categories so guests can still see them.
type CategoryNightAvailability struct {
	CategoryID        string
	Name              string
	MaxGuests         int
	NightlyPriceMinor int64
	RemainingRooms    int
}

type AvailabilityRepository interface {
	ListByHotelAndStay(ctx context.Context, hotelID string, checkIn, checkOut time.Time) ([]CategoryNightAvailability, error)
}

type GetHotelAvailability struct {
	Hotels       HotelRepository
	Availability AvailabilityRepository
}

type GetHotelAvailabilityRequest struct {
	HotelID    string
	CheckIn    time.Time
	CheckOut   time.Time
	GuestCount int
	RoomCount  int
}

type AvailableCategory struct {
	CategoryNightAvailability
	CanBook        bool
	LineTotalMinor int64
}

type GetHotelAvailabilityResult struct {
	Currency   string
	Categories []AvailableCategory
}

func (uc GetHotelAvailability) Execute(ctx context.Context, req GetHotelAvailabilityRequest) (GetHotelAvailabilityResult, error) {
	if err := ValidateAvailabilityRequest(req); err != nil {
		return GetHotelAvailabilityResult{}, err
	}

	hotel, found, err := uc.Hotels.GetByID(ctx, req.HotelID)
	if err != nil {
		return GetHotelAvailabilityResult{}, err
	}
	if !found {
		return GetHotelAvailabilityResult{}, ErrHotelNotFound
	}

	categories, err := uc.Availability.ListByHotelAndStay(ctx, hotel.ID, req.CheckIn, req.CheckOut)
	if err != nil {
		return GetHotelAvailabilityResult{}, err
	}
	result := GetHotelAvailabilityResult{
		Currency:   hotel.Currency,
		Categories: make([]AvailableCategory, 0, len(categories)),
	}
	for _, category := range categories {
		quantityNights := int64(req.RoomCount) * int64(req.CheckOut.Sub(req.CheckIn)/(24*time.Hour))
		if category.NightlyPriceMinor < 0 || category.NightlyPriceMinor > math.MaxInt64/quantityNights {
			return GetHotelAvailabilityResult{}, fmt.Errorf("invalid nightly price for category %s", category.CategoryID)
		}
		result.Categories = append(result.Categories, AvailableCategory{
			CategoryNightAvailability: category,
			CanBook: category.RemainingRooms >= req.RoomCount &&
				req.GuestCount <= category.MaxGuests*req.RoomCount,
			LineTotalMinor: category.NightlyPriceMinor * quantityNights,
		})
	}
	return result, nil
}

func ValidateAvailabilityRequest(req GetHotelAvailabilityRequest) error {
	if req.CheckIn.IsZero() || req.CheckOut.IsZero() {
		return fmt.Errorf("%w: check-in and check-out are required", ErrInvalidAvailabilityRequest)
	}
	if req.CheckIn.Before(time.Now().UTC().Truncate(24 * time.Hour)) {
		return fmt.Errorf("%w: check-in must not be in the past", ErrInvalidAvailabilityRequest)
	}
	if !req.CheckOut.After(req.CheckIn) {
		return fmt.Errorf("%w: check-out must be after check-in", ErrInvalidAvailabilityRequest)
	}
	if req.CheckOut.Sub(req.CheckIn) > MaxStayNights*24*time.Hour {
		return fmt.Errorf("%w: stay cannot exceed %d nights", ErrInvalidAvailabilityRequest, MaxStayNights)
	}
	if req.RoomCount < 1 || req.RoomCount > MaxRequestedRooms {
		return fmt.Errorf("%w: rooms must be between 1 and %d", ErrInvalidAvailabilityRequest, MaxRequestedRooms)
	}
	if req.GuestCount < 1 || req.GuestCount > MaxRequestedGuests {
		return fmt.Errorf("%w: guests must be between 1 and %d", ErrInvalidAvailabilityRequest, MaxRequestedGuests)
	}
	return nil
}
