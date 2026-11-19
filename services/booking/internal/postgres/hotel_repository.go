package postgres

import (
	"context"
	"errors"
	"fmt"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HotelRepository struct {
	pool *pgxpool.Pool
}

var _ application.HotelRepository = (*HotelRepository)(nil)

func NewHotelRepository(pool *pgxpool.Pool) *HotelRepository {
	return &HotelRepository{pool: pool}
}

func (r *HotelRepository) GetByID(ctx context.Context, id string) (domain.Hotel, bool, error) {
	var hotel domain.Hotel
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, name, address, currency
		FROM hotels
		WHERE id = $1::uuid
	`, id).Scan(&hotel.ID, &hotel.Name, &hotel.Address, &hotel.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Hotel{}, false, nil
	}
	if err != nil {
		return domain.Hotel{}, false, fmt.Errorf("get hotel %s: %w", id, err)
	}
	return hotel, true, nil
}
