# Booking.Com

A learning project for building a hotel booking system with microservices and practical system design. The first service, **Booking**, is implemented in Go. Auth and Payment are planned as separate services.

Booking sells room **categories** for a date range. A booking can contain multiple categories at one hotel. It reserves every requested category and night in a single PostgreSQL transaction, saves the accepted price, and releases the hold if payment fails or the hold expires. Specific room numbers are assigned later.

## Current status

| Component | Status |
| --- | --- |
| Booking API | Implemented locally: category listing, availability, booking creation, payment-result callback, and health endpoints. |
| PostgreSQL schema | Flyway migrations V1–V8, including category-night inventory and capacity safeguards. |
| Auth service | Planned. Booking currently accepts a caller-supplied `guestId` for local development. |
| Payment service | Planned. Booking currently exposes a signed callback for payment results. |
| Admin API and room assignment | Planned for a later stage. |

The API and database ports are bound to localhost in Docker Compose. The project is not yet configured for public deployment.

## Repository layout

```text
services/booking/          Go Booking microservice
  cmd/server/              HTTP server and background expiry worker
  internal/domain/         Business data types
  internal/application/    Use cases and repository interfaces
  internal/api/            HTTP handlers and request/response types
  internal/postgres/       PostgreSQL adapters and transactions
  db/migrations/           Flyway schema migrations
  db/seeds/                Optional hotel dataset and generated inventory
docs/booking-service.md    Architecture, testing, and concurrency guide
docs/go-clean-architecture.md
compose.yaml               Local PostgreSQL, Flyway, and Booking API
```

## Run locally

You need Docker Compose. To run the Go service directly, install the Go version specified in [`go.mod`](services/booking/go.mod).

1. Copy [`.env.example`](.env.example) to `.env` without overwriting an existing file: `cp -n .env.example .env`.
2. In `.env`, set `BOOKING_DB_PASSWORD`, `BOOKING_FLYWAY_URL`, and `BOOKING_DATABASE_URL` for the same local Compose database. The Flyway and Booking URLs use the Compose hostname `booking-db`. Set `PAYMENT_WEBHOOK_SECRET` to a random value of at least 32 bytes; `openssl rand -hex 32` generates one. `.env` is ignored by Git.
3. Start the stack and check readiness:

```bash
docker compose up -d --build booking-api
curl -i http://127.0.0.1:8080/health/ready
```

Readiness returns `204 No Content` when Booking can reach PostgreSQL. Compose applies migrations before starting the API. To stop the stack, run `docker compose down`.

## Test and learn

```bash
cd services/booking
go test ./...
```

Database integration tests also need a disposable PostgreSQL database and `BOOKING_TEST_DATABASE_URL`; the [Booking service guide](docs/booking-service.md) gives the setup and manual HTTP examples. Optional [seed instructions](services/booking/db/seeds/README.md) load real hotel names and locations with generated room inventory and prices for local testing.

For the API contract, see the [Booking service README](services/booking/README.md). For Go types, Clean Architecture layers, transaction boundaries, and how concurrent bookings avoid overselling, read the [service guide](docs/booking-service.md).
