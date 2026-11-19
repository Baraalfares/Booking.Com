package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
)

const testHotelID = "11111111-1111-4111-8111-111111111111"

type stubHotelRepository struct {
	hotel domain.Hotel
	found bool
}

func (r stubHotelRepository) GetByID(context.Context, string) (domain.Hotel, bool, error) {
	return r.hotel, r.found, nil
}

type stubCategoryRepository struct {
	categories []domain.RoomCategory
}

func (r stubCategoryRepository) ListByHotelID(context.Context, string) ([]domain.RoomCategory, error) {
	return r.categories, nil
}

func TestListHotelCategoriesRoute(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		hotelFound bool
		categories []domain.RoomCategory
		wantStatus int
		wantCount  int
	}{
		{name: "invalid hotel ID", path: "/hotels/not-a-uuid/categories", wantStatus: http.StatusBadRequest},
		{name: "unknown hotel", path: "/hotels/" + testHotelID + "/categories", wantStatus: http.StatusNotFound},
		{name: "empty offerings", path: "/hotels/" + testHotelID + "/categories", hotelFound: true, wantStatus: http.StatusOK},
		{name: "one offering", path: "/hotels/" + testHotelID + "/categories", hotelFound: true, categories: []domain.RoomCategory{{ID: "22222222-2222-4222-8222-222222222222", Name: "Standard", MaxGuests: 2, NightlyPriceMinor: 10000}}, wantStatus: http.StatusOK, wantCount: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterCategoryRoutes(mux, application.ListHotelCategories{
				Hotels:     stubHotelRepository{hotel: domain.Hotel{ID: testHotelID, Currency: "USD"}, found: tt.hotelFound},
				Categories: stubCategoryRepository{categories: tt.categories},
			})

			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
			}
			if tt.wantStatus != http.StatusOK {
				return
			}

			var body struct {
				HotelID    string `json:"hotelId"`
				Currency   string `json:"currency"`
				Categories []struct {
					Name              string `json:"name"`
					NightlyPriceMinor int64  `json:"nightlyPriceMinor"`
				} `json:"categories"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.HotelID != testHotelID || body.Currency != "USD" || body.Categories == nil || len(body.Categories) != tt.wantCount {
				t.Fatalf("unexpected categories response: %+v", body)
			}
			if tt.wantCount == 1 && (body.Categories[0].Name != "Standard" || body.Categories[0].NightlyPriceMinor != 10000) {
				t.Fatalf("unexpected offering: %+v", body.Categories[0])
			}
		})
	}
}
