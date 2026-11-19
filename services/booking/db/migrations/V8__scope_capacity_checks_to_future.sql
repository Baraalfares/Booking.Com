-- Historical sold-out nights remain in the booking ledger. They should not
-- block an operational room change made after the stay has ended.
-- Capacity writers must use READ COMMITTED so the validation sees inventory
-- rows inserted by bookings that committed while the writer waited for locks.
CREATE OR REPLACE FUNCTION assert_reserved_capacity(affected_category_ids UUID[])
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    night RECORD;
    active_rooms INTEGER;
    today_utc DATE := (clock_timestamp() AT TIME ZONE 'UTC')::date;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'room capacity changes require READ COMMITTED isolation'
            USING ERRCODE = '25000';
    END IF;

    PERFORM 1
    FROM room_categories
    WHERE id = ANY(affected_category_ids)
    ORDER BY id
    FOR UPDATE;

    FOR night IN
        SELECT category_id, stay_date, reserved_rooms
        FROM category_night_inventory
        WHERE category_id = ANY(affected_category_ids)
          AND stay_date >= today_utc
          AND reserved_rooms > 0
        ORDER BY category_id, stay_date
        FOR UPDATE
    LOOP
        SELECT COUNT(*)::integer INTO active_rooms
        FROM rooms AS r
        LEFT JOIN room_status_overrides AS o
          ON o.room_id = r.id
         AND o.starts_on <= night.stay_date
         AND night.stay_date < o.ends_on
        WHERE r.category_id = night.category_id
          AND COALESCE(o.status, r.status) = 'active';

        IF active_rooms < night.reserved_rooms THEN
            RAISE EXCEPTION
                'room status change would overbook category % on %',
                night.category_id, night.stay_date
                USING ERRCODE = '23514';
        END IF;
    END LOOP;
END;
$$;
