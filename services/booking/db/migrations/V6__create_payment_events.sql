-- A payment event ID is unique across all bookings. Replays with the same
-- booking and outcome are safe; reuse for another result is rejected.
CREATE TABLE payment_events (
    event_id TEXT PRIMARY KEY CHECK (event_id <> '' AND length(event_id) <= 128),
    booking_id BIGINT NOT NULL REFERENCES bookings(id),
    outcome TEXT NOT NULL CHECK (outcome IN ('SUCCEEDED', 'FAILED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX payment_events_booking_id_idx ON payment_events(booking_id);
