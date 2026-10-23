# Booking load data

This seed combines **real hotel place records** with **generated booking inventory**.

- Hotel IDs, names, and address fields come from [Overture Maps Places](https://docs.overturemaps.org/guides/places/). The importer selects named hotels with a street address or locality and excludes records marked permanently closed. A source record can still be inaccurate or out of date.
- Each Overture place ID becomes the `hotels.id` UUID. USD is a generated test pricing currency for every imported hotel; it does not claim to be the hotel's real charging currency.
- Room categories, guest limits, nightly rates, room numbers, and room status are generated for load testing. They are **not** claims about those hotels' actual inventory or prices.
- Each imported hotel receives 2–5 categories and 40–160 rooms, determined by its ID. Importing 50,000 hotels produces roughly five million room rows.
- The exporter takes the first matching records in Overture's file order. This sample is weighted toward Mexico and nearby countries, so it is useful for load testing but is not a geographically balanced hotel catalog.

The Overture Places theme includes data under CDLA Permissive 2.0, Apache 2.0, and CC0 licenses. See [Overture attribution and licensing](https://docs.overturemaps.org/attribution/) and the [current release catalog](https://docs.overturemaps.org/getting-data/cloud-sources/). The exporter uses the latest release unless `--release` is supplied; Overture retains only recent public releases.

## Load locally

Run these commands from the `Booking.Com` project root:

```bash
docker compose up -d booking-db
python3 -m venv /tmp/booking-data-venv
/tmp/booking-data-venv/bin/pip install -r services/booking/db/seeds/requirements.txt
/tmp/booking-data-venv/bin/python services/booking/db/seeds/export_overture_hotels.py --limit 50000
docker compose cp /tmp/overture_hotels.csv booking-db:/tmp/overture_hotels.csv
docker compose exec -T booking-db psql -X -v ON_ERROR_STOP=1 -U booking -d booking < services/booking/db/seeds/load_overture_hotels.sql
```

The CSV stays outside Git. The SQL loader runs in one transaction and uses deterministic IDs with `ON CONFLICT DO NOTHING`, so it can be rerun without duplicating hotels, categories, or rooms.

Verify the imported row counts:

```bash
docker compose exec -T booking-db psql -X -U booking -d booking -c "SELECT (SELECT count(*) FROM hotels) AS hotels, (SELECT count(*) FROM room_categories) AS categories, (SELECT count(*) FROM rooms) AS rooms;"
```

Using Overture release `2026-10-21.0`, the 50,000-hotel import produced 174,612 room categories and 4,857,877 rooms. A different release or file order may produce different inventory counts because the generated room count depends on each hotel's source ID.
