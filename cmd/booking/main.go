package main

import (
	"log"

	"example.com/bookingcom/internal/booking/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Booking service configured for %s", cfg.HTTPAddr)
}
