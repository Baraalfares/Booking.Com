package application

import (
	"context"

	"example.com/bookingcom/internal/booking/domain"
)

type HotelRepository interface {
	GetByID(ctx context.Context, id string) (domain.Hotel, bool, error)
}

type RoomCategoryRepository interface {
	ListByHotelID(ctx context.Context, hotelID string) ([]domain.RoomCategory, error)
}
