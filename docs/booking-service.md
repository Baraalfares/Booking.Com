# Booking service guide

This guide describes the Booking microservice as it exists in this repository. It is written for someone coming from Java: Go has **structs, methods, and interfaces**, but no Java-style classes or inheritance. Here, a "component" is usually a package or a struct with dependencies passed into it.

## What the service does

Booking sells **room categories**, not specific room numbers. A request belongs to one hotel and can contain several categories. Each requested category is reserved for every night from `checkIn` (inclusive) to `checkOut` (exclusive). The service saves the price the guest accepted, holds inventory for 15 minutes, and later confirms or releases the hold based on a payment result.

Specific room assignment is a later feature. The Auth and Payment microservices are also future integrations. For local development, the booking request supplies `guestId`; it is **not authenticated** by Booking.

## Components and request flow

```text
HTTP client
    │
    ▼
internal/api                 Parse JSON, dates, headers; map errors to HTTP
    │
    ▼
internal/application         Validate commands and apply use-case rules
    │  repository interfaces
    ▼
internal/postgres            SQL queries, transactions, locks, persistence
    │
    ▼
PostgreSQL                   Data constraints and capacity triggers

internal/domain              Shared business data types
internal/config              Environment-based configuration
cmd/server                   Dependency wiring, HTTP server, expiry worker
```

The dependencies point inward: application code names the repository operations it needs, and the PostgreSQL package implements them. The application package does not import SQL or HTTP. [`main.go`](../services/booking/cmd/server/main.go) creates the concrete repositories and passes them to the use cases.

| Package | Main types and functions | Responsibility |
| --- | --- | --- |
| [`domain`](../services/booking/internal/domain) | `Hotel`, `RoomCategory`, `Room`, `Booking`, `BookingItem`, status types | Business data. `Room.Number` is unique within a hotel; the room's database ID is a UUID. Prices and totals are integer **minor currency units** (for example, cents). |
| [`application`](../services/booking/internal/application) | `ListHotelCategories`, `GetHotelAvailability`, `CreateBooking`, `ProcessPaymentResult`, `ExpirePendingBookings` | One use case per operation. `Execute` methods validate input and call repository interfaces. |
| [`postgres`](../services/booking/internal/postgres) | `HotelRepository`, `RoomCategoryRepository`, `AvailabilityRepository`, `BookingRepository` | SQL implementations. `BookingRepository` owns the atomic booking and payment transactions. |
| [`api`](../services/booking/internal/api) | `RegisterCategoryRoutes`, `RegisterAvailabilityRoutes`, `RegisterBookingRoutes`, `RegisterPaymentRoutes` | Route registration, request/response types, HTTP validation and status codes. |
| [`config`](../services/booking/internal/config/config.go) | `Config`, `Load` | Reads `DATABASE_URL`, `PAYMENT_WEBHOOK_SECRET`, and optional `HTTP_ADDR`. |

For a Java analogy, a domain struct resembles an entity or model class; a use-case struct resembles a service; an application repository interface resembles a repository contract; and an API registration function creates a controller route. Go interfaces are satisfied implicitly: there is no `implements` keyword.

### Domain and database model

| Data | Relationship or rule |
| --- | --- |
| `hotels` | A hotel has an ID, name, address, and currency. |
| `room_categories` | Belongs to one hotel; has a name, maximum guests **per room**, and nightly price. Categories belong to their hotel, rather than being global types. |
| `rooms` | Belongs to a hotel and one of its categories; has a hotel-unique number and a default `active` or `out_of_service` status. |
| `room_status_overrides` | Changes a room's status for a date range. Its end date is exclusive; overlapping ranges for one room are rejected. |
| `category_night_inventory` | One row per category and stay date when needed, with `reserved_rooms`. Pending and confirmed bookings both count. |
| `bookings` | One hotel and guest, stay dates, status, currency, total, expiry time, and a guest-scoped idempotency key. |
| `booking_items` | One row for each category in a booking, including room/guest counts and **saved** nightly price and line total. |
| `booking_nights` | Ledger of the category nights held by each booking; used to release exactly those nights. |
| `payment_events` | Unique event IDs make payment-result replay safe. |

The schema is created in order by [Flyway migrations V1–V8](../services/booking/db/migrations). The Go structs are not an ORM mapping: repositories explicitly select and insert the fields they need.

### Operations

| HTTP route | Use case | Result |
| --- | --- | --- |
| `GET /hotels/{hotelID}/categories` | `ListHotelCategories` | All offerings and current nightly prices; 404 for a missing hotel. |
| `GET /hotels/{hotelID}/availability?check_in=YYYY-MM-DD&check_out=YYYY-MM-DD&guests=2&rooms=1` | `GetHotelAvailability` | Every category, including sold-out ones, its minimum remaining rooms over the stay, and whether the request fits guest capacity. |
| `POST /hotels/{hotelID}/bookings` | `CreateBooking` | One pending booking containing one or more categories, with held nights and a 15-minute expiry. Requires `Idempotency-Key`. |
| `POST /internal/payment-results` | `ProcessPaymentResult` | Applies a signed `SUCCEEDED` or `FAILED` result. |
| Background worker | `ExpirePendingBookings` | Releases expired pending holds in batches. |
| `GET /health/live`, `GET /health/ready` | Server health handlers | Readiness checks PostgreSQL. |

See the [service README](../services/booking/README.md) for the JSON contract and environment setup.

## How booking prevents race conditions

The availability endpoint is a **read-only preview**. Two guests may both see one room available. The booking transaction is the final authority; it must recheck capacity after acquiring locks.

[`BookingRepository.Create`](../services/booking/internal/postgres/create_booking_repository.go) does this in one PostgreSQL `READ COMMITTED` transaction:

1. Look up the hotel and insert the booking under `UNIQUE (guest_id, idempotency_key)`. An identical retry returns the saved booking; reusing the key for different input returns a conflict. UUIDs and item ordering are normalized before hashing.
2. Read the requested categories with `FOR SHARE`, check guest capacity, and snapshot the nightly prices. Category IDs are processed in sorted order.
3. Build every `(category_id, stay_date)` allocation and sort by category and date. Insert the inventory row if missing, then lock that row with `SELECT ... FOR UPDATE`.
4. While holding the row lock, count rooms active on that date, including dated overrides. If `requested > active - reserved`, return insufficient availability. Otherwise increment `reserved_rooms`.
5. Insert the booking items and night ledger, set the total and expiry, then commit. Any failure before commit rolls back **all** nights and categories.

For example, if only one Standard room remains, guest A and guest B can both read `remainingRooms: 1`. A locks the Standard inventory row first and reserves it. B waits for that lock; after A commits, B reads the updated `reserved_rooms` and receives HTTP `409 Conflict`. There is no double sale. If a request reserves Monday and Tuesday but Wednesday has no capacity, the whole transaction rolls back: Monday and Tuesday are released automatically.

The fixed lock order reduces deadlocks between multi-category bookings. PostgreSQL can still detect a deadlock when a bulk operational update takes locks in another order. Booking retries deadlock or serialization failures up to three times, then responds with HTTP `503` and `Retry-After: 1`. A future admin writer should lock affected categories in a stable order and retry its own deadlocks.

**Room status changes:** [V7 and V8](../services/booking/db/migrations) add database triggers for room and dated-override changes. They lock affected category rows, inspect reserved **current and future** nights, and reject an edit that would leave fewer active rooms than reservations. Operational capacity writes must use `READ COMMITTED`; the trigger rejects higher isolation levels that could see an old snapshot. Historical stays do not block retiring a room today.

**Payment versus expiry:** Both the payment callback and expiry worker lock the `bookings` row before changing its status or releasing nights. Payment checks the wall clock *after* acquiring that lock. The worker uses `FOR UPDATE SKIP LOCKED`, so concurrent workers do not claim the same hold. A failed or expired booking releases its ledger nights once, inside the status transaction. A successful payment keeps the inventory reserved. A success arriving after expiry is rejected, and its event ID is still recorded.

This protects **category-night counts**. It does not yet guarantee that one physical room can cover every night of a multi-night stay; the later room-assignment design must account for that.

## Run and test

All commands below start at the repository root unless a `cd` is shown. Put the local database password, Flyway JDBC URL, Booking database URL, and payment secret in `.env` (it is Git-ignored):

```bash
cp -n .env.example .env
openssl rand -hex 32
```

Paste the generated value after `PAYMENT_WEBHOOK_SECRET=` in `.env`, and fill the database fields with your local values. Keep the database name, user, and password consistent across the PostgreSQL, Flyway, and Booking settings. Then start the stack:

```bash
docker compose up -d --build booking-api
curl -i http://127.0.0.1:8080/health/ready
```

The expected readiness status is **204 No Content**. Compose starts PostgreSQL and applies Flyway migrations before the API. To inspect migration state:

```bash
docker compose exec -T booking-db psql -U booking -d booking -c 'SELECT version, success FROM flyway_schema_history ORDER BY installed_rank DESC LIMIT 1'
```

### Automated tests

From the Go module directory:

```bash
cd services/booking
go test ./...
```

This runs unit tests. Database integration tests are **skipped** unless their environment variables are set. For the booking, lifecycle, and capacity integration tests, use a disposable database with V1–V8 applied. One way to create it with the local Compose stack is:

```bash
docker compose up -d booking-db
docker compose exec -T booking-db psql -U booking -d booking -c 'CREATE DATABASE booking_test'
docker compose run --rm -e FLYWAY_URL="$BOOKING_TEST_FLYWAY_URL" booking-migrate
cd services/booking
BOOKING_TEST_DATABASE_URL="$BOOKING_TEST_DATABASE_URL" go test ./... -count=1
```

Before running these commands, export `BOOKING_TEST_FLYWAY_URL` as the JDBC URL for the disposable `booking_test` database and `BOOKING_TEST_DATABASE_URL` as its PostgreSQL URL. Keep both values in your local shell or ignored `.env`, never in Git. Run the first three commands from the repository root. Create `booking_test` only once; if it already exists, skip that command. The integration suite covers multi-category rollback, two concurrent guests competing for one room, idempotent replay, price snapshots, payment/expiry races, and capacity-reducing room edits. The separate availability database checks require `DATABASE_URL` and a database containing at least one hotel with categories and rooms; see the [seed README](../services/booking/db/seeds/README.md). Set both `DATABASE_URL` and `BOOKING_TEST_DATABASE_URL` to run every database check.

### Manual HTTP check

Find a hotel and its categories in your local seeded database:

```bash
docker compose exec -T booking-db psql -U booking -d booking -c 'SELECT h.id, h.name, c.id AS category_id, c.name AS category FROM hotels h JOIN room_categories c ON c.hotel_id = h.id LIMIT 5'
```

Replace the placeholders below with IDs from that query and future dates. On macOS, `date -u -v+7d +%F` gives a date seven days ahead.

```bash
curl -i 'http://127.0.0.1:8080/hotels/<hotel-uuid>/categories'
curl -i 'http://127.0.0.1:8080/hotels/<hotel-uuid>/availability?check_in=<YYYY-MM-DD>&check_out=<later-YYYY-MM-DD>&guests=2&rooms=1'
curl -i -X POST 'http://127.0.0.1:8080/hotels/<hotel-uuid>/bookings' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: manual-booking-1' \
  --data '{"guestId":"manual-guest","checkIn":"<YYYY-MM-DD>","checkOut":"<later-YYYY-MM-DD>","items":[{"categoryId":"<category-uuid>","roomCount":1,"guestCount":2}]}'
```

Save the returned booking ID. Repeating the **same** POST with the same key returns that booking and does not reserve another room. To simulate a Payment-service failure and release the hold, export the same secret used in `.env`, then sign the exact JSON bytes:

```bash
export PAYMENT_WEBHOOK_SECRET='<value from .env>'
BODY='{"eventId":"manual-failure-1","bookingId":<booking-id>,"outcome":"FAILED"}'
SIGNATURE=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$PAYMENT_WEBHOOK_SECRET" -binary | xxd -p -c 256)
curl -i -X POST 'http://127.0.0.1:8080/internal/payment-results' \
  -H 'Content-Type: application/json' \
  -H "X-Payment-Signature: sha256=$SIGNATURE" \
  --data "$BODY"
```

The callback should return status `FAILED`. Check availability again to see the released room. Change both the idempotency key and payment event ID for another manual scenario.

## Current boundaries

- The booking API accepts a caller-supplied guest ID for local development. A trusted Auth service or gateway must provide the identity before public exposure.
- The payment endpoint verifies HMAC signatures, but a separate Payment service is not built yet.
- There is no admin API for hotel, category, room, or dated status management. The schema and capacity safeguards exist.
- The service reserves category capacity; it does not assign specific room numbers.
