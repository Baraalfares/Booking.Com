package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"example.com/bookingcom/internal/application"
)

type availableCategoryResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	MaxGuests         int    `json:"maxGuests"`
	NightlyPriceMinor int64  `json:"nightlyPriceMinor"`
	LineTotalMinor    int64  `json:"lineTotalMinor"`
	RemainingRooms    int    `json:"remainingRooms"`
	CanBook           bool   `json:"canBook"`
}

type availabilityResponse struct {
	HotelID    string                      `json:"hotelId"`
	Currency   string                      `json:"currency"`
	CheckIn    string                      `json:"checkIn"`
	CheckOut   string                      `json:"checkOut"`
	GuestCount int                         `json:"guestCount"`
	RoomCount  int                         `json:"roomCount"`
	Categories []availableCategoryResponse `json:"categories"`
}

func RegisterAvailabilityRoutes(mux *http.ServeMux, useCase application.GetHotelAvailability) {
	mux.HandleFunc("GET /hotels/{hotelID}/availability", func(w http.ResponseWriter, r *http.Request) {
		hotelID := r.PathValue("hotelID")
		if !canonicalUUID.MatchString(hotelID) {
			writeAvailabilityError(w, r, http.StatusBadRequest, "invalid hotel ID")
			return
		}
		query := r.URL.Query()
		checkIn, checkInErr := time.Parse(time.DateOnly, query.Get("check_in"))
		checkOut, checkOutErr := time.Parse(time.DateOnly, query.Get("check_out"))
		guests, guestsErr := strconv.Atoi(query.Get("guests"))
		rooms, roomsErr := strconv.Atoi(query.Get("rooms"))
		if checkInErr != nil || checkOutErr != nil || guestsErr != nil || roomsErr != nil {
			writeAvailabilityError(w, r, http.StatusBadRequest, "check_in and check_out must be YYYY-MM-DD; guests and rooms must be integers")
			return
		}

		result, err := useCase.Execute(r.Context(), application.GetHotelAvailabilityRequest{
			HotelID: hotelID, CheckIn: checkIn, CheckOut: checkOut,
			GuestCount: guests, RoomCount: rooms,
		})
		switch {
		case errors.Is(err, application.ErrInvalidAvailabilityRequest):
			writeAvailabilityError(w, r, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, application.ErrHotelNotFound):
			writeAvailabilityError(w, r, http.StatusNotFound, "hotel not found")
			return
		case err != nil:
			slog.ErrorContext(r.Context(), "get hotel availability", "hotel_id", hotelID, "error", err)
			writeAvailabilityError(w, r, http.StatusInternalServerError, "internal server error")
			return
		}

		categories := make([]availableCategoryResponse, 0, len(result.Categories))
		for _, category := range result.Categories {
			categories = append(categories, availableCategoryResponse{
				ID:                category.CategoryID,
				Name:              category.Name,
				MaxGuests:         category.MaxGuests,
				NightlyPriceMinor: category.NightlyPriceMinor,
				LineTotalMinor:    category.LineTotalMinor,
				RemainingRooms:    category.RemainingRooms,
				CanBook:           category.CanBook,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(availabilityResponse{
			HotelID: hotelID, Currency: result.Currency,
			CheckIn: checkIn.Format(time.DateOnly), CheckOut: checkOut.Format(time.DateOnly),
			GuestCount: guests, RoomCount: rooms, Categories: categories,
		}); err != nil {
			slog.ErrorContext(r.Context(), "write hotel availability response", "error", err)
		}
	})
}

func writeAvailabilityError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: message}); err != nil {
		slog.ErrorContext(r.Context(), "write availability error response", "error", err)
	}
}
