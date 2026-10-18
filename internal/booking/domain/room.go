package domain

type Room struct {
	ID         string
	HotelID    string
	CategoryID string
	Number     string
	RoomStatus RoomStatus
}

type RoomStatus string

const (
	RoomStatusActive       RoomStatus = "active"
	RoomStatusOutOfService RoomStatus = "out_of_service"
)
