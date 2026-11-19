package postgres

import (
	"context"
	"fmt"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RoomCategoryRepository struct {
	pool *pgxpool.Pool
}

var _ application.RoomCategoryRepository = (*RoomCategoryRepository)(nil)

func NewRoomCategoryRepository(pool *pgxpool.Pool) *RoomCategoryRepository {
	return &RoomCategoryRepository{pool: pool}
}

func (r *RoomCategoryRepository) ListByHotelID(ctx context.Context, hotelID string) ([]domain.RoomCategory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, hotel_id::text, name, max_guests, nightly_price_minor
		FROM room_categories
		WHERE hotel_id = $1::uuid
		ORDER BY name, id
	`, hotelID)
	if err != nil {
		return nil, fmt.Errorf("list categories for hotel %s: %w", hotelID, err)
	}
	defer rows.Close()

	categories := make([]domain.RoomCategory, 0)
	for rows.Next() {
		var category domain.RoomCategory
		if err := rows.Scan(
			&category.ID,
			&category.HotelID,
			&category.Name,
			&category.MaxGuests,
			&category.NightlyPriceMinor,
		); err != nil {
			return nil, fmt.Errorf("scan category for hotel %s: %w", hotelID, err)
		}
		categories = append(categories, category)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read categories for hotel %s: %w", hotelID, err)
	}
	return categories, nil
}
