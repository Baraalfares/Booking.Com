# Booking service

This Go service lists hotel room offerings, reports category availability for a stay, and creates a pending booking for one hotel and one or more room categories. A booking reserves every requested category night in one PostgreSQL transaction. The room number is assigned later. Failed or expired payment holds release the reserved category nights.

For the component map, Go types, testing walkthrough, and concurrency design, see the [Booking service guide](../../docs/booking-service.md).

## Run locally

From the repository root, create a local `.env` file from `.env.example`. Set a local PostgreSQL password, the Flyway JDBC URL for the Compose database, the Booking API database URL for the Compose database, and a payment secret generated with `openssl rand -hex 32`. The `.env` file is ignored by Git; `.env.example` contains only empty fields. Use the same database name, user, and password in the PostgreSQL, Flyway, and Booking settings.

```bash
docker compose up -d --build booking-api
curl -i http://127.0.0.1:8080/health/ready
```

Compose starts PostgreSQL, runs Flyway migrations, then starts Booking. The API is published only on `127.0.0.1:8080`. The liveness endpoint is `GET /health/live`; readiness is `GET /health/ready` and checks PostgreSQL.

To run Go on your Mac instead, start the database and migrations from the repository root, then run the service from its module directory:

```bash
docker compose up -d booking-db
docker compose run --rm booking-migrate
cd services/booking
export DATABASE_URL='<your local PostgreSQL URL>'
export PAYMENT_WEBHOOK_SECRET='<paste a locally generated secret of at least 32 bytes>'
go run ./cmd/server
```

`HTTP_ADDR` is optional for a host run and defaults to `127.0.0.1:8080`. Compose sets it to `:8080` inside the container while publishing the port only to the host loopback interface. Startup fails if PostgreSQL is unreachable or the payment webhook secret is too short. SIGINT/SIGTERM triggers a graceful HTTP shutdown; after the shutdown deadline, active request contexts are cancelled. A background worker checks for expired pending bookings every 30 seconds.

## HTTP contract

| Route | Purpose |
| --- | --- |
| `GET /hotels/{hotelID}/categories` | List offerings and nightly prices; missing hotel returns 404. |
| `GET /hotels/{hotelID}/availability?check_in=YYYY-MM-DD&check_out=YYYY-MM-DD&guests=2&rooms=1` | List every category, including sold-out categories, with the minimum remaining room count across the stay. |
| `POST /hotels/{hotelID}/bookings` | Create a pending booking and hold inventory for 15 minutes. Requires `Idempotency-Key`. |
| `POST /internal/payment-results` | Apply a signed Payment-service result. Requires `X-Payment-Signature`. |
| `GET /health/live`, `GET /health/ready` | Process and database health. |

Booking creation accepts JSON like:

```json
{
  "guestId": "guest-123",
  "checkIn": "<future YYYY-MM-DD>",
  "checkOut": "<later YYYY-MM-DD>",
  "items": [
    {"categoryId": "<category UUID>", "roomCount": 1, "guestCount": 2},
    {"categoryId": "<another category UUID in the same hotel>", "roomCount": 1, "guestCount": 2}
  ]
}
```

Replace the placeholder IDs and dates with a hotel's real category IDs and upcoming dates. Send the JSON with `Content-Type: application/json` and an `Idempotency-Key` header. All items must belong to the hotel in the path. The total and each item's nightly price are stored on the booking, so later price changes do not alter what the guest accepted. The 15-minute payment hold starts when the booking is finalized in the database. Stay length is 1–30 nights, with at most 20 rooms and 100 guests in one request. Availability is advisory; the create transaction rechecks capacity while locking the category nights. A shortage rolls the entire request back. Retrying the same idempotency key and request returns the original booking, including after its check-in date has passed.

Payment results use JSON `{"eventId":"unique-event-id","bookingId":123,"outcome":"SUCCEEDED"}` or outcome `FAILED`. The signature header is `sha256=` followed by the lowercase hex HMAC-SHA256 of the exact request body, using `PAYMENT_WEBHOOK_SECRET`. Event IDs prevent a duplicated result from releasing inventory twice. Confirmation retains the reservation; failure or hold expiry releases it.

Room status is a default; `room_status_overrides` changes a room's status for a dated range (`starts_on` inclusive, `ends_on` exclusive). The database rejects room or override changes that would reduce capacity below already reserved current or future category nights. Operational writes must use READ COMMITTED isolation. Bulk operational edits should lock affected categories in a stable order and retry a deadlock; Booking retries its reservation transaction before returning `503` if conflicts persist. An admin API for scheduling those changes has not been added. Because inventory is sold by category and night, later physical room assignment must account for rooms that differ across nights of the stay.

## Verify

From `services/booking`, run `go test ./...`. Database integration tests run when `BOOKING_TEST_DATABASE_URL` points to a disposable PostgreSQL database with the Flyway migrations applied. The availability integration check also reads `DATABASE_URL` when set.

## Integration boundary

`guestId` in the booking request is a temporary Auth-service integration field for local development. It is not authenticated by this service. Keep the API on localhost until an Auth service or trusted gateway derives guest identity from a verified token and passes it to Booking. The payment route authenticates callbacks with its shared secret, but no Payment service is implemented in this repository yet.

Hotel names and addresses in the local seed come from Overture Maps; room inventory and prices are generated test data. See [seed instructions](db/seeds/README.md).
