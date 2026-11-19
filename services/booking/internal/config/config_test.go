package config

import (
	"strings"
	"testing"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PAYMENT_WEBHOOK_SECRET", strings.Repeat("x", 32))
	if _, err := Load(); err == nil {
		t.Fatal("expected an error when DATABASE_URL is empty")
	}
}

func TestLoadRequiresPaymentSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "configured-test-database")
	t.Setenv("PAYMENT_WEBHOOK_SECRET", "short")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a short payment secret")
	}
}

func TestLoadDefaultsToLoopbackAddress(t *testing.T) {
	t.Setenv("DATABASE_URL", "configured-test-database")
	t.Setenv("PAYMENT_WEBHOOK_SECRET", strings.Repeat("x", 32))
	t.Setenv("HTTP_ADDR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("unexpected default HTTP address: %q", cfg.HTTPAddr)
	}
}
