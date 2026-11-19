package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"example.com/bookingcom/internal/application"
)

var canonicalUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type categoryOfferingResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	MaxGuests         int    `json:"maxGuests"`
	NightlyPriceMinor int64  `json:"nightlyPriceMinor"`
}

type listHotelCategoriesResponse struct {
	HotelID    string                     `json:"hotelId"`
	Currency   string                     `json:"currency"`
	Categories []categoryOfferingResponse `json:"categories"`
}

// RegisterCategoryRoutes adds the public category-listing endpoint to the service mux.
func RegisterCategoryRoutes(mux *http.ServeMux, useCase application.ListHotelCategories) {
	mux.HandleFunc("GET /hotels/{hotelID}/categories", func(w http.ResponseWriter, r *http.Request) {
		hotelID := r.PathValue("hotelID")
		if !canonicalUUID.MatchString(hotelID) {
			writeCategoryError(w, r, http.StatusBadRequest, "invalid hotel ID")
			return
		}

		result, err := useCase.Execute(r.Context(), hotelID)
		if errors.Is(err, application.ErrHotelNotFound) {
			writeCategoryError(w, r, http.StatusNotFound, "hotel not found")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "list hotel categories", "hotel_id", hotelID, "error", err)
			writeCategoryError(w, r, http.StatusInternalServerError, "internal server error")
			return
		}

		categories := make([]categoryOfferingResponse, 0, len(result.Categories))
		for _, category := range result.Categories {
			categories = append(categories, categoryOfferingResponse{
				ID:                category.ID,
				Name:              category.Name,
				MaxGuests:         category.MaxGuests,
				NightlyPriceMinor: category.NightlyPriceMinor,
			})
		}
		response := listHotelCategoriesResponse{
			HotelID:    hotelID,
			Currency:   result.Currency,
			Categories: categories,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			slog.ErrorContext(r.Context(), "write hotel categories response", "error", err)
		}
	})
}

func writeCategoryError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: message}); err != nil {
		slog.ErrorContext(r.Context(), "write category error response", "error", err)
	}
}
