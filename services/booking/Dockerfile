FROM golang:1.27.2-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/booking ./cmd/server

FROM alpine:3.24
RUN apk add --no-cache ca-certificates \
    && addgroup -S booking \
    && adduser -S -G booking booking
COPY --from=build /out/booking /usr/local/bin/booking
USER booking
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/booking"]
