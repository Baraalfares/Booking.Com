package domain

import "time"

type Booking struct {
	BookingID  int64
	Status     BookingStatus
	Items      []BookingItem
	HotelID    string
	CheckIn    time.Time
	CheckOut   time.Time
	TotalMinor int64 // will be stored with cents
	Currency   string
}
type BookingStatus string

const (
	pending_payment BookingStatus = "PENDING_PAYMENT"
	confirmed       BookingStatus = "CONFIRMED"
	failed          BookingStatus = "FAILED"
	expired         BookingStatus = "EXPIRED"
)

type BookingItem struct {
	CategoryID     string
	RoomCount      int
	GuestCount     int
	LineTotalMinor int64
}
