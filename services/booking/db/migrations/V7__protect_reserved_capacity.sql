-- Booking creation holds room_categories FOR SHARE before reserving nights.
-- Capacity writers take the same category rows FOR UPDATE, then lock its
-- already-reserved nights. This prevents room status changes from committing
-- below inventory that a booking has promised to guests.
CREATE FUNCTION assert_reserved_capacity(affected_category_ids UUID[])
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    night RECORD;
    active_rooms INTEGER;
BEGIN
    PERFORM 1
    FROM room_categories
    WHERE id = ANY(affected_category_ids)
    ORDER BY id
    FOR UPDATE;

    FOR night IN
        SELECT category_id, stay_date, reserved_rooms
        FROM category_night_inventory
        WHERE category_id = ANY(affected_category_ids)
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

CREATE FUNCTION validate_room_capacity_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM assert_reserved_capacity(ARRAY[OLD.category_id]);
        RETURN OLD;
    END IF;
    IF NEW.status <> OLD.status OR NEW.category_id <> OLD.category_id THEN
        PERFORM assert_reserved_capacity(ARRAY[OLD.category_id, NEW.category_id]);
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER validate_room_capacity_after_change
AFTER UPDATE OF status, category_id OR DELETE ON rooms
FOR EACH ROW EXECUTE FUNCTION validate_room_capacity_change();

CREATE FUNCTION validate_override_capacity_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_room_ids UUID[];
    affected_category_ids UUID[];
BEGIN
    IF TG_OP = 'INSERT' THEN
        affected_room_ids := ARRAY[NEW.room_id];
    ELSIF TG_OP = 'DELETE' THEN
        affected_room_ids := ARRAY[OLD.room_id];
    ELSE
        affected_room_ids := ARRAY[OLD.room_id, NEW.room_id];
    END IF;

    SELECT ARRAY(
        SELECT DISTINCT category_id
        FROM (
            SELECT category_id
            FROM rooms
            WHERE id = ANY(affected_room_ids)
            ORDER BY id
            FOR SHARE
        ) AS affected_rooms
    ) INTO affected_category_ids;

    PERFORM assert_reserved_capacity(affected_category_ids);
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER validate_override_capacity_after_change
AFTER INSERT OR UPDATE OR DELETE ON room_status_overrides
FOR EACH ROW EXECUTE FUNCTION validate_override_capacity_change();
