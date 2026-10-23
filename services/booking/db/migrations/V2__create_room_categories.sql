create table room_categories(
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hotel_id UUID NOT NULL,
    name TEXT NOT NULL,
    max_guests integer NOT NULL CHECK (max_guests > 0),
    nightly_price_minor BIGINT not null CHECK ( nightly_price_minor >= 0 ),

    CONSTRAINT fk_hotels FOREIGN KEY (hotel_id) REFERENCES hotels(id),
    CONSTRAINT unique_hotel_name UNIQUE (hotel_id, name),
    CONSTRAINT unique_hotel_category_id UNIQUE (hotel_id, id)
);