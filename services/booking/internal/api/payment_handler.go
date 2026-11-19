package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"example.com/bookingcom/internal/application"
)

const maxPaymentBodyBytes = 16 << 10

type paymentResultRequest struct {
	EventID   string                     `json:"eventId"`
	BookingID int64                      `json:"bookingId"`
	Outcome   application.PaymentOutcome `json:"outcome"`
}

// RegisterPaymentRoutes exposes the Booking side of the future Payment service
// contract. The signature covers the exact request bytes, including bookingId.
// The secret must come from service configuration and stay outside the repo.
func RegisterPaymentRoutes(mux *http.ServeMux, useCase application.ProcessPaymentResult, secret string) error {
	if len(secret) < 32 {
		return errors.New("payment webhook secret must be at least 32 bytes")
	}
	mux.HandleFunc("POST /internal/payment-results", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxPaymentBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writePaymentError(w, http.StatusRequestEntityTooLarge, "payment result is too large")
			} else {
				writePaymentError(w, http.StatusBadRequest, "cannot read payment result")
			}
			return
		}
		if !validPaymentSignature(secret, body, r.Header.Get("X-Payment-Signature")) {
			writePaymentError(w, http.StatusUnauthorized, "invalid payment signature")
			return
		}

		var request paymentResultRequest
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writePaymentError(w, http.StatusBadRequest, "invalid payment result JSON")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writePaymentError(w, http.StatusBadRequest, "payment result must contain one JSON object")
			return
		}

		status, err := useCase.Execute(r.Context(), application.PaymentResult{
			EventID:   request.EventID,
			BookingID: request.BookingID,
			Outcome:   request.Outcome,
		})
		switch {
		case errors.Is(err, application.ErrInvalidPaymentResult):
			writePaymentError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, application.ErrBookingNotFound):
			writePaymentError(w, http.StatusNotFound, err.Error())
			return
		case errors.Is(err, application.ErrBookingExpired),
			errors.Is(err, application.ErrBookingStateConflict),
			errors.Is(err, application.ErrPaymentEventConflict):
			writePaymentError(w, http.StatusConflict, err.Error())
			return
		case err != nil:
			slog.ErrorContext(r.Context(), "process payment result", "booking_id", request.BookingID, "error", err)
			writePaymentError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			BookingID int64  `json:"bookingId"`
			Status    string `json:"status"`
		}{BookingID: request.BookingID, Status: string(status)}); err != nil {
			slog.ErrorContext(r.Context(), "write payment result response", "error", err)
		}
	})
	return nil
}

func validPaymentSignature(secret string, body []byte, header string) bool {
	hexDigest, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	provided, err := hex.DecodeString(hexDigest)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func writePaymentError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: message}); err != nil {
		slog.Error("write payment error response", "error", fmt.Errorf("encode response: %w", err))
	}
}
