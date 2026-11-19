package config

import (
	"errors"
	"os"
)

type Config struct {
	DatabaseURL          string
	HTTPAddr             string
	PaymentWebhookSecret string
}

func Load() (*Config, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL environment variable not set")
	}
	paymentWebhookSecret := os.Getenv("PAYMENT_WEBHOOK_SECRET")
	if len(paymentWebhookSecret) < 32 {
		return nil, errors.New("PAYMENT_WEBHOOK_SECRET must be at least 32 bytes")
	}

	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = "127.0.0.1:8080"
	}
	return &Config{
		DatabaseURL:          databaseURL,
		HTTPAddr:             httpAddr,
		PaymentWebhookSecret: paymentWebhookSecret,
	}, nil
}
