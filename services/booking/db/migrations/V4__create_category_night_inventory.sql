-- One row per sellable category and night is created when a booking needs it.
-- Pending and confirmed bookings both count toward reserved_rooms.
CREATE TABLE category_night_inventory (
    category_id UUID NOT NULL REFERENCES room_categories(id),
    stay_date DATE NOT NULL,
    reserved_rooms INTEGER NOT NULL DEFAULT 0 CHECK (reserved_rooms >= 0),
    PRIMARY KEY (category_id, stay_date)
);

-- A dated override lets a currently unavailable room become active on future dates.
-- The end date is exclusive. Overlapping overrides for one room are ambiguous.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE room_status_overrides (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room_id UUID NOT NULL REFERENCES rooms(id),
    starts_on DATE NOT NULL,
    ends_on DATE NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'out_of_service')),
    CHECK (starts_on < ends_on),
    EXCLUDE USING gist (
        room_id WITH =,
        daterange(starts_on, ends_on, '[)') WITH &&
    )
);

-- Availability scans the small set of rooms in a requested category.
CREATE INDEX idx_rooms_category_id ON rooms (category_id);
