package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
)

var bookingUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type createBookingRequest struct {
	GuestID  string `json:"guestId"`
	CheckIn  string `json:"checkIn"`
	CheckOut string `json:"checkOut"`
	Items    []struct {
		CategoryID string `json:"categoryId"`
		RoomCount  int    `json:"roomCount"`
		GuestCount int    `json:"guestCount"`
	} `json:"items"`
}

type bookingItemResponse struct {
	CategoryID        string `json:"categoryId"`
	RoomCount         int    `json:"roomCount"`
	GuestCount        int    `json:"guestCount"`
	NightlyPriceMinor int64  `json:"nightlyPriceMinor"`
	LineTotalMinor    int64  `json:"lineTotalMinor"`
}

type bookingResponse struct {
	ID         int64                 `json:"id"`
	GuestID    string                `json:"guestId"`
	HotelID    string                `json:"hotelId"`
	Status     domain.BookingStatus  `json:"status"`
	CheckIn    string                `json:"checkIn"`
	CheckOut   string                `json:"checkOut"`
	Currency   string                `json:"currency"`
	TotalMinor int64                 `json:"totalMinor"`
	ExpiresAt  time.Time             `json:"expiresAt"`
	Items      []bookingItemResponse `json:"items"`
}

// RegisterBookingRoutes adds booking creation. guestId in this local API is an
// Auth integration seam; a public deployment must derive it from a trusted
// authenticated identity instead of accepting it from the request body.
func RegisterBookingRoutes(mux *http.ServeMux, useCase application.CreateBooking) {
	mux.HandleFunc("POST /hotels/{hotelID}/bookings", func(w http.ResponseWriter, r *http.Request) {
		hotelID := r.PathValue("hotelID")
		if !bookingUUID.MatchString(hotelID) {
			writeBookingError(w, r, http.StatusBadRequest, "invalid hotel ID")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeBookingError(w, r, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if strings.TrimSpace(key) == "" {
			writeBookingError(w, r, http.StatusBadRequest, "Idempotency-Key header is required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request createBookingRequest
		if err := decoder.Decode(&request); err != nil {
			writeBookingError(w, r, http.StatusBadRequest, "invalid booking JSON")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeBookingError(w, r, http.StatusBadRequest, "booking JSON must contain one object")
			return
		}
		checkIn, err := time.Parse(time.DateOnly, request.CheckIn)
		if err != nil {
			writeBookingError(w, r, http.StatusBadRequest, "checkIn must be YYYY-MM-DD")
			return
		}
		checkOut, err := time.Parse(time.DateOnly, request.CheckOut)
		if err != nil {
			writeBookingError(w, r, http.StatusBadRequest, "checkOut must be YYYY-MM-DD")
			return
		}
		items := make([]application.CreateBookingItem, 0, len(request.Items))
		for _, item := range request.Items {
			if !bookingUUID.MatchString(item.CategoryID) {
				writeBookingError(w, r, http.StatusBadRequest, "invalid category ID")
				return
			}
			items = append(items, application.CreateBookingItem{
				CategoryID: item.CategoryID,
				RoomCount:  item.RoomCount,
				GuestCount: item.GuestCount,
			})
		}
		booking, err := useCase.Execute(r.Context(), application.CreateBookingCommand{
			GuestID:        request.GuestID,
			HotelID:        hotelID,
			CheckIn:        checkIn,
			CheckOut:       checkOut,
			Items:          items,
			IdempotencyKey: key,
		})
		if err != nil {
			writeCreateBookingError(w, r, err)
			return
		}
		responseItems := make([]bookingItemResponse, 0, len(booking.Items))
		for _, item := range booking.Items {
			responseItems = append(responseItems, bookingItemResponse{
				CategoryID: item.CategoryID, RoomCount: item.RoomCount,
				GuestCount: item.GuestCount, NightlyPriceMinor: item.NightlyPriceMinor,
				LineTotalMinor: item.LineTotalMinor,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(bookingResponse{
			ID: booking.BookingID, GuestID: booking.GuestID, HotelID: booking.HotelID,
			Status: booking.Status, CheckIn: booking.CheckIn.Format(time.DateOnly),
			CheckOut: booking.CheckOut.Format(time.DateOnly), Currency: booking.Currency,
			TotalMinor: booking.TotalMinor, ExpiresAt: booking.ExpiresAt, Items: responseItems,
		}); err != nil {
			slog.ErrorContext(r.Context(), "write booking response", "error", err)
		}
	})
}

func writeCreateBookingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidBooking):
		writeBookingError(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, application.ErrHotelNotFound), errors.Is(err, application.ErrCategoryNotFound):
		writeBookingError(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, application.ErrGuestCapacityExceeded):
		writeBookingError(w, r, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, application.ErrInsufficientAvailability), errors.Is(err, application.ErrIdempotencyConflict):
		writeBookingError(w, r, http.StatusConflict, err.Error())
	case errors.Is(err, application.ErrBookingBusy):
		w.Header().Set("Retry-After", "1")
		writeBookingError(w, r, http.StatusServiceUnavailable, "booking is temporarily busy")
	default:
		slog.ErrorContext(r.Context(), "create booking", "error", err)
		writeBookingError(w, r, http.StatusInternalServerError, "internal server error")
	}
}

func writeBookingError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: message}); err != nil {
		slog.ErrorContext(r.Context(), "write booking error response", "error", err)
	}
}
