package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/bookingcom/internal/api"
	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/config"
	"example.com/bookingcom/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	err = pool.Ping(startupCtx)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	hotels := postgres.NewHotelRepository(pool)
	categories := postgres.NewRoomCategoryRepository(pool)
	availability := postgres.NewAvailabilityRepository(pool)
	bookings := postgres.NewBookingRepository(pool)

	mux := http.NewServeMux()
	api.RegisterCategoryRoutes(mux, application.ListHotelCategories{
		Hotels: hotels, Categories: categories,
	})
	api.RegisterAvailabilityRoutes(mux, application.GetHotelAvailability{
		Hotels: hotels, Availability: availability,
	})
	api.RegisterBookingRoutes(mux, application.CreateBooking{Bookings: bookings})
	if err := api.RegisterPaymentRoutes(mux, application.ProcessPaymentResult{
		Repository: bookings,
	}, cfg.PaymentWebhookSecret); err != nil {
		return fmt.Errorf("register payment route: %w", err)
	}
	registerHealthRoutes(mux, pool)

	handlerCtx, cancelHandlers := context.WithCancel(context.Background())
	defer cancelHandlers()
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		BaseContext:       func(net.Listener) context.Context { return handlerCtx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serviceCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	workerCtx, stopWorker := context.WithCancel(serviceCtx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		runExpiryWorker(workerCtx, application.ExpirePendingBookings{Repository: bookings})
	}()

	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	slog.Info("booking service listening", "address", cfg.HTTPAddr)

	var serveErr error
	select {
	case <-serviceCtx.Done():
		slog.Info("booking service stopping")
	case serveErr = <-serverErrors:
	}
	stopWorker()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancelShutdown()
	if shutdownErr != nil {
		// Graceful shutdown timed out. Cancel active database operations before
		// the deferred pool.Close waits for acquired connections to return.
		cancelHandlers()
		_ = server.Close()
	}
	<-workerDone
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", serveErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("shut down HTTP server: %w", shutdownErr)
	}
	return nil
}

func registerHealthRoutes(mux *http.ServeMux, pool *pgxpool.Pool) {
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			slog.ErrorContext(r.Context(), "readiness database check failed", "error", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func runExpiryWorker(ctx context.Context, useCase application.ExpirePendingBookings) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		// Drain a bounded backlog at startup and on each tick.
		for batch := 0; batch < 10 && ctx.Err() == nil; batch++ {
			jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			count, err := useCase.Execute(jobCtx, 100)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("expire pending bookings", "error", err)
				}
				break
			}
			if count > 0 {
				slog.Info("expired pending bookings", "count", count)
			}
			if count < 100 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
