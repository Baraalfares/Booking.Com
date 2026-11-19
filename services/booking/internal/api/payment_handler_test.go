package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/bookingcom/internal/application"
	"example.com/bookingcom/internal/domain"
)

const testPaymentSecret = "local-test-payment-secret-32-bytes-minimum"

type fakePaymentRepository struct {
	result application.PaymentResult
	status domain.BookingStatus
	err    error
	calls  int
}

func (f *fakePaymentRepository) ApplyPaymentResult(_ context.Context, result application.PaymentResult) (domain.BookingStatus, error) {
	f.calls++
	f.result = result
	return f.status, f.err
}

func paymentTestMux(t *testing.T, repo *fakePaymentRepository) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	err := RegisterPaymentRoutes(mux, application.ProcessPaymentResult{Repository: repo}, testPaymentSecret)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func signedPaymentRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/internal/payment-results", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(testPaymentSecret))
	_, _ = mac.Write([]byte(body))
	request.Header.Set("X-Payment-Signature", fmt.Sprintf("sha256=%x", mac.Sum(nil)))
	return request
}

func TestPaymentHandlerRequiresValidSignature(t *testing.T) {
	repo := &fakePaymentRepository{status: domain.BookingStatusConfirmed}
	mux := paymentTestMux(t, repo)
	request := httptest.NewRequest(http.MethodPost, "/internal/payment-results", strings.NewReader(`{"eventId":"evt-1","bookingId":42,"outcome":"SUCCEEDED"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || repo.calls != 0 {
		t.Fatalf("status=%d calls=%d; want 401 and no repository call", response.Code, repo.calls)
	}
}

func TestPaymentHandlerProcessesSignedResult(t *testing.T) {
	repo := &fakePaymentRepository{status: domain.BookingStatusConfirmed}
	mux := paymentTestMux(t, repo)
	request := signedPaymentRequest(`{"eventId":"evt-2","bookingId":42,"outcome":"SUCCEEDED"}`)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repo.calls != 1 {
		t.Fatalf("status=%d calls=%d; want 200 and one repository call", response.Code, repo.calls)
	}
	if repo.result.EventID != "evt-2" || repo.result.BookingID != 42 || repo.result.Outcome != application.PaymentSucceeded {
		t.Fatalf("unexpected payment result: %+v", repo.result)
	}
	if !strings.Contains(response.Body.String(), `"status":"CONFIRMED"`) {
		t.Fatalf("unexpected response body: %s", response.Body.String())
	}
}

func TestPaymentHandlerRejectsOversizedBody(t *testing.T) {
	repo := &fakePaymentRepository{}
	mux := paymentTestMux(t, repo)
	request := signedPaymentRequest(strings.Repeat("x", maxPaymentBodyBytes+1))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || repo.calls != 0 {
		t.Fatalf("status=%d calls=%d; want 413 and no repository call", response.Code, repo.calls)
	}
}

func TestPaymentHandlerRejectsLateSuccess(t *testing.T) {
	repo := &fakePaymentRepository{status: domain.BookingStatusExpired, err: application.ErrBookingExpired}
	mux := paymentTestMux(t, repo)
	request := signedPaymentRequest(`{"eventId":"evt-3","bookingId":42,"outcome":"SUCCEEDED"}`)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d; want 409", response.Code)
	}
}
