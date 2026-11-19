package domain

import "time"

type Booking struct {
	BookingID  int64
	GuestID    string
	Status     BookingStatus
	Items      []BookingItem
	HotelID    string
	CheckIn    time.Time
	CheckOut   time.Time
	TotalMinor int64
	Currency   string
	ExpiresAt  time.Time
}
type BookingStatus string

const (
	BookingStatusPendingPayment BookingStatus = "PENDING_PAYMENT"
	BookingStatusConfirmed      BookingStatus = "CONFIRMED"
	BookingStatusFailed         BookingStatus = "FAILED"
	BookingStatusExpired        BookingStatus = "EXPIRED"
)

type BookingItem struct {
	CategoryID        string
	RoomCount         int
	GuestCount        int
	NightlyPriceMinor int64
	LineTotalMinor    int64
}
