-- Run with psql after copying overture_hotels.csv into the database container.
-- Hotel identities, names, and addresses come from Overture Maps Places.
-- Categories, rates, room numbers, and room status are deterministic test data.

BEGIN;

CREATE TEMP TABLE imported_hotels (
    id UUID,
    name TEXT,
    address TEXT,
    currency VARCHAR(3)
) ON COMMIT DROP;

COPY imported_hotels (id, name, address, currency)
FROM '/tmp/overture_hotels.csv'
WITH (FORMAT csv, HEADER true);

INSERT INTO hotels (id, name, address, currency)
SELECT id, name, address, currency
FROM imported_hotels
ON CONFLICT (id) DO NOTHING;

CREATE TEMP TABLE imported_hotel_plan ON COMMIT DROP AS
SELECT
    id,
    2 + get_byte(decode(md5(id::text), 'hex'), 0) % 4 AS category_count,
    40 + get_byte(decode(md5(id::text), 'hex'), 1) % 121 AS room_count,
    7000 + (get_byte(decode(md5(id::text), 'hex'), 2) % 101) * 100 AS base_rate_minor
FROM imported_hotels;

INSERT INTO room_categories (
    id, hotel_id, name, max_guests, nightly_price_minor
)
SELECT
    md5('overture-category-' || p.id::text || '-' || c.category_no::text)::uuid,
    p.id,
    c.name,
    c.max_guests,
    p.base_rate_minor + c.premium_minor
FROM imported_hotel_plan AS p
CROSS JOIN (
    VALUES
        (1, 'Standard', 2, 0),
        (2, 'Deluxe', 2, 4000),
        (3, 'Family', 4, 7000),
        (4, 'Suite', 4, 12000),
        (5, 'Executive', 3, 9000)
) AS c(category_no, name, max_guests, premium_minor)
WHERE c.category_no <= p.category_count
ON CONFLICT (id) DO NOTHING;

INSERT INTO rooms (id, hotel_id, category_id, number, status)
SELECT
    md5('overture-room-' || p.id::text || '-' || r.n::text)::uuid,
    p.id,
    md5(
        'overture-category-' || p.id::text || '-' ||
        (((r.n - 1) % p.category_count) + 1)::text
    )::uuid,
    lpad((100 + r.n)::text, 3, '0'),
    CASE WHEN r.n % 47 = 0 THEN 'out_of_service' ELSE 'active' END
FROM imported_hotel_plan AS p
CROSS JOIN LATERAL generate_series(1, p.room_count) AS r(n)
ON CONFLICT (id) DO NOTHING;

COMMIT;

ANALYZE hotels;
ANALYZE room_categories;
ANALYZE rooms;
