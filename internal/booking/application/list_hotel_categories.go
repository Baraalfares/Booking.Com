package application

import (
	"context"
	"errors"

	"example.com/bookingcom/internal/booking/domain"
)

type ListHotelCategoriesResult struct {
	Currency   string
	Categories []domain.RoomCategory
}

var ErrHotelNotFound = errors.New("hotel not found")

type ListHotelCategories struct {
	Hotels     HotelRepository
	Categories RoomCategoryRepository
}

func (uc ListHotelCategories) Execute(ctx context.Context, hotelID string) (ListHotelCategoriesResult, error) {
	hotel, found, err := uc.Hotels.GetByID(ctx, hotelID)
	if err != nil {
		return ListHotelCategoriesResult{}, err
	}
	if !found {
		return ListHotelCategoriesResult{}, ErrHotelNotFound
	}
	categories, err := uc.Categories.ListByHotelID(ctx, hotel.ID)
	if err != nil {
		return ListHotelCategoriesResult{}, err
	}
	if categories == nil {
		categories = []domain.RoomCategory{}
	}
	return ListHotelCategoriesResult{Currency: hotel.Currency, Categories: categories}, nil
}
