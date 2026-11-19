CREATE TABLE bookings (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    guest_id TEXT NOT NULL CHECK (guest_id <> ''),
    hotel_id UUID NOT NULL REFERENCES hotels(id),
    check_in DATE NOT NULL,
    check_out DATE NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('PENDING_PAYMENT', 'CONFIRMED', 'FAILED', 'EXPIRED')),
    total_minor BIGINT NOT NULL DEFAULT 0 CHECK (total_minor >= 0),
    currency VARCHAR(3) NOT NULL,
    idempotency_key TEXT NOT NULL CHECK (idempotency_key <> ''),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '15 minutes'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT valid_booking_stay CHECK (check_out > check_in),
    CONSTRAINT unique_guest_booking_request UNIQUE (guest_id, idempotency_key),
    CONSTRAINT unique_booking_hotel UNIQUE (id, hotel_id)
);

CREATE TABLE booking_items (
    booking_id BIGINT NOT NULL,
    hotel_id UUID NOT NULL,
    category_id UUID NOT NULL,
    room_count INTEGER NOT NULL CHECK (room_count > 0),
    guest_count INTEGER NOT NULL CHECK (guest_count > 0),
    nightly_price_minor BIGINT NOT NULL CHECK (nightly_price_minor >= 0),
    line_total_minor BIGINT NOT NULL CHECK (line_total_minor >= 0),
    PRIMARY KEY (booking_id, category_id),
    CONSTRAINT fk_booking_item_hotel FOREIGN KEY (booking_id, hotel_id)
        REFERENCES bookings(id, hotel_id),
    CONSTRAINT fk_booking_item_category FOREIGN KEY (hotel_id, category_id)
        REFERENCES room_categories(hotel_id, id)
);

-- These rows record exactly which category nights were held. Payment failure or
-- hold expiry can release inventory from this ledger in one transaction.
CREATE TABLE booking_nights (
    booking_id BIGINT NOT NULL,
    category_id UUID NOT NULL,
    stay_date DATE NOT NULL,
    room_count INTEGER NOT NULL CHECK (room_count > 0),
    PRIMARY KEY (booking_id, category_id, stay_date),
    CONSTRAINT fk_booking_night_item FOREIGN KEY (booking_id, category_id)
        REFERENCES booking_items(booking_id, category_id),
    CONSTRAINT fk_booking_night_inventory FOREIGN KEY (category_id, stay_date)
        REFERENCES category_night_inventory(category_id, stay_date)
);

CREATE INDEX bookings_pending_expiry_idx ON bookings(expires_at)
    WHERE status = 'PENDING_PAYMENT';
CREATE INDEX booking_nights_category_date_idx ON booking_nights(category_id, stay_date);
