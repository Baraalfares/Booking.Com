package config

import (
	"errors"
	"os"
)

type Config struct {
	DatabaseURL string
	HTTPAddr    string
}

func Load() (*Config, error) {
	databaseUrl := os.Getenv("DATABASE_URL")
	if databaseUrl == "" {
		return nil, errors.New("DATABASE_URL environment variable not set")
	}

	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	return &Config{
		DatabaseURL: databaseUrl,
		HTTPAddr:    httpAddr,
	}, nil
}
