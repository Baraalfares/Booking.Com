package postgres

import (
	"context"
	"fmt"
	"time"

	"example.com/bookingcom/internal/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type availabilityQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type AvailabilityRepository struct {
	db availabilityQuerier
}

func NewAvailabilityRepository(pool *pgxpool.Pool) *AvailabilityRepository {
	return &AvailabilityRepository{db: pool}
}

var _ application.AvailabilityRepository = (*AvailabilityRepository)(nil)

// ListByHotelAndStay returns each category, including sold-out ones. RemainingRooms
// is the minimum remaining capacity on any night of the requested stay.
func (r *AvailabilityRepository) ListByHotelAndStay(ctx context.Context, hotelID string, checkIn, checkOut time.Time) ([]application.CategoryNightAvailability, error) {
	const query = `
		WITH nights AS (
			SELECT ($2::date + day_series.day)::date AS stay_date
			FROM generate_series(0, $3::date - $2::date - 1) AS day_series(day)
		), nightly AS (
			SELECT
				c.id AS category_id,
				c.name,
				c.max_guests,
				c.nightly_price_minor,
				n.stay_date,
				COUNT(room.id) FILTER (
					WHERE COALESCE(status_override.status, room.status) = 'active'
				) AS capacity,
				COALESCE(inventory.reserved_rooms, 0) AS reserved_rooms
			FROM room_categories AS c
			CROSS JOIN nights AS n
			LEFT JOIN rooms AS room ON room.category_id = c.id
			LEFT JOIN room_status_overrides AS status_override
				ON status_override.room_id = room.id
				AND daterange(status_override.starts_on, status_override.ends_on, '[)') @> n.stay_date
			LEFT JOIN category_night_inventory AS inventory
				ON inventory.category_id = c.id AND inventory.stay_date = n.stay_date
			WHERE c.hotel_id = $1::uuid
			GROUP BY c.id, c.name, c.max_guests, c.nightly_price_minor,
				n.stay_date, inventory.reserved_rooms
		)
		SELECT
			category_id::text,
			name,
			max_guests,
			nightly_price_minor,
			GREATEST(0, MIN(capacity - reserved_rooms))::integer AS remaining_rooms
		FROM nightly
		GROUP BY category_id, name, max_guests, nightly_price_minor
		ORDER BY name, category_id
	`

	rows, err := r.db.Query(ctx, query, hotelID, checkIn, checkOut)
	if err != nil {
		return nil, fmt.Errorf("query hotel availability: %w", err)
	}
	defer rows.Close()

	categories := make([]application.CategoryNightAvailability, 0)
	for rows.Next() {
		var category application.CategoryNightAvailability
		if err := rows.Scan(
			&category.CategoryID,
			&category.Name,
			&category.MaxGuests,
			&category.NightlyPriceMinor,
			&category.RemainingRooms,
		); err != nil {
			return nil, fmt.Errorf("scan hotel availability: %w", err)
		}
		categories = append(categories, category)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read hotel availability: %w", err)
	}
	return categories, nil
}
