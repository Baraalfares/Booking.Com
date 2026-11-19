package application

import (
	"context"
	"testing"
	"time"

	"example.com/bookingcom/internal/domain"
)

type availabilityTestHotelRepository struct{}

func (availabilityTestHotelRepository) GetByID(_ context.Context, id string) (domain.Hotel, bool, error) {
	return domain.Hotel{ID: id, Currency: "USD"}, true, nil
}

type availabilityTestRepository struct {
	categories []CategoryNightAvailability
}

func (r availabilityTestRepository) ListByHotelAndStay(_ context.Context, _ string, _, _ time.Time) ([]CategoryNightAvailability, error) {
	return r.categories, nil
}

func TestGetHotelAvailabilityKeepsSoldOutCategoriesAndChecksGuests(t *testing.T) {
	checkIn := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	useCase := GetHotelAvailability{
		Hotels: availabilityTestHotelRepository{},
		Availability: availabilityTestRepository{categories: []CategoryNightAvailability{
			{CategoryID: "sold-out", MaxGuests: 4, NightlyPriceMinor: 100, RemainingRooms: 0},
			{CategoryID: "too-small", MaxGuests: 2, NightlyPriceMinor: 100, RemainingRooms: 2},
			{CategoryID: "available", MaxGuests: 4, NightlyPriceMinor: 100, RemainingRooms: 2},
		}},
	}
	result, err := useCase.Execute(context.Background(), GetHotelAvailabilityRequest{
		HotelID: "hotel", CheckIn: checkIn, CheckOut: checkIn.AddDate(0, 0, 2),
		GuestCount: 5, RoomCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Categories) != 3 {
		t.Fatalf("got %d categories, want 3 including sold-out", len(result.Categories))
	}
	if result.Categories[0].CanBook || result.Categories[1].CanBook || !result.Categories[2].CanBook {
		t.Fatalf("unexpected bookability: %+v", result.Categories)
	}
	if result.Categories[2].LineTotalMinor != 400 {
		t.Fatalf("line total = %d, want 400", result.Categories[2].LineTotalMinor)
	}
}
